#!/usr/bin/env bash
#
# nebula-monitor 集中日志后端（VictoriaLogs）安装脚本（独立部署，可与 Server 分机）
# ----------------------------------------------------------------------------
# 功能：
#   1. 从离线包安装 VictoriaLogs 二进制 + systemd 单元（不联网下载）
#   2. 启动并做健康检查，输出日志查询地址（供 install-server.sh --log-addr 使用）
#
# 与 deploy/install-tsdb.sh 的关系：**两者是并存的观测后端，不是彼此的替代品**——
#   install-tsdb.sh 装**指标时序库**（VictoriaMetrics / Mimir / Cortex / Thanos），
#   本脚本装**日志存储**（VictoriaLogs）。
# 因此独立成脚本，而不是给 install-tsdb.sh 加一个 `--backend victorialogs`：
# 那会让"时序库后端"这个选项多出一个根本不是 TSDB 的值，读代码的人会先困惑再改错。
#
# 只在 Server 端把 logBackend 设为 victorialogs 时才需要装它（ADR-0002）：
#   - 默认后端是自研分片落盘（local），不装本组件也能用集中日志；
#   - 切到 VL 后，日志的**保留与容量由 VL 自己负责**（本脚本的 --retention），
#     平台不再叠加"单来源每日上限"，也不再执行按天清理；
#   - 上行的限速与请求体上限仍然生效（在 Server 的 receiver 侧，与后端无关）。
#
# 既支持交互式，也支持非交互式（--yes）。
#
set -uo pipefail

# ============================ 默认参数 ============================
VL_BINARY=""
VL_PKG=""
PKG_DIR=""
VL_LISTEN=":9428"
# 默认保留 7 天，与平台自研落盘的默认保留期（retention 的 logsDays=7）保持一致：
# 换后端不该顺手把保留期放大（那会连带把磁盘占用放大好几倍，而没人注意到）。
# VL 自身的默认值是 7d，这里显式写出来是为了让"平台口径"可见、可改。
VL_RETENTION="7d"
VL_STORAGE=""
ASSUME_YES=0
UPGRADE=0

ARCH=""
OS=""

BIN_DIR="/usr/local/bin"
SERVICE_DIR="/etc/systemd/system"
DATA_DIR="/var/lib/victoria-logs-data"
SERVICE_NAME="victoria-logs.service"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 运行时填充
VL_ADDR=""

# ============================ 日志/工具 ============================
c_info() { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()   { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn() { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
c_err()  { printf '\033[31m[错误]\033[0m %s\n' "$*"; }
die()    { c_err "$*"; exit 1; }

have_cmd() { command -v "$1" >/dev/null 2>&1; }

prompt() {
  local __var="$1" __q="$2" __def="$3"
  local __val
  read -r -p "$__q [$__def]: " __val || true
  __val="${__val:-$__def}"
  printf -v "$__var" '%s' "$__val"
}

confirm() {
  local __q="$1" __def="$2" __a
  while true; do
    read -r -p "$__q [$__def]: " __a || true
    __a="${__a:-$__def}"
    case "$__a" in
      y|Y|yes|YES) return 0 ;;
      n|N|no|NO)   return 1 ;;
      *) echo "请输入 y 或 n" ;;
    esac
  done
}

# ============================ 参数解析 ============================
usage() {
  cat <<EOF
用法: $0 [选项]

  --vl-binary <path>    直接使用本地 victoria-logs 二进制（不扫描离线包）
  --vl-package <name>   指定离线包内的 VictoriaLogs 压缩包文件名
  --packages <dir>      离线包目录（默认自动探测 ../dist/artifacts/packages 或 ../offline）
  --listen <addr>       VictoriaLogs 监听地址（默认 :9428）
  --retention <dur>     日志保留期（默认 7d；单位 s/h/d/w/M/y，1d..100y）
  --storage <dir>       数据目录（默认 $DATA_DIR）
  --yes                 非交互式
  --upgrade             升级模式：强制覆盖二进制，保留数据，重启服务
  -h, --help            显示本帮助

说明：本脚本只安装**日志存储**（VictoriaLogs）。指标时序库请用 deploy/install-tsdb.sh；
      装完后在 Server 端设 logBackend=victorialogs + logVictoriaLogs.addr 指向本机（见输出末尾）。
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --vl-binary)  VL_BINARY="$2"; shift 2 ;;
    --vl-package) VL_PKG="$2"; shift 2 ;;
    --packages)   PKG_DIR="$2"; shift 2 ;;
    --listen)     VL_LISTEN="$2"; shift 2 ;;
    --retention)  VL_RETENTION="$2"; shift 2 ;;
    --storage)    VL_STORAGE="$2"; shift 2 ;;
    --yes)        ASSUME_YES=1; shift ;;
    --upgrade)    UPGRADE=1; shift ;;
    -h|--help)    usage ;;
    *) die "未知参数: $1（用 -h 查看帮助）" ;;
  esac
