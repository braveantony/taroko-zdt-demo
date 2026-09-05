# infra

kind + Cilium 1.20.1 native routing + L2 announcement

| 檔案 | 用途 |
|---|---|
| `kind.yaml` | 3 節點，無 CNI、無 kube-proxy |
| `cilium/values.yaml` | Cilium Helm values |
| `l2/lb-pool.yaml` | LB-IPAM pool `10.89.0.192/27` |
| `l2/l2-policy.yaml` | worker eth0 宣告 LoadBalancer IP |
| `l2/demo-httpd.yaml` | 驗證用 LoadBalancer Service |

## 網段

| 用途 | CIDR |
|---|---|
| podman 網路 `kind`（節點，步驟 1 固定建立） | 10.89.0.0/24 |
| Pod | 10.244.0.0/16 |
| LoadBalancer IP | 10.89.0.192/27 |

## 版本

kind v0.32.0（podman provider）、kindest/node v1.36.1、Cilium 1.20.1、Gateway API v1.6.1

## 步驟

```sh
cd ~/infra
K="kubectl --context kind-kind"
```

1. 建立 podman 網路（固定網段，LB pool 依賴此段）

   ```sh
   sudo podman network exists kind || sudo podman network create --driver bridge --subnet 10.89.0.0/24 --gateway 10.89.0.1 kind
   sudo podman network inspect kind --format '{{range .Subnets}}{{.Subnet}} {{end}}'   # 必須是 10.89.0.0/24
   ```

2. 建立叢集

   ```sh
   sudo KIND_EXPERIMENTAL_PROVIDER=podman KIND_EXPERIMENTAL_PODMAN_NETWORK=kind kind create cluster --config kind.yaml
   ```

3. 合併 kubeconfig

   ```sh
   tmp=$(mktemp)
   sudo KIND_EXPERIMENTAL_PROVIDER=podman kind get kubeconfig --name kind > "$tmp"
   KUBECONFIG="$HOME/.kube/config:$tmp" kubectl config view --flatten > ~/.kube/config.new
   mv ~/.kube/config.new ~/.kube/config && chmod 600 ~/.kube/config && rm "$tmp"
   ```

4. Gateway API CRD

   ```sh
   B=https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/v1.6.1/config/crd/standard
   for c in gatewayclasses gateways httproutes grpcroutes referencegrants backendtlspolicies tlsroutes; do
     $K apply --server-side -f $B/gateway.networking.k8s.io_${c}.yaml
   done
   ```

5. 安裝 Cilium

   ```sh
   helm repo add cilium https://helm.cilium.io/ && helm repo update
   helm install cilium cilium/cilium --version 1.20.1 -n kube-system --kube-context kind-kind -f cilium/values.yaml
   $K -n kube-system rollout status ds/cilium --timeout=15m
   $K wait --for=condition=Ready node --all --timeout=5m
   cilium --context kind-kind status --wait
   ```

6. L2 announcement

   ```sh
   $K apply -f l2/lb-pool.yaml -f l2/l2-policy.yaml
   $K get ciliumloadbalancerippool ciliuml2announcementpolicy
   ```

7. 驗證

   ```sh
   $K apply -f l2/demo-httpd.yaml
   $K rollout status deploy/httpd
   VIP=$($K get svc httpd -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
   curl -s http://$VIP/
   ip neigh show $VIP
   $K -n kube-system get lease | grep l2announce
   ```

8. 更新 Cilium

   ```sh
   helm upgrade cilium cilium/cilium --version 1.20.1 -n kube-system --kube-context kind-kind -f cilium/values.yaml
   $K -n kube-system rollout status ds/cilium --timeout=10m
   ```

9. 拆除

   ```sh
   sudo KIND_EXPERIMENTAL_PROVIDER=podman kind delete cluster --name kind
   kubectl config delete-context kind-kind; kubectl config delete-cluster kind-kind; kubectl config delete-user kind-kind
   ```

## 注意

- LB pool 必須在 podman `kind` 網路內；若已存在的 `kind` 網路不是 10.89.0.0/24，先 `sudo podman network rm kind` 重建，或同步改 `l2/lb-pool.yaml`。
- 多個 pool 的 CIDR 不可重疊（重疊會 CONFLICTING）。
- 多個 kind 叢集同時跑時：`sudo sysctl -w fs.inotify.max_user_instances=1024 fs.inotify.max_user_watches=1048576`
- 改 Pod CIDR 需 `kubectl patch ciliumpodippool default`，helm upgrade 不會更新既有 pool。
- 叢集改名時 `k8sServiceHost` 要改成 `<name>-control-plane`。
