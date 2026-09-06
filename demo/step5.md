# Step 5:從叢集外部打(Cilium L2 + Gateway API)

前面 step0~4 的 oha 都是在 **client pod 內部**打 hydra 的 ClusterIP。這步換個打法:把 hydra 用
**Cilium L2 Announcements + Gateway API** 對外曝露,改從**叢集外(你的 host)**打,重跑一次 step0~4。

重點不是再加機制,而是驗證一件事:**前面擺一個 L7 Gateway(Cilium 底層是 Envoy),會不會改變結果?**
——會,而且剛好畫出「Gateway 能救什麼、不能救什麼」的界線:
- **短請求**:Envoy 幫你把換 pod 的錯誤吃掉一大半 → 外部看起來比內部乾淨很多。
- **SSE 長連線**:Gateway 救不了,還是得靠 app 自己(step3/4)。

## 前置:確認 Cilium 已就緒

這套環境已經開好(`kubeProxyReplacement` / `l2announcements` / `gatewayAPI` / `lb-ipam`),先確認:

```sh
kubectl -n kube-system get configmap cilium-config \
  -o jsonpath='{.data.kube-proxy-replacement}{"  "}{.data.enable-l2-announcements}{"  "}{.data.enable-gateway-api}{"\n"}'
kubectl get gatewayclass cilium                      # ACCEPTED 應為 True
kubectl get ciliumloadbalancerippool,ciliuml2announcementpolicy   # pool 與 policy 應已存在
```

> 若你的環境沒開,得先用 Helm 打開 `l2announcements.enabled` / `kubeProxyReplacement=true` /
> `gatewayAPI.enabled=true`,並建一個 `CiliumLoadBalancerIPPool`(給 LB IP)＋ `CiliumL2AnnouncementPolicy`
> (ARP 宣告)。這套已經有 `l2-pool`(網段 `10.89.0.192/27`)和 `l2-policy`(選所有 LB Service),不用再建。

## 建立對外入口(Gateway + HTTPRoute)

`deploy/gateway/` 放了一個 `gatewayClassName: cilium` 的 Gateway ＋ 一條把所有路徑轉給 `hydra:80` 的
HTTPRoute。套下去:

```sh
kubectl apply -k deploy/gateway
kubectl -n zdt-tour get gateway hydra -w   # PROGRAMMED 變 True 就緒(Ctrl-C 離開)
```

Cilium 會**自動**替這個 Gateway 建一個 LoadBalancer Service（`cilium-gateway-hydra`）,從 `l2-pool` 配一個
IP、由 `l2-policy` 用 ARP 對外宣告。拿到那個對外 IP:

```sh
GW=$(kubectl -n zdt-tour get gateway hydra -o jsonpath='{.status.addresses[0].value}')
echo "$GW"                       # 會是 10.89.0.192/27 範圍內的一個 IP
curl -s "http://$GW/version"     # host 直接連得到 → 對外入口 OK
```

> 連不到的話:確認 host 到 `10.89.0.0/24`(kind/podman 的 bridge)有路由——node IP 也在這段
> (`10.89.0.2~6`),連得到 node 通常就連得到 LB IP。

## 從叢集外重跑 step0~4(短請求)

對**每一個** step 做同一件事:切到那步 → 從 host 用 oha 打 `/version` → 壓測跑著時觸發滾動更新 →
記下 `Error distribution`。以 step0 為例(其餘把 `step0` 換成 `step1`…`step4`):

```sh
# ① 切到要測的那步,等它換完
kubectl apply -k deploy/step0
kubectl rollout status deploy/hydra

# ② host 這端起壓測(打對外 Gateway IP,不是 ClusterIP)
oha -z 60s -c 20 --disable-keepalive "http://$GW/version"
```

```sh
# ③ 壓測跑著時,另開終端機觸發滾動更新
kubectl rollout restart deploy/hydra
```

## SSE 也從外部掛一條

短請求測完,順手從 host 掛一條 SSE、觸發滾動更新,看長連線的差別:

```sh
curl -s -c /tmp/jar "http://$GW/tour" >/dev/null &&
  curl -s -b /tmp/jar -N "http://$GW/tour/events"
# 另一個終端機:kubectl rollout restart deploy/hydra
```

## 預期與重點:Gateway 改變了什麼

**短請求 `/version`——外部經 Gateway,錯誤比內部 ClusterIP 少很多。**
Cilium 底層的 Envoy 幫你做三件事(以下 Cilium 行為參考 DeepWiki / cilium/cilium):**上下游連線脫鉤**
(client 到 Gateway 那條連線全程不動)、**endpoint draining**(pod 一標 `Terminating` 就停送新流量)、
**HTTP retry**(連線錯誤/RST 自動改送別顆,`httpRetryCount` 預設 3)。所以「新連線打到將死 pod」那種
refused 大多被 Envoy 吃掉或重試掉:

- **step0**(沒 preStop)內部直打有一堆 `Connection refused`(那 266 個);**從外部經 Gateway 打,預期
  會少非常多、甚至接近 0**——因為 Envoy 幫它擋掉了。**這就是「為什麼前面擺個 Gateway 感覺就沒事了」。**
- step1~4 從外部看,連線層錯誤一樣很低。

**但這只證明了「短請求層」。長連線 `/tour/events`(SSE)——Gateway 救不了。**
一條 SSE 是一個永不結束的請求,綁死在一顆 backend pod 上;那顆 pod 被換掉,串流就結束、host 這端就得重連
(Envoy 沒辦法把進行中的串流搬到新 pod)。所以:

- **step0~step2**:SSE 從外部看**照樣會斷、要重連**(step1 的 preStop、step2 的 graceful 都改善不了「串流被換掉」)。
- **step3**:關機前先送 `bye`、乾淨收線 → 重連可預期。
- **step4**:重連後 `seq` 接得上 → 真正無感。

換句話說:**Gateway 把「短請求 + 換手」做到近乎無感,但「長連線 + 有狀態」的零停機,還是得靠 app 的
graceful / drain / 狀態外部化(step2~4)。preStop 也一樣省不掉——DeepWiki 裡 Cilium 官方就把 preStop 列為
換 pod 掉連線的建議解法。**

## 對照表(實測貼給我,我補進來)

同一組 rollout,內部 ClusterIP(step0~4 各步原本的數字)vs 外部 Gateway:

| step | `/version` 內部 ClusterIP | `/version` 外部 Gateway | SSE 外部 |
|---|---|---|---|
| step0 | 266 連線錯誤 | 待填(預期 ≪ 266) | 斷、重連 |
| step1 | 0 | 待填(預期 0) | 斷、重連 |
| step2 | 0(短請求) | 待填 | 斷、重連(SSE 撐到約 30s) |
| step3 | 0 | 待填 | 先 `bye` 再重連 |
| step4 | 0 | 待填 | 重連後 `seq` 接得上 |

> 上面「外部 Gateway」欄的數字是**預期方向**(短請求 ≪ 內部;SSE 一樣要重連)。你跑完把每步的 oha
> `Error distribution` 貼給我,我補成完整對照。

## 收尾

```sh
kubectl delete -k deploy/gateway     # 移除對外入口(LB IP 一併釋放)
```

> 移掉 Gateway 不影響 step0~4 本身;它們照樣能用 client pod 內部的 ClusterIP 測。
