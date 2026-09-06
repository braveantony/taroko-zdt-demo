# Step 4:狀態外部化(Valkey)+ 就緒檢查

step3 的問題:連線善終了,但進度存在 pod 的記憶體,pod 一換就歸零。

**這步加的**(相對 step3):

- `HYDRA_STATE_BACKEND=valkey`:進度改存到外部的 Valkey(Redis 相容),所有 hydra pod 共用同一份。
- 加上 `readinessProbe`(`/readyz`):app 啟動時先確認連得上 Valkey,連不上就直接結束(fail-fast);
  確認完成前 `/readyz` 不過,流量進不來。
  (註:`/readyz` 只反映「啟動完成、還沒開始關機」,不是持續探測 Valkey——啟動之後 Valkey 若掛掉,
  readiness 不會翻紅。)
- 多部署一顆 Valkey(單副本、無持久化;`apply` 時一起建起來)。

**背後的 code**([`internal/server/server.go`](../images/hydra/internal/server/server.go)):進度改由
Valkey 版的 StateStore 讀寫(所有 pod 共用同一個 Valkey),啟動時先 `Ping` 一次做 fail-fast;`/readyz`
只有在啟動完成後才回 200:

```go
// Run():valkey 後端啟動時先確認連得上,連不上就直接結束(不無聲降級為記憶體)
if s.cfg.StateBackend == "valkey" {
    if err := s.store.Ping(pingCtx); err != nil {
        return fmt.Errorf("valkey 狀態庫不可用: %w", err)
    }
}
// …Serve 起來(且上面 Ping 過)之後,才把就緒翻成 ready
s.health.SetReady()
```

```go
// health.go:/readyz 依這個狀態機;starting / draining 都回 503,只有 ready 才 200
func (h *Health) IsReady() bool {
    return h.state.Load() == stateReady   // starting → ready → draining(單向)
}
```

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集)——這步的輸出除了 hydra,還會多出
一個 Valkey 的 Deployment 與 Service:

```sh
kubectl kustomize deploy/step4
```

hydra Deployment 相對 step3 改了兩處——`STATE_BACKEND` 換成 valkey、加上 `readinessProbe`:

```yaml
      containers:
      - name: hydra
        env:
        - {name: HYDRA_GRACEFUL, value: "on"}
        - {name: HYDRA_STATE_BACKEND, value: valkey}    # ← step4:memory → valkey(進度外部化)
        - {name: HYDRA_SSE_DRAIN, value: "on"}
        - {name: HYDRA_TOUR_INTERVAL_SECONDS, value: "10"}
        readinessProbe:            # ← step4 新增:就緒(連上 Valkey)才收流量
          httpGet: {path: /readyz, port: http}
          periodSeconds: 2
          failureThreshold: 1
        # tGPS 45、preStop 15、liveness、nodeSelector / affinity / strategy 同 step3
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step4          # 同時建立 Valkey
kubectl rollout status deploy/hydra
kubectl get pods -o wide   # 應該多一顆 valkey-*
```

## 觀察

跟 [step3](step3.md) 完全一樣的狀態測試:建 session → 記住走到第幾站 → 滾動更新 →
用同一個 cookie 重連,看 `hello` 的 `seq`:

```sh
# 1) 建 session、收事件(記住 seq 與 pod),Ctrl-C
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'

# 2) 滾動更新,並等它換完(確保等下重連的是新 pod)
kubectl rollout restart deploy/hydra
kubectl rollout status deploy/hydra

# 3) 同一個 cookie 重連
kubectl exec -it deploy/client -- sh -c \
  'curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

## 預期現象

- 重連後 `hello` 的 `pod` 換成新的,但 `seq` **接得上**——進度存在 Valkey,新 pod 讀得到同一份。
- 順手把 step0 的 oha 再跑一輪、觸發一次滾動更新:Error distribution 應該幾乎空了。
  step1 起一路提到的**啟動端** `Connection refused`,這回被 readiness 擋掉了——新 pod 要 `/readyz`
  通過(啟動完成、連上 Valkey)才會被加進 Service,補上「還沒 `listen` 完就收流量」的空窗。

## 收尾

到這裡,rolling update 對使用者完全無感:連線被好好收掉、client/瀏覽器乾淨重連、
導覽進度接著播。這就是這個 demo 想演的「零停機」。

清理(一併移除 Valkey):

```sh
kubectl delete -k deploy/step4
```
