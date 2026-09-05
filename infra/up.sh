#!/usr/bin/env bash
# 一鍵建立 taroko-zdt-demo 的 kind + Cilium + L2 叢集(叢集名/網路名 = zdt)。
# 對應 README 步驟 1–6(podman 網路 → kind → kubeconfig → Gateway API CRD → Cilium → L2)。
# 前置:先用工具箱把 kind/kubectl/cilium/helm 裝到 ~/.local/bin(見 infra/tools)。
# 用法: sudo -v; ./up.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PATH="$HOME/.local/bin:$PATH"   # 工具箱把 kind/kubectl/cilium/helm 裝在這
CLUSTER="zdt"                          # kind 叢集名 + podman 網路名(同名)
K="kubectl --context kind-${CLUSTER}"
CILIUM_VERSION="1.20.1"
GWAPI_VERSION="v1.6.1"

# kind 在 ~/.local/bin,但 sudo 的 secure_path 找不到 → 解析絕對路徑、用 sudo env 帶進去
KIND_BIN="$(command -v kind || echo "$HOME/.local/bin/kind")"
[ -x "$KIND_BIN" ] || { echo "✗ 找不到 kind。先用工具箱裝:"; echo "    sudo podman run --rm -v \"\$HOME/.local/bin:/out\" ghcr.io/braveantony/taroko-tools"; exit 1; }
command -v helm >/dev/null || { echo "✗ 找不到 helm,請先用工具箱裝。"; exit 1; }
kind_run(){ sudo env KIND_EXPERIMENTAL_PROVIDER=podman KIND_EXPERIMENTAL_PODMAN_NETWORK="$CLUSTER" "$KIND_BIN" "$@"; }

log(){ printf '\n\033[1;36m▶ %s\033[0m\n' "$*"; }

# ── 0. 叢集已存在就先擋下 ────────────────────────────────
if kind_run get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "叢集 $CLUSTER 已存在。要重建請先執行 ./down.sh。"
  exit 1
fi

# ── 1. podman 網路(固定 10.89.0.0/24,LB pool 依賴此段) ──
log "1/6 podman 網路 ($CLUSTER)"
if ! sudo podman network exists "$CLUSTER"; then
  sudo podman network create --driver bridge --subnet 10.89.0.0/24 --gateway 10.89.0.1 "$CLUSTER"
fi
sub="$(sudo podman network inspect "$CLUSTER" --format '{{range .Subnets}}{{.Subnet}} {{end}}')"
echo "  $CLUSTER 網路 subnet: $sub"
case "$sub" in
  *10.89.0.0/24*) ;;
  *) echo "  ⚠ subnet 不是 10.89.0.0/24,LB pool(10.89.0.192/27)會對不上;請 sudo podman network rm $CLUSTER 後重跑。"; exit 1;;
esac

# ── 2. 建叢集 ────────────────────────────────────────────
log "2/6 kind create cluster (1 control + 4 worker)"
kind_run create cluster --config "$HERE/kind.yaml"

# ── 3. 合併 kubeconfig 到 ~/.kube/config ────────────────
log "3/6 kubeconfig"
tmp="$(mktemp)"
kind_run get kubeconfig --name "$CLUSTER" > "$tmp"
KUBECONFIG="$HOME/.kube/config:$tmp" kubectl config view --flatten > "$HOME/.kube/config.new"
mv "$HOME/.kube/config.new" "$HOME/.kube/config" && chmod 600 "$HOME/.kube/config" && rm -f "$tmp"

# ── 4. Gateway API CRD ──────────────────────────────────
log "4/6 Gateway API CRD $GWAPI_VERSION"
B="https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/${GWAPI_VERSION}/config/crd/standard"
for c in gatewayclasses gateways httproutes grpcroutes referencegrants backendtlspolicies tlsroutes; do
  $K apply --server-side -f "$B/gateway.networking.k8s.io_${c}.yaml"
done

# ── 5. Cilium(native routing + KPR + L2 + Gateway API) ──
log "5/6 Cilium $CILIUM_VERSION"
helm repo add cilium https://helm.cilium.io/ >/dev/null 2>&1 || true
helm repo update >/dev/null
helm upgrade --install cilium cilium/cilium --version "$CILIUM_VERSION" \
  -n kube-system --kube-context "kind-${CLUSTER}" -f "$HERE/cilium/values.yaml"
$K -n kube-system rollout status ds/cilium --timeout=15m
$K wait --for=condition=Ready node --all --timeout=5m

# ── 6. L2 announcement(LB pool + policy) ────────────────
log "6/6 L2 announcement"
$K apply -f "$HERE/l2/lb-pool.yaml" -f "$HERE/l2/l2-policy.yaml"

log "✅ 叢集就緒 (context: kind-${CLUSTER})"
$K get nodes -o wide
echo
echo "下一步:部署 taroko demo(step0) → 見上層 README。"
