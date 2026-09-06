# Step 1:preStop 緩衝

step0 的問題:pod 一被殺,Service 的 endpoint 還沒更新完,新連線就打到已經死掉的 pod ——
於是一堆 `Connection refused`。

**這步加的**(相對 step0 的唯一差別):一段 15 秒的 `preStop` 緩衝。

pod 進入終止流程時,兩件事同時開跑:控制面把它從 Service 的 endpoint 摘掉(標成 not-ready),
kubelet 則執行 `preStop`,讓 process 先多活 15 秒再收到 SIGTERM。注意這是並行,不是「先摘掉、
再等待」的保證順序——所以才需要這 15 秒:讓「這顆 pod 已摘掉」有時間傳到各節點的 datapath
(kube-proxy,這裡是 Cilium),新連線就逐漸不會再打到它。

(app 本身仍不處理關機,`HYDRA_GRACEFUL=off`。distroless 沒有 shell,所以 preStop 用 K8s 原生的
`sleep` action,不是 `exec` 跑 `sleep`。)

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step1
```

相對 step0,hydra Deployment 只多了 `lifecycle.preStop`:

```yaml
      containers:
      - name: hydra
        lifecycle:                 # ← step1 新增:摘除 endpoint 後多撐 15 秒
          preStop:
            sleep: {seconds: 15}
        # env(graceful=off / memory / drain=off)、livenessProbe、tGPS 30、
        # nodeSelector / affinity / strategy 都與 step0 相同
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step1
kubectl rollout status deploy/hydra
```

## 觀察

沿用 [step0](step0.md) 的雙終端——右邊 `watch` 盯 pods、左邊在 client 內跑 oha:

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 120s -c 20 --disable-keepalive "$TARGET"'
```

壓測跑著時,另開終端觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

## 預期現象

- oha 的 **Error distribution** 裡,**關機端**的 `Connection refused` 大幅減少——
  舊 pod 被標成 not-ready 後,新連線逐漸不再打到它。
- 但不會完全歸零,還有兩個缺口:
  - **關機端**:15 秒一到、SIGTERM 送達,不處理關機的 app 立刻結束,
    **那一刻還開著的連線仍被硬斷**(多是 `Connection reset` / `closed before message completed`)。
  - **啟動端**:step0–3 都還沒有 readinessProbe,新 pod 一進入 Running 就可能被加進 Service,
    但這時 hydra 也許還沒 `listen` 完 → 又冒出一批 `Connection refused`。這個啟動缺口要到
    step4 加上 readiness 才補起來——所以別把所有 `Connection refused` 都當成舊 endpoint 的傳播延遲。

## 還沒解決

在途連線在關機瞬間還是斷。要讓 app 收到 SIGTERM 後把手上的請求好好收完,
得靠程式層的優雅關機 → [step2](step2.md)。
