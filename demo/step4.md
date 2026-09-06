# Step 4:狀態外部化(Valkey)+ 就緒檢查

step3 的問題:連線善終了,但進度存在 pod 的記憶體,pod 一換就歸零。

**這步加的**(相對 step3):

- `HYDRA_STATE_BACKEND=valkey`:進度改存到外部的 Valkey,所有 hydra pod 共用同一份。
- 加回 `readinessProbe`(`/readyz`):app 啟動要連上 Valkey、載入狀態,這段期間 readiness
  擋住流量,就緒才收。
- 多部署一顆 Valkey(單副本、無持久化;`apply` 時一起建起來)。

## 切到這步

```sh
kubectl --context kind-zdt apply -k deploy/step4          # 同時建立 Valkey
kubectl --context kind-zdt -n zdt-tour rollout status deploy/hydra
kubectl --context kind-zdt -n zdt-tour get pods -o wide   # 應該多一顆 valkey-*
```

## 觀察

跟 [step3](step3.md) 完全一樣的狀態測試:建 session → 記住走到第幾站 → 滾動更新 →
用同一個 cookie 重連,看 `hello` 的 `seq`:

```sh
# 1) 建 session、收事件(記住 seq 與 pod),Ctrl-C
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 2) 滾動更新
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra

# 3) 同一個 cookie 重連
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

## 預期現象

- 重連後 `hello` 的 `pod` 換成新的,但 `seq` **接得上**——進度存在 Valkey,新 pod 讀得到同一份。
- 加上 step1–3 的 preStop / graceful / drain,連線也全程善終。**連線與狀態都零停機。**
- readiness 的作用:滾動時新 pod 要 `/readyz` 通過(連上 Valkey)才會被加進 Service,
  不會出現「還沒準備好就收流量」的空窗。

## 收尾

到這裡,rolling update 對使用者完全無感:連線被好好收掉、client/瀏覽器乾淨重連、
導覽進度接著播。這就是這個 demo 想演的「零停機」。

清理(一併移除 Valkey):

```sh
kubectl --context kind-zdt delete -k deploy/step4
```
