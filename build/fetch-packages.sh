#!/usr/bin/env bash
# ----------------------------------------------------------------------------
# 准备离线安装所需的第三方依赖（Node / VictoriaMetrics tarball）
# 到 dist/artifacts/packages/。幂等：已存在则跳过。
#
# 主要用途：
#   - GitHub Actions 在构建 full 包前自动下载（无需本地准备）
#   - 本地开发者也可手动跑这个脚本准备 packages
#
# 注意：以下三个版本号必须与 deploy/ 脚本里的默认包名保持一致：
#   NODE_VERSION → deploy/install-server.sh:default_node_pkg
#   VM_VERSION   → deploy/install-tsdb.sh:default_vm_pkg
#   VL_VERSION   → deploy/install-logs.sh:default_vl_pkg
#       改了下面变量需要同步改 deploy/ 脚本。
# ----------------------------------------------------------------------------
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

NODE_VERSION="v24.18.0"
VM_VERSION="v1.148.0"
# VictoriaLogs 是**独立的仓库与发布线**（VictoriaMetrics/VictoriaLogs），
# 版本号与 VM 不同步——不要跟着 VM_VERSION 一起改。
VL_VERSION="v1.53.0"

OUT_DIR="dist/artifacts/packages"
mkdir -p "$OUT_DIR"

c_info() { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()   { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn() { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
die()    { printf '\033[31m[错误]\033[0m %s\n' "$*"; exit 1; }

have_cmd() { command -v "$1" >/dev/null 2>&1; }

# 下载（已存在则跳过）；失败返回非零，由调用方决定是致命还是可降级。
fetch() {
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
  [[ -s "$dest" ]]
}

# 必需依赖：失败即终止（Node 与 VM 缺一不可）。
download() {
  fetch "$1" "$2" || die "下载失败或文件为空: $1"
}

# 可选依赖：失败只告警（VictoriaLogs 是可选的日志后端，见文件末尾说明）。
download_optional() {
  fetch "$1" "$2" || {
    c_warn "下载失败（可选组件，不阻断发布）: $(basename "$2")"
    return 1
  }
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

# ---------- VictoriaLogs（可选：集中日志的外部后端） ----------
# 只有把 logBackend 设为 victorialogs 时才需要，因此**失败不致命**：
# 拉不到就让 full 包少一个可选组件，而不是让整条发布流水线断掉
# （离线包的主体是 Server/Agent，日志后端是可选能力，见 ADR-0002）。
c_info "VictoriaLogs ${VL_VERSION}（可选组件）"
for arch in amd64 arm64 arm; do
  download_optional "https://github.com/VictoriaMetrics/VictoriaLogs/releases/download/${VL_VERSION}/victoria-logs-linux-${arch}-${VL_VERSION}.tar.gz" \
                    "${OUT_DIR}/victoria-logs-linux-${arch}-${VL_VERSION}.tar.gz" || true
done

c_ok "所有第三方包已就绪: ${OUT_DIR}/"
ls -lh "$OUT_DIR"
