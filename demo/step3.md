# Step 3:連線排空(drain)

step2 的問題:SSE 長連線在關機時等不到自己結束,撞 shutdown 上限被剪斷。

**這步加的**(相對 step2 的唯一差別):`HYDRA_SSE_DRAIN=on`。app 關機時主動對每條 SSE 送出
`bye` 事件,然後由 server 端關閉連線。手上沒有掛著的長連線,`Shutdown` 幾秒內就能完成、exit 0。

## 切到這步

```sh
kubectl --context kind-zdt apply -k deploy/step3
kubectl --context kind-zdt -n zdt-tour rollout status deploy/hydra
```

## 觀察一:連線善終

照 [step2](step2.md) 的方式掛一條 SSE,再觸發滾動更新:

```sh
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra
```

預期:SSE 流會先收到一個 `bye` 事件**才**關閉——是 server 主動、可預期地收線,不是硬斷。

- 用 `curl` 看:收到 `bye` 後連線正常結束、指令跟著退出(curl 不會自動重連)。
- 用瀏覽器看(想看導覽頁的話,另開終端 `kubectl --context kind-zdt -n zdt-tour port-forward svc/hydra 8080:80`,開 <http://localhost:8080/tour>):`EventSource` 收到連線結束會自動重連,燈號由「導覽員換班中」轉回「導覽中」。

重點不是「TCP 連線永不中斷」,而是中斷變得可預期。

## 觀察二:進度還是掉了(接下來要解的問題)

排空解決了連線,但進度仍在 pod 的記憶體裡。做法:開一條帶 cookie 的 session、記住走到第幾站,
滾動更新後用**同一個 cookie** 重連,看進度還在不在。cookie 寫在 client pod 的 `/tmp/jar`;
client 不在這次 rollout 的範圍,所以兩次 exec 讀到的是同一個檔。

(其實記憶體狀態本來就跟「建立它的那顆 pod」綁死:三副本又沒做 session affinity,就算不 rollout、
單純重連也可能被分到別顆 pod 而讀不到進度。rollout 只是讓「舊 pod 一定不在了」必然發生、好觀察。)

```sh
# 1) 建 session 並開始收事件;讓它跑幾站(留意 station 的 seq 與 pod),然後 Ctrl-C
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 2) 觸發滾動更新,並等它換完(確保等下重連的是新 pod,而不是還沒被換掉的舊 pod)
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra
kubectl --context kind-zdt -n zdt-tour rollout status deploy/hydra

# 3) 用同一個 cookie 重連,看 hello 事件的 seq
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

預期:重連後 `hello` 的 `pod` 是一顆新 pod(rollout 換掉了),而 `seq` **掉回 1**——
進度隨舊 pod 的記憶體一起消失了。

## 還沒解決

連線善終了,但狀態沒了。把進度搬到 pod 外面,換 pod 也不受影響 → [step4](step4.md)。
