# Step 0:什麼都不處理

**故事**:hydra 收到 `SIGTERM` 立即死、SSE/HTTP 連線硬斷、進度存在記憶體隨 pod 消失而歸零。
這就是 rolling update 下 stateful 工作負載「完全不處理」的樣子,作為 step1–4 的對照基準。

**step0 相對 base 陽春疊了什麼**(見 `deploy/step0/kustomization.yaml`):

| 疊上去的 | 值 | 用途 |
|---|---|---|
| `nodeSelector` | `tier=hydra` | hydra 固定在 w1–3,與 client(w4)分開 |
| `podAntiAffinity` | preferred / hostname | 3 個 pod 分散在不同 node |
| `strategy` | `maxUnavailable=0` / `maxSurge=1` | 先起新、後刪舊,容量不下降 |
| `terminationGracePeriodSeconds` | `30` | SIGTERM → SIGKILL 的預算 |
| `livenessProbe` | `GET /healthz` | 掛了會重啟 |
| env | `HYDRA_GRACEFUL=off`、`STATE_BACKEND=memory`、`SSE_DRAIN=off` | step0 參數 |

**沒有** preStop、**沒有** readinessProbe、**沒有** graceful shutdown、**沒有** 排水 → 換手瞬間連線被硬斷。

---

## 前置

- kind + Cilium 叢集就緒(見 [`infra/`](../infra/),`./infra/up.sh`),context = `kind-zdt`。
- 三個 GHCR image 皆已設為 **Public**:`hydra`、`loadtest`、`taroko-tools`。

方便起見,先設個別名(帶上 context 與 namespace):

```sh
alias k='kubectl --context kind-zdt -n zdt-tour'
```

## 1. 部署 step0

```sh
kubectl --context kind-zdt apply -k deploy/step0
k rollout status deploy/hydra
k rollout status deploy/client
```

## 2. 確認 pod 分散在不同 node

```sh
k get pods -o wide
```

預期:3 個 `hydra-*` 各據一台 hydra tier 的 worker(`zdt-worker` / `zdt-worker2` / `zdt-worker3`),
`client-*` 落在 client tier 的 `zdt-worker4`。

## 3. 開兩個終端觀察(所見即所跑)

**右終端 —— 盯 pod 變化:**

```sh
watch -n1 'kubectl --context kind-zdt -n zdt-tour get pods -o wide'
```

**左終端 —— 從 client pod 內用 oha 持續壓測 hydra Service:**

```sh
CLIENT=$(k get pod -l app=client -o jsonpath='{.items[0].metadata.name}')
k exec -it "$CLIENT" -- sh -c 'oha -z 120s -c 20 --disable-keepalive "$TARGET"'
```

`$TARGET` 已烘進 image(`http://hydra.zdt-tour.svc.cluster.local/version`)。
oha 會即時顯示 QPS、狀態碼分佈與 latency。並發數 `-c`、時長 `-z` 可自行調整;
`--disable-keepalive` 讓每次請求都新建連線,最能凸顯換手瞬間打到剛被殺掉的 pod。

## 4. 觸發 rolling update

壓測跑著的同時,另開一個終端:

```sh
k rollout restart deploy/hydra
```

## 5. 預期現象(不處理的代價)

- **右終端**:舊 `hydra-*` 幾乎瞬間 `Terminating` → 消失,新 pod `ContainerCreating` → `Running`;
  因為 `maxUnavailable=0`,總是先補新的再殺舊的。
- **左終端**:oha 統計會在每次換手瞬間冒出 **非 2xx / connection error**(數量不多但看得到)——
  step0 收到 SIGTERM 立即結束,沒有 preStop 緩衝、沒有排水,in-flight 連線被硬斷。
- 這些 error 就是 step1–4 要一步步消滅的東西。

### (選用)直接看 SSE 連線被硬斷

hydra 的導覽事件流是長連線,最能體現「連線存亡」。另開終端掛一條 SSE:

```sh
k exec -it "$CLIENT" -- sh -c 'curl -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

跑 `rollout restart` 時,若這條流所在的 pod 被換掉,streaming 會**當場斷掉**(step0 不排水、不善終)。
到 step3/step4 加上 drain + 狀態外部化後,同樣操作下連線會被好好收尾、進度也不再歸零。

## 6. 清理

```sh
kubectl --context kind-zdt delete -k deploy/step0
```
