#!/bin/sh
# 把工具複製到掛載進來的 host 目錄(預設 /out,對到 host 的 ~/.local/bin)。
# 用法:
#   podman run --rm -v "$HOME/.local/bin:/out" ghcr.io/braveantony/taroko-tools
set -eu
OUT="${OUT:-/out}"
if [ ! -d "$OUT" ]; then
  echo "✗ 請把 host 目錄掛載到 $OUT,例如:  -v \"\$HOME/.local/bin:/out\""
  exit 1
fi
for t in kind kubectl cilium; do
  cp -f "/tools/$t" "$OUT/$t"
  chmod 0755 "$OUT/$t"
  echo "  ✓ $OUT/$t"
done
echo "已安裝 → kind $KIND_VERSION · kubectl $KUBECTL_VERSION · cilium-cli $CILIUM_CLI_VERSION"
echo "確認 ~/.local/bin 有在 PATH 裡(bash: 'export PATH=\$HOME/.local/bin:\$PATH')。"
