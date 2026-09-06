# Step 2:優雅關機

step1 的問題:app 收到 SIGTERM 立刻結束,在途連線被硬斷。

**這步加的**(相對 step1):打開程式層的優雅關機 `HYDRA_GRACEFUL=on`。收到 SIGTERM 後,
app 停止接新連線、把在途的 HTTP 請求處理完再退出;這個等待最多 15 秒(hydra 的 shutdown 上限
`HYDRA_SHUTDOWN_TIMEOUT_SECONDS`,預設 15),等不到就強制收掉。

順帶把 `terminationGracePeriodSeconds` 從 30 拉到 45:preStop 先睡 15 秒、shutdown 最多再等 15 秒,
原本的 30 秒剛好貼著上限,拉到 45 留點餘裕,免得 SIGKILL 搶先到。

**背後的 code**([`internal/server/server.go`](../images/hydra/internal/server/server.go) 的關機序列):
`GRACEFUL=on` 時 main 有註冊 SIGTERM handler,`Run` 收到訊號後走到 `httpSrv.Shutdown`——它停收新連線、
**等在途請求跑完**,這就是 step1 被剪的那些 `/slow` 到這步能收完的原因:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout) // 預設 15s
defer cancel()
// Shutdown:停收新連線,但等在途請求做完再回
if err := httpSrv.Shutdown(shutdownCtx); err != nil {
    httpSrv.Close()   // 逾時(例如永不結束的 SSE)才強制剪線
    return fmt.Errorf("shutdown timeout exceeded: %w", err)
}
```

**Go 官方怎麼定義 `Shutdown`**([`net/http` · Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)),四個要點:

- **不中斷任何進行中的連線**(*without interrupting any active connections*)——在途 HTTP 請求會被好好收完,這就是 step2 的價值。
- 動作順序:先關掉所有 listener(不再收新連線)→ 關閉閒置連線 → **等使用中的連線回到閒置**再收。
- 傳入的 `context` 若在關完前逾時(這裡設 15 秒),`Shutdown` 回傳 context 的錯誤;程式據此 `Close()` 強制收尾。
- **`Shutdown` 不會替你收「長生命週期」的連線**:官方以 hijacked 的 WebSocket 為例,明講要「由呼叫端自己通知這些連線關閉」。SSE 雖非 hijacked,但同樣永不回到閒置,`Shutdown` 只能一直等到逾時。

所以 step2 的天花板正是最後這條:SSE 永不結束,`Shutdown` 等不到、只能撞 15 秒逾時被剪。要乾淨收掉它,得照官方說的「自己通知」——[step3](step3.md) 就是廣播 `bye`、主動收線。

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step2
```

相對 step1,改了兩處——`GRACEFUL` 與 `terminationGracePeriodSeconds`:

```yaml
      terminationGracePeriodSeconds: 45      # ← step2:30 → 45(留給優雅關機的時間)
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

## 觀察

oha 打 `/version` 從 step1 起就已經乾淨(短請求 preStop 就蓋掉了),step2 也一樣——所以要驗證
step2 到底修好什麼,得看 step1 剪掉的那批**在途請求**(`/slow`),以及它的天花板 **SSE 長連線**。

右終端照舊盯 pod:

```sh
watch -n1 'kubectl get pods -o wide'
```

**(a) 重跑 step1 的 `/slow` 壓測**(和 step1 同一條指令,才好對照那 5 個 `connection closed`):

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=20"'
```

**(b) 另開一個終端,掛一條 SSE**(先拿 session cookie、再連事件流),看 step2 的天花板:

```sh
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

會看到 `hello`、`station` 事件持續進來(留意 `pod` 欄位是哪一顆)。兩邊都跑著時,觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

## 預期現象

- **`/slow` 壓測(這裡用 `seconds=20`)**:成功率 66.67%,**還是有 5 個 `connection closed before
  message completed`——跟 step1 一樣沒少**。為什麼 graceful 沒救到?因為 `/slow?seconds=20` 要 20 秒,
  比 hydra 的 shutdown 上限 `HYDRA_SHUTDOWN_TIMEOUT_SECONDS`(15 秒)還長。graceful 確實有等
  ——**證據:成功的 10 條 latency 全是滿 20 秒才回**(裸奔的話早被切、不會是 20s)——但它最多只等
  15 秒;在 SIGTERM 時剩餘超過 15 秒的 `/slow`,撞到上限就被 `httpSrv.Close()` 強制關閉。
  → 一課:**graceful 的等待是有預算的(shutdown 上限),不是無限等**。想看它把在途請求「收完」
  而非撞上限,把 `seconds` 調到 15 以下(例如 `10`)重跑——請求短於 shutdown 上限,graceful 就來得及
  在剪線前收完:step1 仍會剪、step2 應該就歸零。
- **SSE 那條流**:它是**永遠不會結束**的長連線——不管你把 shutdown 上限設多大都容不下。app 的
  Shutdown 等不到它,撞到 15 秒上限後被強制剪斷。從 pod 進入 `Terminating` 算起大約 **30 秒**
  (preStop 15 + shutdown 15)SSE 才斷——不是立刻,但**還是斷了**。對照 step1:那裡 SIGTERM 一到
  app 立刻死、SSE 約 15 秒就斷;step2 多等它到上限、拖到約 30 秒——有等,只是「等」對無限長的連線沒用。

### 實際輸出範例(`/slow?seconds=20`)

```text
Summary:
  Success rate: 66.67%
  Total:        60.00 sec
  Slowest:      20.02 sec      ← 有回應的都花滿 20 秒(graceful 在等)

Status code distribution:
  [200] 10 responses

Error distribution:
  [5] connection closed before message completed   ← 需要 >15s、超過 shutdown 上限,被剪
  [5] aborted due to deadline                       ← -z 到點的中止,不算問題
```

和 step1 的 `/slow=20` 幾乎一模一樣(都 10 / 5 / 5)——差別在**原因**:step1 是 SIGTERM 一到就切;
step2 是等了滿 15 秒的上限才切。20 秒的請求在兩步都救不了,只是 step2「有試著等」。

## 還沒解決

優雅關機的「等」有預算上限,而且對**永不結束**的 SSE 根本沒用——再大的上限也容不下無限長的連線。
所以不能靠「等」,得讓 app 關機時**主動**跟 SSE 道別、收線 → [step3](step3.md)。
