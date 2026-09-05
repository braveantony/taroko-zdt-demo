# hydra — 太魯閣線上導覽

hydra 是一支 Go 寫的單一執行檔,前端頁面和七張站點照片都用 `go:embed` 打包進去,不需要任何外部檔案。它提供一個太魯閣導覽頁,用 SSE 把導覽逐站推進,另外開了健康檢查和 Prometheus 指標。這個 demo 用它來觀察滾動更新對「長連線」和「狀態」的影響。

## 端點

| 路徑 | 說明 |
|---|---|
| `/tour` | 導覽頁(HTML) |
| `/tour/events` | SSE 事件流(見下) |
| `/tour/static/…` | 站點照片 |
| `/version` | 建置版本字串 |
| `/healthz` | 存活檢查(liveness) |
| `/readyz` | 就緒檢查(readiness) |
| `/metrics` | Prometheus 指標 |

`/tour/events` 會送四種事件:

- `hello` — 連上時先打聲招呼,附上目前進度
- `station` — 推進到下一站
- `notice` — 公告(順便當 keep-alive,免得連線閒置太久被中間的 proxy 或 load balancer 切掉)
- `bye` — 關機時的道別(只有 `HYDRA_SSE_DRAIN=on` 時才會送)

## 狀態存哪裡

進度(每個使用者走到第幾站)透過一個 StateStore 介面存放,有兩種實作:

- **memory** — 記在 pod 自己的記憶體。pod 一換掉,進度就沒了。
- **valkey** — 存到外部的 Valkey。pod 換掉,接手的 pod 讀得到同一份進度。

memory 是 step0–3 用來演「狀態會不見」,valkey 是 step4 用來演「狀態留得住」。

## 關機時怎麼處理連線

SSE 連線一開就不會自己斷,程式關機時有兩種處理方式:

- **不排空**(預設,step0–2):瀏覽器那端連線瞬間中斷,沒有任何預告。
- **先排空再結束**(step3 起,`HYDRA_SSE_DRAIN=on`):送一個 `bye`、把每條連線收乾淨,然後才退出。

## 七個站點

導覽每隔 `HYDRA_TOUR_INTERVAL_SECONDS`(預設 10 秒)推進一站,循環播放:

1. 遊客中心(Taroko Visitor Center)
2. 砂卡礑步道(Shakadang Trail)
3. 長春祠(Eternal Spring Shrine)
4. 燕子口(Swallow Grotto)
5. 九曲洞(Tunnel of Nine Turns)
6. 白楊步道(Baiyang Trail)
7. 清水斷崖(Qingshui Cliff)

照片取自 Wikimedia Commons,採 CC 授權,出處與作者列在 [`internal/tour/web/ATTRIBUTION.md`](internal/tour/web/ATTRIBUTION.md)。

## 設定(環境變數)

| 變數 | 預設 | 說明 |
|---|---|---|
| `HYDRA_PORT` | `8080` | 監聽埠 |
| `HYDRA_GRACEFUL` | `on` | 收到 SIGTERM 是否優雅關機;`off` 就立刻結束(step0 用) |
| `HYDRA_STATE_BACKEND` | `memory` | 進度存放:`memory` 或 `valkey` |
| `HYDRA_VALKEY_ADDR` | `valkey:6379` | Valkey 位址(只有 `valkey` 後端會用到) |
| `HYDRA_SSE_DRAIN` | `off` | 關機是否先道別、收掉 SSE 連線 |
| `HYDRA_TOUR_INTERVAL_SECONDS` | `10` | 導覽推進間隔,1–60 秒 |
| `HYDRA_SHUTDOWN_TIMEOUT_SECONDS` | `15` | 關機收尾的時間上限 |
| `HYDRA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## 自己重建映像

從本目錄執行:

```sh
sudo podman build -f Containerfile --build-arg VERSION=<版本字串> \
  -t ghcr.io/braveantony/hydra:<tag> .
```

`VERSION` 會顯示在 `/version`。用 golang:1.27 編譯,產物放進 distroless/static:nonroot 執行(uid 65532、監聽 8080 埠),映像裡沒有 shell 也沒有套件管理器。
