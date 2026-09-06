# Step 1:preStop 緩衝

step0 的問題:pod 一被殺,Service 的 endpoint 還沒更新完,新連線就打到已經死掉的 pod ——
於是一堆 `Connection refused`。

**這步加的**(相對 step0 的唯一差別):一段 `preStop` 緩衝。pod 進入 Terminating 時,
K8s 會先把它從 endpoint 摘除,再等 `preStop` 的 15 秒才送 SIGTERM。這 15 秒裡舊 pod 還活著,
新流量已經改道到其他 pod。

（app 本身仍然不處理關機,`HYDRA_GRACEFUL=off`。distroless 沒有 shell,所以 preStop 用
K8s 原生的 `sleep` action,不是 `exec` 跑 `sleep`。）

## 切到這步

```sh
kubectl --context kind-zdt apply -k deploy/step1
kubectl --context kind-zdt -n zdt-tour rollout status deploy/hydra
```

## 觀察

沿用 [step0](step0.md) 的雙終端——右邊 `watch` 盯 pods、左邊在 client 內跑 oha:

```sh
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- \
  sh -c 'oha -z 120s -c 20 --disable-keepalive "$TARGET"'
```

壓測跑著時,另開終端觸發滾動更新:

```sh
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra
```

## 預期現象

- oha 的 **Error distribution** 明顯變少——`Connection refused` 幾乎消失,
  因為新連線不會再打到剛被殺的 pod。
- 但還沒歸零:15 秒一到、SIGTERM 送達,不處理關機的 app 立刻結束,
  **那一刻還開著的連線仍被硬斷**(剩下的多是 `Connection reset` / `closed before message completed`)。

## 還沒解決

在途連線在關機瞬間還是斷。要讓 app 收到 SIGTERM 後把手上的請求好好收完,
得靠程式層的優雅關機 → [step2](step2.md)。
