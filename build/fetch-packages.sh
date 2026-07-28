#!/usr/bin/env bash
# ----------------------------------------------------------------------------
# 准备离线安装所需的第三方依赖（Node / VictoriaMetrics tarball）
# 到 dist/artifacts/packages/。幂等：已存在则跳过。
#
# 主要用途：
#   - GitHub Actions 在构建 full 包前自动下载（无需本地准备）
#   - 本地开发者也可手动跑这个脚本准备 packages
#
# 注意：以下两个版本号必须与 deploy/install-server.sh:default_node_pkg
#       和 deploy/install-tsdb.sh:default_vm_pkg 中的默认文件名保持一致。
#       改了下面变量需要同步改 deploy/ 脚本。
# ----------------------------------------------------------------------------
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

NODE_VERSION="v24.18.0"
VM_VERSION="v1.148.0"

OUT_DIR="dist/artifacts/packages"
mkdir -p "$OUT_DIR"

c_info() { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()   { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn() { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
die()    { printf '\033[31m[错误]\033[0m %s\n' "$*"; exit 1; }

have_cmd() { command -v "$1" >/dev/null 2>&1; }

# 下载（已存在则跳过）
download() {
  local url="$1" dest="$2"
  if [[ -s "$dest" ]]; then
    c_info "已存在，跳过: $(basename "$dest")"
    return 0
  fi
  c_info "下载: $(basename "$dest")"
  if have_cmd curl; then
    curl -fL --retry 3 --connect-timeout 15 -o "$dest.tmp" "$url" \
      && mv "$dest.tmp" "$dest"
  elif have_cmd wget; then
    wget -q -O "$dest.tmp" "$url" && mv "$dest.tmp" "$dest"
  else
    die "需 curl 或 wget 下载第三方包"
  fi
  [[ -s "$dest" ]] || die "下载失败或文件为空: $url"
}

# ---------- Node ----------
c_info "Node ${NODE_VERSION}"
download "https://nodejs.org/dist/${NODE_VERSION}/node-${NODE_VERSION}-linux-x64.tar.xz" \
         "${OUT_DIR}/node-${NODE_VERSION}-linux-x64.tar.xz"
download "https://nodejs.org/dist/${NODE_VERSION}/node-${NODE_VERSION}-linux-arm64.tar.xz" \
         "${OUT_DIR}/node-${NODE_VERSION}-linux-arm64.tar.xz"

# ---------- VictoriaMetrics ----------
c_info "VictoriaMetrics ${VM_VERSION}"
download "https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/${VM_VERSION}/victoria-metrics-linux-amd64-${VM_VERSION}.tar.gz" \
         "${OUT_DIR}/victoria-metrics-linux-amd64-${VM_VERSION}.tar.gz"
download "https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/${VM_VERSION}/victoria-metrics-linux-arm64-${VM_VERSION}.tar.gz" \
         "${OUT_DIR}/victoria-metrics-linux-arm64-${VM_VERSION}.tar.gz"
download "https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/${VM_VERSION}/victoria-metrics-linux-arm-${VM_VERSION}.tar.gz" \
         "${OUT_DIR}/victoria-metrics-linux-arm-${VM_VERSION}.tar.gz"

c_ok "所有第三方包已就绪: ${OUT_DIR}/"
ls -lh "$OUT_DIR"
