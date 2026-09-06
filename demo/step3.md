# Step 3:連線排空(drain)

step2 的問題:SSE 長連線在關機時等不到自己結束,撞 shutdown 上限被剪斷。

**這步加的**(相對 step2 的唯一差別):`HYDRA_SSE_DRAIN=on`。app 關機時主動對每條 SSE 送出
`bye` 事件,然後由 server 端關閉連線。手上沒有掛著的長連線,`Shutdown` 幾秒內就能完成、exit 0。

**背後的 code**:關機序列在 `Shutdown` 之前先排空 SSE,一條鏈路走完:

```go
// server.go:SSE_DRAIN=on 才廣播收線(否則就跟 step2 一樣被永不結束的 SSE 卡到逾時)
if s.cfg.SSEDrain {
    s.tour.Hub().Drain()
}

// hub.go:關閉每條連線的 bye channel
func (h *Hub) Drain() {
    h.draining = true
    for _, ch := range h.conns {
        close(ch)
    }
}

// handler.go:SSE handler 看到 bye channel 關閉 → 送 bye 事件、收線返回
case <-bye:
    t.writeEvent(w, fl, "", "bye", map[string]string{"reason": "pod shutting down"})
    return
```

## SSE_DRAIN on / off 差在哪

同一段關機序列,`SSE_DRAIN` 只決定「要不要走 `Hub().Drain()` 那一步」——差別全在這裡:

| | `off`(step2 及之前) | `on`(step3) |
|---|---|---|
| 關機時呼叫 `Drain()` | 不呼叫 | 呼叫 |
| SSE handler 迴圈 | 只等 `timer.C`,永遠不回到閒置 | `bye` channel 一被 close → 送 `bye`、`return` |
| `httpSrv.Shutdown` | 被永不結束的 SSE 卡到 **shutdown 上限(15s)逾時** → 才 `Close()` 強制剪線 | 連線全回到閒置 → **幾秒內乾淨收完** |
| client 看到 | stream 中途被剪斷、**沒有 bye** | 先收到 **`bye`**、連線可預期地結束 |
| pod 收場 | 拖滿 ~30s(preStop 15 + shutdown 15)、exit **非 0** | 幾秒 **exit 0** |

`off` 就是 step2 對 SSE 的處理:graceful 有等,但等不完永不結束的 SSE,最後撞上限被剪。`on` 則是 app 主動
說再見、收線,不用傻等。

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step3
```

相對 step2,只改一個 env——`SSE_DRAIN`:

```yaml
      containers:
      - name: hydra
        env:
        - {name: HYDRA_GRACEFUL, value: "on"}
        - {name: HYDRA_STATE_BACKEND, value: memory}    # 進度仍在記憶體(這步還沒解)
        - {name: HYDRA_SSE_DRAIN, value: "on"}          # ← step3:off → on(關機送 bye、排空 SSE)
        - {name: HYDRA_TOUR_INTERVAL_SECONDS, value: "10"}
        # tGPS 45、preStop 15、nodeSelector / affinity / strategy / liveness 同 step2
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step3
kubectl rollout status deploy/hydra
```

## 觀察一:連線善終

照 [step2](step2.md) 的方式掛一條 SSE,再觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

預期:SSE 流會先收到一個 `bye` 事件**才**關閉——是 server 主動、可預期地收線,不是硬斷。

- 用 `curl` 看:收到 `bye` 後連線正常結束、指令跟著結束(curl 不會自動重連)。
- 用瀏覽器看(想看導覽頁的話,另開終端機 `kubectl port-forward svc/hydra 8080:80`,開 <http://localhost:8080/tour>):`EventSource` 收到連線結束會自動重連,燈號由「導覽員換班中」轉回「導覽中」。

> **重連是 client 做的,不是 server。** server 只做兩件事——送 `bye`、然後關掉連線;瀏覽器的 `EventSource`
> 偵測到連線斷掉就**自己**重連,這是內建行為,跟有沒有收到 `bye` 無關(所以 `off` 硬斷時瀏覽器一樣會重連)。
>
> - `bye` 是 **app 層的禮貌訊息**:給前端 JS 更新畫面用(切成「導覽員換班中」),不是叫瀏覽器重連的指令。
> - 連線一開始 server 送過一行 `retry: 1000`(SSE 契約),那是**建議 client 的重連間隔**(1 秒);動作還是
>   client 做,不送就用瀏覽器預設(多數約 3 秒)。
> - 所以 `drain=on` 改善的不是「幫 client 重連」,而是讓 **pod 早早、乾淨地放手(exit 0)**,順帶給前端一個
>   `bye` 好更新 UI——client 端的重連,兩種情況本來都會發生。

重點不是「TCP 連線永不中斷」,而是中斷變得可預期。

## 觀察二:進度還是掉了(接下來要解的問題)

排空解決了連線,但進度仍在 pod 的記憶體裡。做法:開一條帶 cookie 的 session、記住走到第幾站,
滾動更新後用**同一個 cookie** 重連,看進度還在不在。cookie 寫在 client pod 的 `/tmp/jar`;
client 不在這次 rollout 的範圍,所以兩次 exec 讀到的是同一個檔。

(其實記憶體狀態本來就跟「建立它的那顆 pod」綁死:三副本又沒做 session affinity,就算不 rollout、
單純重連也可能被分到別顆 pod 而讀不到進度。rollout 只是讓「舊 pod 一定不在了」必然發生、好觀察。)

```sh
# 1) 建 session 並開始收事件;讓它跑幾站(留意 station 的 seq 與 pod),然後 Ctrl-C
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 2) 觸發滾動更新,並等它換完(確保等下重連的是新 pod,而不是還沒被換掉的舊 pod)
kubectl rollout restart deploy/hydra
kubectl rollout status deploy/hydra

# 3) 用同一個 cookie 重連,看 hello 事件的 seq
kubectl exec -it deploy/client -- sh -c \
  'curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

預期:重連後 `hello` 的 `pod` 是一顆新 pod(rollout 換掉了),而 `seq` **掉回 1**——
進度隨舊 pod 的記憶體一起消失了。

## 還沒解決

連線善終了,但狀態沒了。把進度搬到 pod 外面,換 pod 也不受影響 → [step4](step4.md)。
