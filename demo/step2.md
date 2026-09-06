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

相對 step1,改了三處,而且要**互相搭配**——`GRACEFUL` 開優雅關機、`HYDRA_SHUTDOWN_TIMEOUT_SECONDS`
是它的等待上限、`terminationGracePeriodSeconds` 要蓋得住(≥ preStop + shutdown 上限):

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

## 反例:tGPS 太短,graceful 會被 SIGKILL 廢掉

先示範「搭配沒做好」會怎樣。時間軸是這樣——tGPS 到點,不管 graceful 收完沒,一律 SIGKILL:

```
Terminating ──preStop 15s──► SIGTERM ──app graceful(最多 15s)──► 乾淨結束
     │                                                              
     └───────────────── tGPS 到點就 SIGKILL(硬砍)───────────────►
```

repo 裡有現成的反例 overlay [`deploy/step2-badtgps`](../deploy/step2-badtgps/):它 reuse step2、只把
`terminationGracePeriodSeconds` 覆蓋成太短的 **20**(< preStop 15 + shutdown 15)。切過去:

```sh
kubectl apply -k deploy/step2-badtgps
kubectl rollout status deploy/hydra
```

用 oha 打 **`/slow?seconds=10`**——刻意選 10 秒(**< shutdown 上限 15**),讓 tGPS 成為唯一變因:
shutdown 上限本身收得完 10 秒的請求,收不完只會是因為 tGPS 太短、被 SIGKILL 搶先。壓測跑著時觸發滾動更新:

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=10"'
```

```sh
kubectl rollout restart deploy/hydra
```

**實測(tGPS=20)**——35 次請求,結果分三種:

```
Status code distribution:
  [200] 25 responses

Error distribution:
  [5] aborted due to deadline
  [5] connection closed before message completed
```

三種各對應什麼,分開看(**混在一起就會誤判**):

- **`[5] connection closed before message completed`** —— 這 5 條就是 tGPS 太短的鐵證。rollout 時舊 pod 進
  `Terminating`,preStop 先吃掉 15 秒,SIGTERM 後 graceful 只剩 **5 秒**(20 − 15),但 `/slow=10` 要 10 秒才
  收得完 → 5 秒一到、tGPS 撞頂,SIGKILL 把還在途的連線全部硬砍。`-c 5` 的 5 條連線(keep-alive 全釘在這顆
  要走的 pod 上)一起被砍,剛好 5 條。**開了 graceful,卻因為 tGPS 太短而形同虛設。**
- **`[5] aborted due to deadline`** —— 這 5 條**不是 pod 的鍋**,是 oha 自己 `-z 60s` 到點:測試視窗收尾時
  正好有 5 條 `/slow=10` 還在飛(每條 10 秒,撞 60 秒邊界必卡一批),oha 主動中止。與關機無關,別跟上面
  那 5 條混為一談。
- **`[200] 25`** —— 沒撞上那顆 pod 關機瞬間的請求,照常 10 秒收完。

pod 這邊也對得起來:它以「grace period 到點被 SIGKILL」的非正常狀態收場(`kubectl describe pod` 會看到
exit code **137** = 128 + 9,`kubectl get events` 有 grace period 相關事件)。

**對照組(tGPS=45,正解)**:改回 step2、同一條 `/slow=10` 再跑一次。

```sh
kubectl apply -k deploy/step2      # tGPS 回到 45
```

實測——Success rate **100%**:

```
Status code distribution:
  [200] 25 responses

Error distribution:
  [5] aborted due to deadline
```

`connection closed` **歸零**。唯一動的變因是 tGPS(20 → 45),被砍的條數就從 5 掉到 0——graceful 這下有滿
15 秒預算,`/slow=10` 全收得完。`aborted due to deadline` 一樣是 5:它跟關機無關,純粹是 oha `-z 60s` 到點時
卡住的那批,兩組都相同。

### 那 keep-alive 呢?不是會一直把流量灌進要走的 pod?

會,而且**兩組都一樣灌**——這點很容易誤會,講清楚:preStop 那 15 秒 pod 還活著,keep-alive 連線一直釘在
它身上、oha 照灌 `/slow=10`。差別不在「有沒有灌」,而在**灌進來的請求收不收得完**:

- **tGPS=20**:SIGTERM 後 graceful 只剩 5 秒 → 在途的 `/slow=10`(要 10 秒)撐不到,tGPS 一到就 SIGKILL 硬砍 → 5 條 `connection closed`。
- **tGPS=45**:SIGTERM 後有滿 15 秒 → 同一批在途 `/slow=10` 全收得完。`Shutdown` 會等它們跑完,同時對 SIGTERM 之後的新請求回 `Connection: close`(新流量改打新 pod)→ 0 條被砍。

換句話說,`seconds=10 < shutdown 上限 15` 這個刻意的選法,讓 keep-alive 灌進來的流量**一定收得完**,
keep-alive 就干擾不了實驗——**tGPS 才是唯一變因**。這也正是這裡用 10 秒、不用 20 秒的原因:20 秒 > shutdown
上限,keep-alive 灌進來的會撐不完,「tGPS 太短」和「請求比 shutdown 上限還長」兩個效應就混在一起、分不清誰
砍了誰。

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
