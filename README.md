# 太魯閣導覽網站 — Kubernetes 零停機 demo

一個給 Kubernetes Summit 2026 的教學 demo。主角是一個叫 **hydra** 的太魯閣線上導覽網站:當它在滾動更新(rolling update)時被換掉,使用者會遇到什麼?從連線被硬生生切斷、導覽進度歸零,一路修到使用者完全無感。每一步只多加一個機制,讓你看清楚每個機制各自擋掉了哪個問題。

## 為什麼用 hydra 當主角

hydra 有兩個特性,剛好踩在滾動更新最容易出事的地方:

- **長連線**:它用 SSE(Server-Sent Events)把導覽一站一站推進,連線會一直開著。
- **狀態**:每個使用者走到第幾站,預設記在 pod 自己的記憶體裡。

pod 被換掉的那一刻,連線還開著、進度還在記憶體裡。關機處理有沒有做好,畫面上一眼就看得出來——這正是 demo 想讓你看到的。

## 五個步驟

每一步只多加一個機制,擋掉前一步還擋不掉的問題:

| 步驟 | 多加了什麼 | 使用者這時會看到什麼 |
|---|---|---|
| **[step0](demo/step0.md)** | 什麼都不做 | 連線瞬間斷掉、導覽進度歸零 |
| **[step1](demo/step1.md)** | preStop 緩衝 | 新請求不再打到即將關閉的 pod;但已開的連線最後還是會斷 |
| **[step2](demo/step2.md)** | 程式優雅關機 | 一般 HTTP 請求會收完再結束;但 SSE 這種收不完的長連線仍會被切 |
| **[step3](demo/step3.md)** | 連線排空(drain) | 瀏覽器先收到道別、乾淨重連;只是進度還會歸零 |
| **[step4](demo/step4.md)** | Valkey(Redis 相容的外部儲存)+ 就緒檢查 | 重連後進度接得上,新 pod 準備好才接流量,全程無感 |

五步共用 `zdt-tour` namespace,用 `kubectl apply -k deploy/stepN` 原地切換——改的是同一組資源,
apply 下去就觸發一次滾動更新。每步的操作步驟見 [`demo/`](demo/)。

## 目錄

| 目錄 | 內容 |
|---|---|
| [`infra/`](infra/) | kind + Cilium 環境,`up.sh` 一鍵建好;另附工具箱映像,把版本相符的 kind/kubectl/cilium/helm 裝到本機 |
| [`deploy/`](deploy/) | Kustomize:base(陽春版 hydra)→ common(namespace + 壓測 client)→ 各 step overlay |
| [`images/hydra/`](images/hydra/) | hydra 原始碼與 Containerfile,細節見該目錄 README |
| [`images/loadtest/`](images/loadtest/) | 壓測用的 client 映像(內含 oha 與 curl) |
| [`demo/`](demo/) | 各 step 的操作流程:[step0](demo/step0.md) · [step1](demo/step1.md) · [step2](demo/step2.md) · [step3](demo/step3.md) · [step4](demo/step4.md) |

## 映像(公開於 GHCR)

- `ghcr.io/braveantony/hydra` — 導覽網站本體
- `ghcr.io/braveantony/loadtest` — 壓測 client
- `ghcr.io/braveantony/taroko-tools` — 把 kind / kubectl / cilium / helm 裝到本機的工具箱

## 快速上手

```sh
# 1. 建環境(前置:先用工具箱把 kind/kubectl/cilium/helm 裝好)
sudo -v && ./infra/up.sh

# 2. 部署 step0
kubectl --context kind-zdt apply -k deploy/step0
```

跑完 step0 就 `apply -k deploy/step1`、`deploy/step2`…一路到 `deploy/step4`,原地一步步看每個機制的效果。

環境細節見 [`infra/README.md`](infra/README.md);step0 逐步操作(跑壓測、觸發滾動更新、看連線怎麼被切斷)見 [`demo/step0.md`](demo/step0.md)。
