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
| env | `HYDRA_GRACEFUL=off`、`HYDRA_STATE_BACKEND=memory`、`HYDRA_SSE_DRAIN=off` | step0 參數 |

**沒有** preStop、**沒有** readinessProbe、**沒有** graceful shutdown、**沒有** 排空 → 換手瞬間連線被硬斷。

## 這步的 Deployment 關鍵設定

apply 之前,先把這步會套用的 manifest 渲染出來看(只在本機把 yaml 組出來,不碰叢集):

```sh
kubectl kustomize deploy/step0
```

其中 hydra Deployment 的關鍵欄位如下(image / ports / resources / securityContext 等樣板已略,
完整逐字輸出見上面指令):

```yaml
spec:
  replicas: 3
  strategy:                                # 更新策略:先起新、後刪舊,容量不下降
    type: RollingUpdate
    rollingUpdate: {maxSurge: 1, maxUnavailable: 0}
  template:
    spec:
      nodeSelector: {tier: hydra}          # 只排到 hydra tier(w1–3)
      affinity:                            # 3 副本盡量分散到不同 node
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector: {matchLabels: {app: hydra}}
              topologyKey: kubernetes.io/hostname
      terminationGracePeriodSeconds: 30
      containers:
      - name: hydra
        livenessProbe:                     # 掛了會重啟
          httpGet: {path: /healthz, port: http}
        env:
        - {name: HYDRA_GRACEFUL, value: "off"}         # 收 SIGTERM 立即死(不優雅關機)
        - {name: HYDRA_STATE_BACKEND, value: memory}   # 進度存 pod 記憶體
        - {name: HYDRA_SSE_DRAIN, value: "off"}        # 關機不排空 SSE
        - {name: HYDRA_TOUR_INTERVAL_SECONDS, value: "10"}
        # ✗ 沒有 lifecycle.preStop   ✗ 沒有 readinessProbe
```

---

## 前置

- kind + Cilium 叢集就緒(見 [`infra/`](../infra/),`./infra/up.sh`)。
- 三個 GHCR image 皆已設為 **Public**:`hydra`、`loadtest`、`taroko-tools`。

先把 context 與 namespace 設好——這兩行寫進 `~/.kube/config`,對**所有終端**都有效
(也順便解決 shell 別名不跨終端的問題),之後每條指令就不必再帶 `--context` 和 `-n`:

```sh
kubectl config use-context kind-zdt                        # 設為目前 context
kubectl config set-context --current --namespace=zdt-tour  # 預設 namespace 設成 zdt-tour
```

下面的指令都用精簡寫法(`kubectl get pods …`)。做完 demo 想切回原本的 context:
`kubectl config use-context <你原本的>`。(若不想改動全域設定,也可以在每條指令自行補回
`--context kind-zdt -n zdt-tour`。)

## 1. 部署 step0

```sh
kubectl apply -k deploy/step0
kubectl rollout status deploy/hydra
kubectl rollout status deploy/client
```

## 2. 確認 pod 分散在不同 node

```sh
kubectl get pods -o wide
```

預期:3 個 `hydra-*` 各據一台 hydra tier 的 worker(`zdt-worker` / `zdt-worker2` / `zdt-worker3`),
`client-*` 落在 client tier 的 `zdt-worker4`。

## 3. 開兩個終端觀察(所見即所跑)

**右終端 —— 盯 pod 變化:**

```sh
watch -n1 'kubectl get pods -o wide'
```

**左終端 —— 從 client pod 內用 oha 持續壓測 hydra Service:**

```sh
kubectl exec -it deploy/client -- \
  sh -c 'oha -z 120s -c 20 --disable-keepalive "$TARGET"'
```

`$TARGET` 已烘進 image(`http://hydra.zdt-tour.svc.cluster.local/version`)。
oha 會即時顯示 QPS、狀態碼分佈與 latency。並發數 `-c`、時長 `-z` 可自行調整;
`--disable-keepalive` 讓每次請求都新建連線,最能凸顯換手瞬間打到剛被殺掉的 pod。

## 4. 觸發 rolling update

壓測跑著的同時,另開一個終端:

```sh
kubectl rollout restart deploy/hydra
```

## 5. 預期現象(不處理的代價)

- **右終端**:舊 `hydra-*` 幾乎瞬間 `Terminating` → 消失,新 pod `ContainerCreating` → `Running`;
  因為 `maxUnavailable=0`,總是先補新的再殺舊的。
- **左終端**:oha 的 **Error distribution** 會在每次換手瞬間冒出連線層錯誤——
  `Connection refused`、`Connection reset by peer`、`connection closed before message completed`。

  注意這些**不會**出現在右上角的 **Status code distribution**(那裡仍是清一色 `[200]`)。
  因為 step0 收到 SIGTERM 直接把 process 殺掉,請求的連線根本沒拿到 HTTP 回應就斷了——
  沒有回應,自然沒有狀態碼可歸類。這正是「硬斷」:失敗發生在**連線層**,而不是一個收得好好的 `503`。
- 這些連線錯誤就是 step1–4 要一步步消滅的東西:到 step2 的優雅關機,請求會被好好收完;
  到 step3 的排空,SSE 會先收到 `bye` 再乾淨重連。

### 實際輸出範例

一輪 120 秒、期間觸發幾次滾動更新,oha 收尾的統計:

```text
Summary:
  Success rate: 99.83%
  Total:        120.10 sec
  Requests/sec: 1341.8

Status code distribution:
  [200] 160883 responses

Error distribution:
  [254] Connection refused (os error 111)
  [10]  connection error
  [4]   aborted due to deadline
  [1]   Connection reset by peer (os error 104)
  [1]   connection closed before message completed
```

16 萬個請求、成功率 99.83%——而**成功的清一色是 `[200]`**,一個非 2xx 都沒有。代價全落在 **Error distribution**:連線層錯誤共 266 筆,絕大多數是 `Connection refused`(254 筆,新連線打到剛被殺、endpoint 還沒更新完的 pod),其餘是 `Connection reset` / `closed before message completed`(連線建好後 pod 中途死掉)。

(另外那 4 個 `aborted due to deadline` 不是滾動更新造成的,是 `-z 120s` 時間到、還在途的請求被中止。)

換句話說,step0 的失敗只會以「連線斷掉」的形式出現在 Error distribution,永遠不會變成一個收得好好的 HTTP 狀態碼。

### (選用)直接看 SSE 連線被硬斷

hydra 的導覽事件流是長連線,最能體現「連線存亡」。`/tour/events` 需要帶 `hydra_session` cookie
(否則回 400),所以先 `curl -c` 造訪 `/tour` 拿 cookie,再帶 `-b` 掛事件流:

```sh
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -N -b /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

跑 `rollout restart` 時,若這條流所在的 pod 被換掉,streaming 會**當場斷掉**(step0 不排空、不善終)。
到 step3/step4 加上 drain + 狀態外部化後,同樣操作下連線會被好好收尾、進度也不再歸零。

## 下一步

step0 是基準線。**不必先清理**,直接切到 step1 就會原地更新同一組資源、一步步把上面的問題補起來:

```sh
kubectl apply -k deploy/step1
```

→ [step1:preStop 緩衝](step1.md)

## 6. 清理(想整個收掉時)

```sh
kubectl delete -k deploy/step0
```
