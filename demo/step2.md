# Step 2:優雅關機(graceful shutdown)

step1 的問題:app 收到 SIGTERM 立刻結束,在途連線被硬斷。

**這步加的**(相對 step1):打開程式層的優雅關機 `HYDRA_GRACEFUL=on`。收到 SIGTERM 後,app 停止接
新連線、把在途的 HTTP 請求處理完再退出,最多等 `HYDRA_SHUTDOWN_TIMEOUT_SECONDS`(預設 15 秒),
等不到就強制收掉。

但**光開 graceful 還不夠——它得有時間跑完**,而那段時間是 `terminationGracePeriodSeconds`(tGPS)
給的。tGPS 從 pod 進入 `Terminating` 起算、**包含 preStop**,到點就 SIGKILL。所以要讓 graceful 收得完:

```
tGPS ≥ preStop(15s) + shutdown 上限(15s)
```

step2 把 tGPS 設成 **45**(15 preStop + 15 shutdown + 15 裕度)。`GRACEFUL` 和 `tGPS` 是一組的,
少搭一個就出事——下面「反例」親眼看。

**背後的 code**([`internal/server/server.go`](../images/hydra/internal/server/server.go) 的關機序列):
`GRACEFUL=on` 時 main 有註冊 SIGTERM handler,`Run` 收到訊號後走到 `httpSrv.Shutdown`——停收新連線、
等在途請求在 shutdown 上限內跑完:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout) // 預設 15s
defer cancel()
if err := httpSrv.Shutdown(shutdownCtx); err != nil {
    httpSrv.Close()   // 逾時(例如永不結束的 SSE)才強制剪線
    return fmt.Errorf("shutdown timeout exceeded: %w", err)
}
```

**Go 官方怎麼定義 `Shutdown`**([`net/http` · Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)),四個要點:

- **不中斷任何進行中的連線**(*without interrupting any active connections*)——在途 HTTP 請求會被好好收完。
- 動作順序:先關掉所有 listener(不再收新連線)→ 關閉閒置連線 → **等使用中的連線回到閒置**再收。
- 傳入的 `context` 若在關完前逾時(這裡設 15 秒),`Shutdown` 回傳 context 的錯誤;程式據此 `Close()` 強制收尾。
- **`Shutdown` 不會替你收「長生命週期」的連線**:官方以 hijacked 的 WebSocket 為例,明講要「由呼叫端自己通知這些連線關閉」。SSE 雖非 hijacked,但同樣永不回到閒置,`Shutdown` 只能一直等到逾時。

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step2
```

相對 step1,改了兩處——`GRACEFUL` 與 `terminationGracePeriodSeconds`,而且這兩個要**搭配**:

```yaml
      terminationGracePeriodSeconds: 45      # ← step2:30 → 45(= preStop 15 + shutdown 15 + 裕度 15)
      containers:
      - name: hydra
        env:
        - {name: HYDRA_GRACEFUL, value: "on"}          # ← step2:off → on(優雅關機)
        - {name: HYDRA_STATE_BACKEND, value: memory}
        - {name: HYDRA_SSE_DRAIN, value: "off"}         # 仍不排空 SSE
        - {name: HYDRA_TOUR_INTERVAL_SECONDS, value: "10"}
        # lifecycle.preStop(15s)沿用 step1;nodeSelector / affinity / strategy / liveness 同前
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step2
kubectl rollout status deploy/hydra
```

## 反例:tGPS 太短,graceful 會被 SIGKILL 廢掉

先示範「搭配沒做好」會怎樣。時間軸是這樣——tGPS 到點,不管 graceful 收完沒,一律 SIGKILL:

```
Terminating ──preStop 15s──► SIGTERM ──app graceful(最多 15s)──► 乾淨結束
     │                                                              
     └───────────────── tGPS 到點就 SIGKILL(硬砍)───────────────►
```

臨時把 tGPS 改成太短的 **20**(< 15+15),掛一條 SSE、再觸發滾動更新:

```sh
# 臨時改小(會觸發一次 rollout,等它換完)
kubectl patch deploy hydra -p '{"spec":{"template":{"spec":{"terminationGracePeriodSeconds":20}}}}'
kubectl rollout status deploy/hydra

# 掛一條 SSE(拿 cookie → 連事件流),留意它在第幾秒斷
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 另開終端觸發滾動更新
kubectl rollout restart deploy/hydra
```

**預期(依 K8s tGPS 生命週期)**:preStop 吃掉 15 秒,SIGTERM 後 graceful 只剩 **5 秒**就被 SIGKILL——
SSE 大約在 `Terminating` 後第 **20 秒**就被硬砍(比正常 tGPS=45 的約 30 秒更早),pod 以「grace period
到點被殺」的非正常狀態結束(可用 `kubectl get events` / `kubectl describe pod` 佐證)。**開了 graceful,
卻因為 tGPS 太短而形同虛設。**

> 這段的實際秒數/pod 結束狀態,建議照上面跑一次確認——不同環境的傳播時間會有些許差異。

看完把 tGPS 改回正解:

```sh
kubectl apply -k deploy/step2   # tGPS 回到 45
```

## 觀察:graceful 撐到上限,但 SSE 還是斷

tGPS 搭配好之後,再看 graceful 的**極限**。oha 打短的 `/version` 從 step1 起就一直是 100%(preStop 已
罩住),看不出 step2 的邊界;要看邊界得用**永不結束的 SSE**。掛一條 SSE、觸發滾動更新:

```sh
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

```sh
kubectl rollout restart deploy/hydra
```

**預期**:SSE 是永不回到閒置的長連線,`Shutdown` 等不到它,撞到 15 秒 shutdown 上限後被強制剪斷。
從 pod 進入 `Terminating` 算起約 **30 秒**(preStop 15 + shutdown 15)SSE 才斷——對照 step1:那裡
SIGTERM 一到 app 立刻死、SSE 約 15 秒就斷。step2 的 graceful **有等**(拖到約 30 秒),只是「等」對
無限長的連線沒用,最後還是撞上限被剪。

## 還沒解決

graceful 的「等」是有預算的(shutdown 上限),對**永不結束**的 SSE 再大的上限也容不下。所以不能靠
「等」,得讓 app 關機時**主動**跟 SSE 道別、收線 → [step3](step3.md)。
