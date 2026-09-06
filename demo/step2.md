# Step 2:優雅關機(graceful shutdown)

step1 的問題:app 收到 SIGTERM 立刻結束,還在服務的連線直接被斷掉。

**這步加的**(相對 step1):打開程式層的優雅關機 `HYDRA_GRACEFUL=on`。收到 SIGTERM 後,app 停止接
新連線、把手上還在處理的 HTTP 請求收完再結束,最多等 `HYDRA_SHUTDOWN_TIMEOUT_SECONDS`(預設 15 秒),
等不到就強制收掉。

一句話講完 step2:step1 是「**不等**」——SIGTERM 一到就死;step2 是「**等**」——把手上還在處理的請求收完
再走。整份 step2 就在講這個等:app 願意等多久、K8s 容不容得下它等、SIGTERM 那刻手上有沒有東西可等,
以及什麼東西是永遠等不完的。

app 願意等是一回事,K8s 讓不讓它等完是另一回事——那段時間是 `terminationGracePeriodSeconds`(tGPS)
給的。tGPS 從 pod 進入 `Terminating` 起算、**包含 preStop**,時間一到就 SIGKILL。所以要讓 graceful 收得完:

```
tGPS ≥ preStop(15s) + shutdown 上限(15s)
```

step2 把 tGPS 設成 **45**(15 preStop + 15 shutdown + 15 裕度)。這 45 秒是「等」的總預算,裡面有
**兩個時鐘**在跑:

```text
        +=================tGPS = 45s=================+
        |              |              |              |
        preStop        SIGTERM        shutdown       SIGKILL
        |              |              |              |
        v              v              v              v
 -------+--------------+--------------+--------------+-------> 時間
        0s             15s            30s            45s
```

- **第 15 秒 SIGTERM**:preStop 結束,app 開始「等」還在處理的請求。
- **第 30 秒 shutdown 上限**:app **自己的鐘**——等不完就自己剪線結束(app 主動放棄)。
- **第 45 秒 SIGKILL**:**K8s 的鐘**——tGPS 一到,強制砍。

正常是 app 的鐘先響(第 30 秒),自己乾淨收場;tGPS 要是小於 30,換 K8s 的鐘先響——那就是「白等」,
下面用反例親眼看看。

**背後的 code**([`internal/server/server.go`](../images/hydra/internal/server/server.go) 的關機序列):
`GRACEFUL=on` 時 main 註冊了 SIGTERM handler,收到訊號走到 `httpSrv.Shutdown`——「等」在 code 裡就是
這一行,ctx 的 timeout 就是 app 願意等的上限;等不到才 `Close()` 硬剪:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout) // 預設 15s
defer cancel()
if err := httpSrv.Shutdown(shutdownCtx); err != nil {
    httpSrv.Close()   // 逾時(例如永不結束的 SSE)才強制剪線
    return fmt.Errorf("shutdown timeout exceeded: %w", err)
}
```

**Go 官方怎麼定義 `Shutdown`**([`net/http` · Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)),
說穿了就是「等使用中的連線回到閒置」。四個要點,**第四點先記著**,結尾會用到:

- **不中斷任何進行中的連線**(*without interrupting any active connections*)——還在處理的 HTTP 請求會被好好收完。
- 動作順序:先關掉所有 listener(不再收新連線)→ 關閉閒置連線 → **等使用中的連線回到閒置**再收。
- 傳入的 `context` 若在關完前逾時(這裡設 15 秒),`Shutdown` 回傳 context 的錯誤;程式據此 `Close()` 強制收尾。
- **`Shutdown` 不會替你收「長生命週期」的連線**:官方以 hijacked 的 WebSocket 為例,明講要「由呼叫端自己通知這些連線關閉」。SSE 雖非 hijacked,但同樣永不回到閒置,`Shutdown` 只能一直等到逾時。

## 這步的 Deployment 關鍵設定

apply 之前先 render 出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step2
```

相對 step1 改了三處,剛好對應「等」的三件事——**要不要等**(`GRACEFUL`)、**app 願意等多久**
(`HYDRA_SHUTDOWN_TIMEOUT_SECONDS`)、**K8s 容不容得下它等**(`terminationGracePeriodSeconds` ≥
preStop + shutdown 上限),少一個都不行:

