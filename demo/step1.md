# Step 1:preStop 緩衝

step0 的問題:pod 一被殺,Service 的 endpoint 還沒更新完,新連線就打到已經死掉的 pod ——
於是一堆 `Connection refused`。

**這步加的**(相對 step0 的唯一差別):一段 15 秒的 `preStop` 緩衝。

pod 進入終止流程時,兩件事同時開跑:控制面把它從 Service 的 endpoint 摘掉(標成 not-ready),
kubelet 則執行 `preStop`,讓 process 先多活 15 秒再收到 SIGTERM。注意這是並行,不是「先摘掉、
再等待」的保證順序——所以才需要這 15 秒:讓「這顆 pod 已摘掉」有時間傳到各節點的 datapath
(kube-proxy,這裡是 Cilium),新連線就逐漸不會再打到它。

(app 本身仍不處理關機,`HYDRA_GRACEFUL=off`。distroless 沒有 shell,所以 preStop 用 K8s 原生的
`sleep` action,不是 `exec` 跑 `sleep`。)

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step1
```

相對 step0,hydra Deployment 只多了 `lifecycle.preStop`:

```yaml
      containers:
      - name: hydra
        lifecycle:                 # ← step1 新增:摘除 endpoint 後多撐 15 秒
          preStop:
            sleep: {seconds: 15}
        # env(graceful=off / memory / drain=off)、livenessProbe、tGPS 30、
        # nodeSelector / affinity / strategy 都與 step0 相同
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step1
kubectl rollout status deploy/hydra
```

## 觀察

沿用 [step0](step0.md) 的雙終端——右邊 `watch` 盯 pods、左邊在 client 內跑 oha:

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 20 --disable-keepalive "$TARGET"'
```

壓測跑著時,另開終端觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

## 預期現象

- oha 打 `/version`:這輪實測 **100% 成功、連線層錯誤掛零**——preStop 把「新連線打到將死 pod」
  的競態關掉了。`/version` 是短請求,preStop 期間新連線早已改道,等 SIGTERM 真的送到,舊 pod
  手上也幾乎沒有在途連線可斷。對照 [step0](step0.md#實際輸出範例) 的 266 個連線層錯誤,差別很直接。
- 但這只代表「短請求 + 換手」這個組合被 preStop 蓋掉了,**不代表 step1 已經零停機**。還沒解的是:
  - **在途 / 較慢的請求**:若請求還沒處理完 SIGTERM 就到,裸奔 app 立刻死 → 照樣斷
    (step2 的優雅關機才會把手上的請求收完)。
  - **SSE 長連線**:preStop 到期、SIGTERM 一到就被硬斷——用 `/version` 這種短請求看不到,
    得掛一條 SSE 才看得出來(step2 起改善、step3 才乾淨收)。
  - (次要)**啟動端**:step0–3 沒有 readinessProbe,新 pod 若還沒 `listen` 完就收流量,
    理論上會有零星 `Connection refused`;這輪沒遇到,但不保證每次都沒有,step4 加 readiness 才補上。

### 實際輸出範例

期間觸發滾動更新的一輪壓測,oha 收尾的統計(此例跑了約 120 秒):

```text
Summary:
  Success rate: 100.00%
  Total:        120.07 sec
  Requests/sec: 1351.96

Status code distribution:
  [200] 162327 responses

Error distribution:
  [7] aborted due to deadline
```

16 萬個請求、**100% 成功**,連線層錯誤掛零(那 7 個 `aborted due to deadline` 是 `-z`(壓測時間)
到點、還在途的請求被中止,與滾動更新無關)。step0 同樣的壓測有 266 個 `Connection refused` 等連線
錯誤,到 step1 直接歸零——這就是 preStop 對「短請求換手」的效果。

## 用 /slow 看在途請求的破口

`/version` 太快,preStop 一擋就乾淨——所以它看不到 step1 真正的破口:**在途、還沒回應完的請求**。
hydra 有個 `/slow?seconds=N` 端點會停 N 秒才回應(預設 3、上限 60),專門用來看這件事。

要讓請求「SIGTERM 到時還在處理中」,`seconds` 得比 preStop(15 秒)長一點——用 20 秒,
並發別開高(每條請求會佔著連線 20 秒):

```sh
# 左終端:持續打 /slow?seconds=20
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=20"'

# 右終端:壓測跑著時,觸發滾動更新
kubectl rollout restart deploy/hydra
```

### 實際輸出範例

```text
Summary:
  Success rate: 66.67%
  Total:        60.00 sec
  Requests/sec: 0.3333

Status code distribution:
  [200] 10 responses

Error distribution:
  [5] connection closed before message completed
  [5] aborted due to deadline
```

15 條有結論的請求裡(10 成功 + 5 失敗 = 66.67%),那 **5 條 `connection closed before message
completed`** 就是「SIGTERM 到時還在處理」的 `/slow`——step1 裸奔的 app 立刻結束,server 還沒把
回應送完就把連線關了。這正是 `/version` 遮掉、而 `/slow` 揪出來的破口:短請求看不到,慢請求一測就現形。

**背後的 code**([`images/hydra/cmd/hydra/main.go`](../images/hydra/cmd/hydra/main.go)):`HYDRA_GRACEFUL=off`
讓 main **不註冊** SIGTERM handler,收到 SIGTERM 就由 Go runtime 直接終止程序——在途 `/slow` 於是被硬斷:

```go
signals := make(chan os.Signal, 2)
if cfg.Graceful {
    signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
} else {
    // 不註冊 handler → 收到 SIGTERM 由 Go runtime 直接終止程序(exit 143),
    // 不 Shutdown、不等在途連線
    logger.Warn("graceful shutdown DISABLED — SIGTERM 將直接終止程序")
}
```

而 `/slow` 本身([`internal/server/handlers.go`](../images/hydra/internal/server/handlers.go))就是停 N 秒才回應、
用來製造「在途請求」的端點:

```go
// GET /slow?seconds=N(預設 3、上限 60)
timer := time.NewTimer(d)
select {
case <-timer.C:                 // 睡滿 N 秒 → 正常回應
case <-r.Context().Done():      // 連線被關(pod 死/ client 斷)→ 提早結束
    return
}
```

(另外 5 條 `aborted due to deadline` 是 `-z 60s` 到點時還在途的請求,被壓測工具中止,不是 app 造成的;
每條要 20 秒、又只有 5 併發,一分鐘總量本來就少。)

到 [step2](step2.md) 打開 graceful 後,同一批在途 `/slow` 會被好好等完、回乾淨的 200,
`connection closed` 就會消失。

## 還沒解決

短請求換手已經乾淨,但**在途 / 長連線**在 SIGTERM 到來時還是被硬斷(上面的 `/slow` 就是證據)。
要讓 app 收到 SIGTERM 後先把手上的請求好好收完,得靠程式層的優雅關機 → [step2](step2.md)。
