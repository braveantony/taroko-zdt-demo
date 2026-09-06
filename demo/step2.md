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

tGPS 這段預算裡,依序經過三個重點階段(時間軸照比例):

```text
        +=======Termination Grace Period = 45s=======+
        |              |                             |
        preStop Hook   SIGTERM                       SIGKILL
        |              |                             |
        v              v                             v
 -------+--------------+-----------------------------+-------> 時間
        0s             15s                           45s
```

`preStop`=關機緩衝(0→15s)、`SIGTERM`=送關機訊號(15s,graceful 從這開始跑)、`SIGKILL`=強制終止(45s,tGPS 到點)。

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

指令拆解——`oha` 是一支 HTTP 壓測工具(類似 `hey` / `wrk`,會即時顯示成功率與延遲分佈):

- **`-z 60s`**:持續打 **60 秒**(`-z` 是時間模式;`-n` 才是固定次數)。
- **`-c 5`**:**5 條連線同時**打(並行度)。
- **`/slow?seconds=10`**:hydra 的測試端點,收到請求後**故意睡 10 秒**才回覆,專門用來製造「關機當下還在途的長請求」。
- 這裡**沒加 `--disable-keepalive`** → 用的是 keep-alive(連線重複用),這對本測試很關鍵,下面 keep-alive 那段細講。

打壓測的同時,另開一個終端觸發滾動更新:

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

### keep-alive 為什麼要開?開/關的實測差異

**先搞清楚一件事:關機時,卡在 SIGTERM 那一刻的請求,會被誰砍?** 有兩個死線,誰先到算誰(都從 pod
進 `Terminating` 起算):

- **shutdown 上限**:SIGTERM 之後,程式最多再等 15 秒把手上請求收完 → 第 15(preStop)+ 15 = **第 30 秒**,收不完程式自己剪線。
- **tGPS 的 SIGKILL**:第 tGPS 秒(20 或 45)一到,強制砍,不管收完沒。

`/slow=10` 只要 10 秒,**比 shutdown 上限 15 短 → shutdown 那關一定過,能砍它的就只剩 tGPS**:tGPS=20 時
preStop 15 之後只剩 5 秒 < 10 → SIGKILL 砍(5 條);tGPS=45 時還有 15 秒 > 10 → 收得完(0 條)。這就是上面
兩組數據的由來。

**keep-alive 是什麼?** HTTP/1.1 預設開:**一條 TCP 連線打完一個請求不關掉,接著重複用來打下一個**,省掉
每次重建 TCP(甚至 TLS)握手的成本。oha 沒加 `--disable-keepalive` 就是開著。開跟關,oha 的行為差在:

- **開(預設)**:5 條連線各自建好後**一直用同一條**。連線一旦連上某顆 pod 就**黏在那顆 pod**——TCP 連線不會每個請求重新經過 Service 分流。
- **關(`--disable-keepalive`)**:**每個請求開一條新連線、打完就關**。每條新連線都重新過一次 Service 分流,只會落到當下 Ready 的 pod。

差別搬到關機情境,正好是「能不能演出 tGPS 問題」的關鍵:

- **開**:pod 進 preStop 時 endpoint 雖已摘掉,但**既有的 TCP 連線還連著**,oha 沿用它繼續把 `/slow=10` 灌進這顆要走的 pod → **SIGTERM 那一刻 pod 上一定卡著在途請求 → 逼出 tGPS 邊界**。
- **關**:pod 一進 `Terminating`、endpoint 被摘,**新連線就不再進這顆 pod**;它身上先前的請求早在 preStop 15 秒內就跑完 → SIGTERM 時沒有卡著的請求 → **就算 tGPS=20 也砍不到,tGPS 問題被藏住**。

實測對照(全部 `/slow=10`、`-c 5`、跑滿 60 秒、中途觸發一次 rollout):

| tGPS | keep-alive | `connection closed` | 誰在砍 |
|------|-----------|:-------------------:|--------|
| 20 | 開(oha 預設) | **5** | tGPS(只剩 5 秒 < 10) |
| 20 | 關(`--disable-keepalive`) | **0**(實測) | 沒東西可砍 |
| 45 | 開(oha 預設) | **0** | 都收得完 |

驗證中間那列——跟上面 tGPS=20 一模一樣的跑法,**只多 `--disable-keepalive` 一個 flag**:

```sh
kubectl apply -k deploy/step2-badtgps      # 先回到 tGPS=20
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 60s -c 5 --disable-keepalive "http://hydra.zdt-tour.svc.cluster.local/slow?seconds=10"'
# 壓測跑著時,另開終端觸發:kubectl rollout restart deploy/hydra
# 測完切回正解:            kubectl apply -k deploy/step2
```

> 實測結果:`connection closed` = **0**、`[200] 25`、`[5] aborted due to deadline`(oha 60s 邊界)。同樣壞的
> tGPS=20,只把 keep-alive 關掉,被砍條數就從 **5 → 0**——證明 keep-alive 是「把請求塞到關機邊界、逼出 tGPS
> 問題」的探照燈;tGPS 太短才是真兇。

**所以兩個條件缺一不可**:keep-alive 要**開**(才逼得出 tGPS 極限)、`/slow` 秒數要**壓在 shutdown 上限之下**
(10 < 15,讓 shutdown 那關永遠過、tGPS 成為唯一變因)。先前打 `/slow=20`(> 15)壞就壞在後者:shutdown 上限
先砍,連 tGPS=45 都斷,根本分不清是誰砍的。

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
