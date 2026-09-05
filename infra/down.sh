#!/usr/bin/env bash
# 拆除 taroko-zdt-demo 的 kind 叢集(對應 README 步驟 9)。
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
KIND_BIN="$(command -v kind || echo "$HOME/.local/bin/kind")"
sudo env KIND_EXPERIMENTAL_PROVIDER=podman "$KIND_BIN" delete cluster --name kind
kubectl config delete-context kind-kind 2>/dev/null || true
kubectl config delete-cluster kind-kind 2>/dev/null || true
kubectl config delete-user kind-kind 2>/dev/null || true
echo "✅ 已拆除 kind 叢集(podman 網路 kind 保留;要刪:sudo podman network rm kind)"
