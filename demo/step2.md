# Step 2:優雅關機

step1 的問題:app 收到 SIGTERM 立刻結束,在途連線被硬斷。

**這步加的**(相對 step1):打開程式層的優雅關機 `HYDRA_GRACEFUL=on`,
並把 `terminationGracePeriodSeconds` 從 30 拉到 45 給它時間。收到 SIGTERM 後,
app 會停止接受新連線、把在途的 HTTP 請求處理完,再退出。

## 切到這步

```sh
kubectl --context kind-zdt apply -k deploy/step2
kubectl --context kind-zdt -n zdt-tour rollout status deploy/hydra
```

## 觀察

一般 HTTP 請求(oha 打 `/version`)這下應該乾淨了,所以要換個角度——
用一條 **SSE 長連線** 才看得到 step2 的極限。

右終端照舊 `watch` pods。另開一個終端,在 client 內先拿 session cookie、再掛一條 SSE:

```sh
kubectl --context kind-zdt -n zdt-tour exec -it deploy/client -- sh -c \
  'curl -s -c /tmp/jar http://hydra.zdt-tour.svc.cluster.local/tour >/dev/null &&
   curl -s -b /tmp/jar -N http://hydra.zdt-tour.svc.cluster.local/tour/events'
```

會看到 `hello`、`station` 事件持續進來(留意 `pod` 欄位是哪一顆)。這時觸發滾動更新:

```sh
kubectl --context kind-zdt -n zdt-tour rollout restart deploy/hydra
```

## 預期現象

- **oha `/version`**:Error distribution 幾乎歸零——在途的一般 HTTP 請求被優雅收完。
- **SSE 那條流**:它是永遠不會自己結束的長連線,`http.Server.Shutdown` 等不到它結束 →
  撞到 15 秒 shutdown 上限後被強制剪斷。你會看到 SSE 大約在關機 **15 秒後**才斷——
  不是立刻,但**還是斷了**。

## 還沒解決

優雅關機救得了「會結束的請求」,救不了「不會自己結束的長連線」。
得讓 app 關機時主動跟 SSE 道別、收線 → [step3](step3.md)。
