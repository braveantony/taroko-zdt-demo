# Step 3:連線排空(drain)

step2 的問題:SSE 長連線在關機時等不到自己結束,撞 shutdown 上限被剪斷。

**這步加的**(相對 step2 的唯一差別):`HYDRA_SSE_DRAIN=on`。app 關機時會主動對每條 SSE
廣播一個 `bye` 事件、把連線收乾淨,連線自己就結束了,`Shutdown` 幾秒內乾淨完成、exit 0。

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

預期:SSE 流會先收到一個 `bye` 事件**才**關閉(不是被硬剪),client/瀏覽器可以據此乾淨重連。
連線層到這裡做到零停機了。

## 觀察二:進度還是掉了(這步的新問題)

排空解決了連線,但進度仍存在 pod 的記憶體裡。開一條帶 cookie 的 session、記住它走到第幾站,
滾動更新後用**同一個 cookie** 重連,看進度還在不在。cookie 存在不會被 rollout 的 client pod
的 `/tmp/jar`,跨兩次 exec 都在。

```sh
# 1) 建 session 並開始收事件;讓它跑幾站(留意 station 的 seq 與 pod),然後 Ctrl-C
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 2) 觸發滾動更新
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra

# 3) 用同一個 cookie 重連,看 hello 事件的 seq
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

預期:重連後 `hello` 的 `pod` 是一顆新 pod(rollout 換掉了),而 `seq` **掉回 1**——
進度隨舊 pod 的記憶體一起消失了。

## 還沒解決

連線善終了,但狀態沒了。把進度搬到 pod 外面,換 pod 也不受影響 → [step4](step4.md)。