done

[[ -n "$VL_STORAGE" ]] && DATA_DIR="$VL_STORAGE"

# 升级模式：非交互
(( UPGRADE )) && ASSUME_YES=1

# 自动探测离线包目录
#   优先级：dist/artifacts/packages（新结构） > offline（旧结构，兼容）
if [[ -z "$PKG_DIR" ]]; then
  if [[ -d "$SCRIPT_DIR/../dist/artifacts/packages" ]]; then PKG_DIR="$SCRIPT_DIR/../dist/artifacts/packages"
  elif [[ -d "$SCRIPT_DIR/../offline" ]]; then PKG_DIR="$SCRIPT_DIR/../offline"
  elif [[ -d ./dist/artifacts/packages ]]; then PKG_DIR="./dist/artifacts/packages"
  elif [[ -d ./offline ]]; then PKG_DIR="./offline"
  fi
fi
[[ -n "$PKG_DIR" ]] && c_info "检测到离线包目录: $PKG_DIR"

# ============================ 检测 ============================
detect_env() {
  OS="$(uname -s)"
  local m; m="$(uname -m)"
  case "$m" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    armv7l|arm)    ARCH="arm" ;;
    *) die "不支持的架构: $m" ;;
  esac
}

preflight() {
  c_info "预检环境"
  if [[ "$(id -u)" -ne 0 ]]; then
    die "请用 root 或 sudo 执行本脚本（systemd 服务安装/启动需要 root）"
  fi
  if ! have_cmd systemctl; then
    c_warn "未检测到 systemctl，将跳过 systemd 单元安装"
  fi
  c_ok "环境检测: OS=$OS ARCH=$ARCH"
}

# 将包名解析为绝对路径：支持绝对路径 / 相对路径 / 离线包目录内文件名
resolve_pkg() {
  local name="$1" dir="$2"
  [[ -z "$name" ]] && return 1
  if [[ -f "$name" ]]; then printf '%s' "$name"; return 0; fi
  if [[ -n "$dir" && -f "$dir/$name" ]]; then printf '%s' "$dir/$name"; return 0; fi
  return 1
}

# 默认 VictoriaLogs 包名（按 arch）。
# **必须与 build/fetch-packages.sh 的 VL_VERSION 一致**：离线包由那个脚本准备，
# 名字对不上就会变成"包里明明有、脚本却说找不到"。
default_vl_pkg() {
  case "$ARCH" in
    amd64) echo "victoria-logs-linux-amd64-v1.53.0.tar.gz" ;;
    arm64) echo "victoria-logs-linux-arm64-v1.53.0.tar.gz" ;;
    arm)   echo "victoria-logs-linux-arm-v1.53.0.tar.gz" ;;
  esac
}

