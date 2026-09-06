# Step 1:preStop 緩衝

step0 的問題:pod 一被殺,Service 的 endpoint 還沒更新完,新連線就打到已經死掉的 pod ——
於是一堆 `Connection refused`。

**這步加的**(相對 step0 的唯一差別):一段 15 秒的 `preStop` 緩衝。

pod 進入終止流程時,兩件事同時開跑:控制面把它從 Service 的 endpoint 拿掉(標成 not-ready),
kubelet 則執行 `preStop`,讓 process 先多活 15 秒再收到 SIGTERM。注意這是並行,不是「先拿掉、
再等待」的保證順序——所以才需要這 15 秒:讓「這顆 pod 已拿掉」有時間傳到各節點的 datapath
(kube-proxy,這裡是 Cilium),新連線就逐漸不會再打到它。

(app 本身仍不處理關機,`HYDRA_GRACEFUL=off`。distroless 沒有 shell,所以 preStop 用 K8s 原生的
`sleep` action,不是 `exec` 跑 `sleep`。)

## 這步的 Deployment 關鍵設定

apply 之前先組出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step1
```

相對 step0,hydra Deployment 只多了 `lifecycle.preStop`:

```yaml
      containers:
      - name: hydra
        lifecycle:                 # ← step1 新增:拿掉 endpoint 後多撐 15 秒
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

沿用 [step0](step0.md) 的雙終端機——右邊 `watch` 盯 pods、左邊在 client 內跑 oha:

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 20 --disable-keepalive "$TARGET"'
```

壓測跑著時,另開終端機觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

## 預期現象

- oha 打 `/version`:這輪實測 **100% 成功、連線層錯誤掛零**——preStop 把「新連線打到將死 pod」
  的競態關掉了。`/version` 是短請求,preStop 期間新連線早已改道,等 SIGTERM 真的送到,舊 pod
  手上也幾乎沒有還在服務的連線可斷。對照 [step0](step0.md#實際輸出範例) 的 266 個連線層錯誤,差別很直接。
- 但這只代表「短請求 + 換手」這個組合被 preStop 蓋掉了,**不代表 step1 已經零停機**。還沒解的是:
  - **做到一半 / 較慢的請求**:若請求還沒處理完 SIGTERM 就到,裸奔 app 立刻死 → 照樣斷
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
一到、還在跑的請求被中止,與滾動更新無關)。step0 同樣的壓測有 266 個 `Connection refused` 等連線
錯誤,到 step1 直接歸零——這就是 preStop 對「短請求換手」的效果。

## 為什麼「做到一半的請求」還是會斷:背後的 code

`/version` 乾淨,是因為它太快——preStop 那 15 秒早就讓它跑完了。但只要有請求在 **SIGTERM 真正送到時
還沒回應完**(較長、還在處理的請求,尤其是永不結束的 SSE 長連線),step1 就救不了。因為 app 是裸奔的:
`HYDRA_GRACEFUL=off` 讓 main **不註冊** SIGTERM handler,收到 SIGTERM 直接由 Go runtime 終止程序,
連線當場被剪:

```go
// images/hydra/cmd/hydra/main.go
signals := make(chan os.Signal, 2)
if cfg.Graceful {
    signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
} else {
    // 不註冊 handler → 收到 SIGTERM 由 Go runtime 直接終止程序(exit 143),不 Shutdown、不等手上的連線
    logger.Warn("graceful shutdown DISABLED — SIGTERM 將直接終止程序")
}
```

這種「請求做到一半 / 長連線被硬斷」最容易在 **SSE** 上看到——它一定黏在某顆 pod、一定還在跑。step1 下 SIGTERM
一到,SSE 就被硬斷(約 `Terminating` 後 15 秒);到 [step2](step2.md) 的 graceful 會拖到約 30 秒(但仍斷),
step3 才乾淨收。這條 SSE 對照從 step2 開始跑。

## 盤點:step1 解決了什麼、還沒解決什麼

對照 [step0 那三類破口](step0.md#盤點step0-暴露了哪些問題):

| # | 問題 | step1 狀態 | 證據 |
|---|---|---|---|
| ① | 新連線被拒(`Connection refused` / `connection error`) | ✅ **解決** | oha `/version` 從 step0 的 266 個錯誤 → **0(100% 成功)**;preStop 拿掉 endpoint,新連線不再打到將死的 pod |
| ② | 請求做到一半 / 長連線被硬斷 | ❌ **沒解決** | 裸奔 app 收 SIGTERM 立即死(見上方 code);長連線最明顯——SSE 一到 SIGTERM 就斷 → **step2 / step3** |
| ③ | 狀態隨 pod 消失(進度歸零) | ❌ **沒解決** | step1 沒碰狀態,仍存 pod 記憶體 → **step4** |

**有沒有新問題?** 沒有。preStop 把短請求的錯誤清掉後,剩下的「請求做到一半 / 長連線被硬斷」是 step0 本來就有、只是被 `/version`(短請求)遮住的 ②,不是新問題。

(兩個設計代價、不算錯誤:preStop 讓每顆 pod 關機多等 15 秒,整體 rollout 稍慢;另外 step0–3 都沒 `readinessProbe`,新 pod 有「還沒 `listen` 完就收流量」的理論破口,這輪沒觸發、到 step4 才補。)

**下一步**:要收掉 ②——讓 app 收到 SIGTERM 後先把手上的請求好好做完——得靠程式層的優雅關機 → [step2](step2.md)。