```yaml
      terminationGracePeriodSeconds: 45       # ← step2:30 → 45(= preStop 15 + shutdown 15 + 裕度 15)
      containers:
      - name: hydra
        env:
        - {name: HYDRA_GRACEFUL, value: "on"}                 # ← step2:off → on(優雅關機)
        - {name: HYDRA_SHUTDOWN_TIMEOUT_SECONDS, value: "15"} # ← step2 明寫:graceful 的等待上限
        - {name: HYDRA_STATE_BACKEND, value: memory}
        - {name: HYDRA_SSE_DRAIN, value: "off"}               # 仍不排空 SSE
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

## 先驗證:等得完(tGPS=45)

先證明「等」有用。用 oha 打 **`/slow?seconds=10`**——每個請求故意跑 10 秒,SIGTERM 一到那刻一定有幾個
還在跑(keep-alive 釘著,下面 keep-alive 段細講);而 10 秒 **< shutdown 上限 15**,所以 app 等得完:

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=10"'
```

指令拆解——`oha` 是一支 HTTP 壓測工具(類似 `hey` / `wrk`,會即時顯示成功率與延遲分佈):

- **`-z 60s`**:持續打 **60 秒**(`-z` 是時間模式;`-n` 才是固定次數)。
- **`-c 5`**:**5 條連線同時**打(concurrency = 5)。
- **`/slow?seconds=10`**:hydra 的測試端點,收到請求後**故意睡 10 秒**才回覆,專門製造「關機當下還在跑的長請求」。

**`/slow` 背後的 code**([`internal/server/handlers.go`](../images/hydra/internal/server/handlers.go) 的 `handleSlow`):
它不是傻傻 `time.Sleep`,而是在「計時器」和「請求的 context」之間 `select`——睡滿才回,連線一被關就立刻收工:

```go
timer := time.NewTimer(d)      // d = seconds 參數(預設 3、最多 60)
defer timer.Stop()
select {
case <-timer.C:                // 睡滿 d 秒 → 正常回 JSON(hostname / version)
case <-r.Context().Done():     // 連線被關(client 斷線 / server 收線)→ 提早收工,不留殭屍
    return
}
```

這就是 `/slow` 能當「關機邊界上一筆請求」乾淨探針的原因:平常睡滿 N 秒 = 一筆還在處理的請求;連線一旦被關
(graceful 收線、或 SIGKILL 後連線斷),`r.Context().Done()` 觸發、handler 立刻結束,不會卡住。

壓測跑著時,另開一個終端機觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

實測——Success rate **100%**:

```
Status code distribution:
  [200] 25 responses

Error distribution:
  [5] aborted due to deadline
```

`/slow=10` 全收完、`connection closed` **掛零**——app 的「等」確實把還在處理的請求收乾淨了。那 `[5] aborted due
to deadline` 跟關機無關:是 oha 自己 `-z 60s` 時間到時,還有 5 筆 `/slow=10` 還在跑就被中止(每個 10 秒,撞 60 秒
邊界必卡一批)。**這個雜訊先解掉,下面反例出現的 `connection closed` 才是唯一的訊號。**

等有用——但前提是 K8s 容得下它等。下面把 tGPS 改短,看會怎樣。

## 反例:tGPS 太短,等了也白等

app 想等,K8s 卻不讓它等完會怎樣?同一組壓測,只把 tGPS 從 45 改成 **20**。時間軸變這樣:

```text
        +==============tGPS = 20s===============+
        |                             |         |
        preStop                       SIGTERM   SIGKILL
        |                             |         |
        v                             v         v
 -------+-----------------------------+---------+---------+---------+---> 時間
        0s                            15s       20s       25s       30s
```

SIGKILL 在**第 20 秒**就砍下來;但 SIGTERM(15)後灌進來的 `/slow=10` 要到**第 25 秒**才收完,app 自己的
shutdown 上限更要到**第 30 秒**——K8s 的鐘(20)比兩個都早,請求就被硬砍。對照剛剛 tGPS=45:SIGKILL 在
第 45 秒,兩個都來得及。

repo 裡有現成的反例 overlay [`deploy/step2-badtgps`](../deploy/step2-badtgps/):reuse step2、只把
`terminationGracePeriodSeconds` 覆蓋成 **20**。切過去,跑**同一道 oha 指令**(不再貼拆解):

```sh
kubectl apply -k deploy/step2-badtgps
kubectl rollout status deploy/hydra
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=10"'
# 壓測跑著時,另開終端機觸發:kubectl rollout restart deploy/hydra
```