# 扫描离线包目录：优先默认包名，否则按 arch 列出供确认；支持 --vl-package 直接指定。
scan_vl_package() {
  [[ -n "$VL_BINARY" ]] && return 0
  [[ -n "$PKG_DIR" ]] || return 0
  if [[ -n "$VL_PKG" ]]; then
    local rp; rp="$(resolve_pkg "$VL_PKG" "$PKG_DIR")" || die "指定的 VictoriaLogs 包不存在: $VL_PKG"
    VL_PKG="$rp"
    c_ok "将使用 VictoriaLogs 包: $VL_PKG"
    return 0
  fi
  local def; def="$(default_vl_pkg)"
  if [[ -n "$def" && -f "$PKG_DIR/$def" ]]; then
    if (( ASSUME_YES )); then
      VL_PKG="$def"
    else
      c_info "默认 VictoriaLogs 包: $def"
      if confirm "是否使用默认 VictoriaLogs 包？(n=扫描其它/手动指定)" "yes"; then
        VL_PKG="$def"
      fi
    fi
  fi
  if [[ -z "$VL_PKG" ]]; then
    local found; found="$(ls "$PKG_DIR"/victoria-logs-linux-"$ARCH"-*.tar.gz 2>/dev/null)"
    if [[ -n "$found" ]]; then
      echo "在离线包目录中检测到以下 VictoriaLogs 安装包 (arch=$ARCH):"
      echo "$found" | sed 's#.*/##' | cat -n
      if (( ASSUME_YES )) || confirm "是否使用上述 VictoriaLogs 包？(n=手动指定/跳过)" "yes"; then
        VL_PKG="$(echo "$found" | head -1)"
      fi
    fi
    if [[ -z "$VL_PKG" ]] && ! (( ASSUME_YES )); then
      local inp=""
      prompt inp "请输入离线包目录中的 VictoriaLogs 包文件名（留空=跳过）" ""
      VL_PKG="$inp"
    fi
  fi
  if [[ -n "$VL_PKG" ]]; then
    local rp; rp="$(resolve_pkg "$VL_PKG" "$PKG_DIR")" || die "指定的 VictoriaLogs 包不存在: $VL_PKG"
    VL_PKG="$rp"
    c_ok "将使用 VictoriaLogs 包: $VL_PKG"
  fi
}

# 从解压目录挑选 VL 二进制（企业版含 -prod/-prod-fips，优先非 fips 的 -prod）。
#
# 不写死文件名：官方包内是 victoria-logs-prod，但不同版本/发行形态出现过
# victoria-logs、带 fips 后缀等变体——逐个候选找一遍比"猜一个名字"稳。
pick_vl_binary() {
  local d="$1" b=""
  [[ -f "$d/victoria-logs-prod" ]] && b="$d/victoria-logs-prod"
  [[ -z "$b" && -f "$d/victoria-logs" ]] && b="$d/victoria-logs"
  if [[ -z "$b" ]]; then
    b="$(ls "$d"/victoria-logs* 2>/dev/null | grep -v -- '-fips$' | head -1)"
  fi
  [[ -z "$b" ]] && b="$(ls "$d"/victoria-logs* 2>/dev/null | head -1)"
  printf '%s' "$b"
}

write_vl_service() {
  cat > "$SERVICE_DIR/$SERVICE_NAME" <<EOF
[Unit]
Description=VictoriaLogs (nebula-monitor centralized log backend)
After=network.target

[Service]
Type=simple
ExecStart=$BIN_DIR/victoria-logs -storageDataPath=$DATA_DIR -httpListenAddr=$VL_LISTEN -retentionPeriod=$VL_RETENTION
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  c_ok "已写入 systemd 单元: $SERVICE_DIR/$SERVICE_NAME（保留期 $VL_RETENTION）"
}

# 安装 VL 二进制（离线，不联网下载）
install_vl_binary() {
  if [[ -x "$BIN_DIR/victoria-logs" ]] && (( ! UPGRADE )); then
    c_ok "已检测到 VictoriaLogs: $BIN_DIR/victoria-logs，跳过安装（升级请用 --upgrade）"
    [[ -f "$SERVICE_DIR/$SERVICE_NAME" ]] || write_vl_service
    return
  fi
  if [[ -n "$VL_BINARY" ]]; then
    [[ -f "$VL_BINARY" ]] || die "指定的 VictoriaLogs 二进制不存在: $VL_BINARY"
    install -m 0755 "$VL_BINARY" "$BIN_DIR/victoria-logs" || die "安装 victoria-logs 失败"
    write_vl_service
    c_ok "已安装本地 VictoriaLogs: $BIN_DIR/victoria-logs"
    return
  fi
  if [[ -n "$VL_PKG" ]]; then
    c_info "从本地包解压并安装 VictoriaLogs: $VL_PKG"
    local vtmp; vtmp="$(mktemp -d)"
    tar -xzf "$VL_PKG" -C "$vtmp" || die "解压 VictoriaLogs 包失败: $VL_PKG"
    local vbin; vbin="$(pick_vl_binary "$vtmp")"
    [[ -n "$vbin" ]] || die "包内未找到 victoria-logs* 二进制"
    install -m 0755 "$vbin" "$BIN_DIR/victoria-logs" || die "安装 victoria-logs 失败"
    rm -rf "$vtmp"
    write_vl_service
    c_ok "已安装 VictoriaLogs: $BIN_DIR/victoria-logs"
    return
  fi
  die "未找到 VictoriaLogs 安装来源（离线安装，不联网下载）。可用：
  1) 把 victoria-logs-linux-<arch>-*.tar.gz 放到离线包目录
  2) --vl-package <离线包目录内的文件名>
  3) --vl-binary <已解压的二进制路径>
  说明：离线包里的 VL 由 build/fetch-packages.sh 准备（可选组件，拉取失败不阻断发布）。"
}

