#!/usr/bin/env bash
# 拆除 taroko-zdt-demo 的 kind 叢集(對應 README 步驟 9)。
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
CLUSTER="zdt"
KIND_BIN="$(command -v kind || echo "$HOME/.local/bin/kind")"
sudo env KIND_EXPERIMENTAL_PROVIDER=podman "$KIND_BIN" delete cluster --name "$CLUSTER"
kubectl config delete-context "kind-${CLUSTER}" 2>/dev/null || true
kubectl config delete-cluster "kind-${CLUSTER}" 2>/dev/null || true
kubectl config delete-user "kind-${CLUSTER}" 2>/dev/null || true
echo "✅ 已拆除 kind 叢集 $CLUSTER(podman 網路 $CLUSTER 保留;要刪:sudo podman network rm $CLUSTER)"