**實測(tGPS=20)**:

```
Status code distribution:
  [200] 25 responses

Error distribution:
  [5] aborted due to deadline
  [5] connection closed before message completed
```

跟正解一比,只多出 **`[5] connection closed before message completed`**——這 5 筆就是白等的鐵證。rollout 時
舊 pod 進 `Terminating`,preStop 先吃 15 秒,SIGTERM 後 graceful 只剩 **5 秒**(20 − 15),但 `/slow=10` 要
10 秒才收得完 → 第 20 秒 tGPS 撞頂,SIGKILL 把 `-c 5` 那 5 條還在跑的連線全部硬砍。(`[5] aborted due to
deadline` 同前,是 oha 60 秒邊界的雜訊,不是被砍。)

**開了 graceful、app 也真的在等,卻因為 tGPS 太短、等了 5 秒就被 SIGKILL 打斷——白等一場。** pod 這邊也
對得起來:它以「grace period 用完被 SIGKILL」的異常狀態收場(`kubectl describe pod` 會看到 exit code
**137** = 128 + 9)。

### SIGTERM 那刻,pod 手上為什麼有東西在等?——keep-alive

反例成立有個隱藏前提:SIGTERM 一到那刻,那顆要走的 pod 手上**真的有還在處理的請求**可被砍。有,才有「白等」
可看;沒有,tGPS 再短也砍不到人。而這由 **keep-alive** 決定。

oha 預設開 keep-alive——一條 TCP 連線打完一個請求不關,留著接著打下一個(省掉每次重建 TCP、甚至 TLS
握手);加 `--disable-keepalive` 才會每個請求開一條新連線、打完就關。差別搬到關機的場景來看:

- **開**:pod 進 `Terminating` 後,endpoint 被拿掉只影響新連線的分流,既有的 TCP 連線不會斷(那是 client 直連 pod 的 socket);而 pod 在 preStop 期間還沒收到 SIGTERM,照常接請求。於是 oha 沿用同一條連線,把 `/slow=10` 一直灌進這顆還在 preStop 的 pod——灌到 SIGTERM 一到那刻,黏在它身上的每條連線都還卡著一個還在處理的請求。
- **關**:每個請求開新連線、走 Service 分流,不會再進 endpoint 已被拿掉的那顆 pod;它手上那批請求也早在 15 秒 preStop 裡跑完 → SIGTERM 時一個還在處理的請求都沒有,tGPS 再短也沒東西可砍。

把反例區三次跑法擺一起(全部 `/slow=10`、`-c 5`、跑滿 60 秒、中途觸發一次 rollout):

| tGPS | keep-alive | `connection closed` | 誰在砍 |
|------|-----------|:-------------------:|--------|
| 20 | 開(預設) | **5** | tGPS(只剩 5 秒 < 10) |
| 20 | 關(`--disable-keepalive`) | **0** | 沒東西可砍 |
| 45 | 開(預設) | **0** | 都收得完 |

中間那列的驗法:跟上面 tGPS=20 那組完全一樣,只多加一個 `--disable-keepalive`(此時本來就在 badtgps 上):

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 --disable-keepalive "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=10"'
# 壓測跑著時,另開終端機觸發:kubectl rollout restart deploy/hydra
# 測完切回正解:            kubectl apply -k deploy/step2
```

> 實測:connection closed = 0,200 回了 25 個,另外 5 個 aborted due to deadline(oha 60 秒邊界,不是被砍)。
> 同一組太短的 tGPS=20,只把 keep-alive 關掉,被砍就從 5 → 0。keep-alive 只是把問題照出來的探照燈——它讓
> SIGTERM 那刻 pod 手上真的有還在處理的請求在等;真兇是 tGPS 沒容納那個等。

## 等的極限:SSE 等不完,還是被剪

tGPS 搭好、短請求等得完,接著看「等」的**極限**。`/slow=10` 等得完,是因為它十秒後自己會結束;還記得
Go 官方那**第四點**嗎——有一種連線永遠不會回到閒置,`Shutdown` 只能一直等到逾時。SSE 就是這種。掛一條
SSE、觸發滾動更新:

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

「等」有兩個預算——app 的 shutdown 上限、K8s 的 tGPS——對**永不結束**的 SSE,兩個都不夠,再大也不夠。
所以 step3 不再「等」,改成 app 關機時**主動**跟 SSE 道別、收線 → [step3](step3.md)。