start_vl() {
  if ! have_cmd systemctl; then
    c_warn "跳过 systemd 启动；可手动执行: $BIN_DIR/victoria-logs -storageDataPath=$DATA_DIR -httpListenAddr=$VL_LISTEN -retentionPeriod=$VL_RETENTION"
    return
  fi
  systemctl daemon-reload
  (( UPGRADE )) && systemctl stop "$SERVICE_NAME" 2>/dev/null
  systemctl enable "$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"
  sleep 2
}

# 健康检查打的是**平台真正要用的那个接口**（/select/logsql/query），
# 而不是只看进程活着：写入通、进程在，但查询接口不可用的话，集中日志页仍然是空的——
# 那才是这次安装要保证的事。
health_check() {
  c_info "健康检查"
  local port="${VL_LISTEN#:}"
  local base="http://127.0.0.1:${port}"
  if ! have_cmd curl; then
    c_warn "未检测到 curl，跳过健康检查"
    return
  fi
  local code; code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$base/health" 2>/dev/null || echo 000)"
  if [[ "$code" == "200" ]]; then
    c_ok "VictoriaLogs 存活检查通过 ($base/health -> 200)"
  else
    c_warn "存活检查 HTTP $code，请查看: journalctl -u ${SERVICE_NAME} -n 50"
    return
  fi
  local qcode; qcode="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$base/select/logsql/query?query=*&limit=1" 2>/dev/null || echo 000)"
  if [[ "$qcode" == "200" ]]; then
    c_ok "日志查询接口可用 ($base/select/logsql/query -> 200)"
  else
    c_warn "日志查询接口 HTTP $qcode（平台检索会失败）；请查看: journalctl -u ${SERVICE_NAME} -n 50"
  fi
}

summary() {
  VL_ADDR="http://127.0.0.1${VL_LISTEN}"
  echo
  echo "============================================================"
  echo " 集中日志后端安装完成"
  echo "------------------------------------------------------------"
  echo " 组件          : VictoriaLogs"
  echo " 日志查询地址  : $VL_ADDR"
  echo " 保留期        : $VL_RETENTION（由本后端负责，平台不再执行日志清理）"
  echo " 二进制        : $BIN_DIR/victoria-logs"
  echo " 数据目录      : $DATA_DIR"
  echo " systemd       : $SERVICE_NAME"
  echo " 查看状态      : systemctl status ${SERVICE_NAME%%.service}"
  echo "------------------------------------------------------------"
  echo " 接下来在 Server 机器执行（若分机部署，把地址换成日志库机 IP）："
  echo "   首次安装："
  echo "     sudo bash deploy/install-server.sh --yes --tsdb-addr <时序库> \\"
  echo "          --log-backend victorialogs --log-addr $VL_ADDR"
  echo "   已装好的 Server（改配置后重启）："
  echo "     sudo \$EDITOR /etc/monitor-server/server.yaml   # logBackend: victorialogs"
  echo "                                                    # logVictoriaLogs.addr: \"$VL_ADDR\""
  echo "     sudo systemctl restart monitor-server"
  echo "============================================================"
}

# ============================ 主流程 ============================
main() {
  detect_env
  preflight
  scan_vl_package
  install_vl_binary
  start_vl
  health_check
  summary
}

main "$@"
