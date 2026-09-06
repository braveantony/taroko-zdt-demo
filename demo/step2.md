# Step 2:優雅關機

step1 的問題:app 收到 SIGTERM 立刻結束,在途連線被硬斷。

**這步加的**(相對 step1):打開程式層的優雅關機 `HYDRA_GRACEFUL=on`。收到 SIGTERM 後,
app 停止接新連線、把在途的 HTTP 請求處理完再退出;這個等待最多 15 秒(hydra 的 shutdown 上限
`HYDRA_SHUTDOWN_TIMEOUT_SECONDS`,預設 15),等不到就強制收掉。

順帶把 `terminationGracePeriodSeconds` 從 30 拉到 45:preStop 先睡 15 秒、shutdown 最多再等 15 秒,
原本的 30 秒剛好貼著上限,拉到 45 留點餘裕,免得 SIGKILL 搶先到。

## 這步的 Deployment 關鍵設定

apply 之前先渲染出來看(只在本機組出 yaml,不碰叢集):

```sh
kubectl kustomize deploy/step2
```

相對 step1,改了兩處——`GRACEFUL` 與 `terminationGracePeriodSeconds`:

```yaml
      terminationGracePeriodSeconds: 45      # ← step2:30 → 45(留給優雅關機的時間)
      containers:
      - name: hydra
        env:
        - {name: HYDRA_GRACEFUL, value: "on"}          # ← step2:off → on(優雅關機)
        - {name: HYDRA_STATE_BACKEND, value: memory}
        - {name: HYDRA_SSE_DRAIN, value: "off"}         # 仍不排空 SSE
        - {name: HYDRA_TOUR_INTERVAL_SECONDS, value: "10"}
        # lifecycle.preStop(15s)沿用 step1;nodeSelector / affinity / strategy / liveness 同前
```

> 指令用精簡寫法;請先照 [step0 的前置](step0.md) 設好 `kind-zdt` context 與 `zdt-tour` namespace,
> 這樣 bare `kubectl` 才會指到對的地方。

## 切到這步

```sh
kubectl apply -k deploy/step2
kubectl rollout status deploy/hydra
```

## 觀察

oha 打 `/version` 從 step1 起就已經乾淨(短請求換手 preStop 就蓋掉了),step2 也一樣。
所以 step2 做了什麼、又卡在哪,要換個角度——用一條 **SSE 長連線** 才看得出來。

右終端照舊 `watch` pods,左終端的 oha 可以照 step1 繼續跑著(待會要對照)。再另開一個終端,
在 client 內先拿 session cookie、再掛一條 SSE:

```sh
kubectl exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

會看到 `hello`、`station` 事件持續進來(留意 `pod` 欄位是哪一顆)。這時觸發滾動更新:

```sh
kubectl rollout restart deploy/hydra
```

## 預期現象

- **oha `/version`**:維持 100%(和 step1 一樣乾淨)。graceful 對這種短請求看不出額外差別——
  它真正收拾的是「較長、SIGTERM 到時還在處理」的在途請求;`/version` 太快,沒東西留給它收。
  (偶爾的 `Connection refused` 是 step1 提過的啟動端缺口,step4 才補。)
- **SSE 那條流**:它是永遠不會自己結束的長連線,app 的 Shutdown 等不到它,撞到 15 秒上限後被
  強制剪斷。從 pod 進入 `Terminating` 算起大約 **30 秒**(preStop 15 + shutdown 15)SSE 才斷——
  不是立刻,但**還是斷了**。對照 step1:那裡 SIGTERM 一到 app 立刻死、SSE 約 15 秒就斷;
  step2 的 graceful 會多等它到 shutdown 上限,所以拖到約 30 秒——有等,只是等不到永不結束的 SSE。

## 還沒解決

優雅關機救得了「會結束的請求」,救不了「不會自己結束的長連線」。
得讓 app 關機時主動跟 SSE 道別、收線 → [step3](step3.md)。
