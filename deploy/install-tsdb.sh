#!/usr/bin/env bash
#
# nebula-monitor 时序库安装脚本（独立部署，可与 Server 分机）
# ----------------------------------------------------------------------------
# 功能：
#   1. 安装 VictoriaMetrics（二进制 + systemd，离线包扫描；或 Docker）
#   2. 或用 Docker 拉起 Mimir / Cortex / Thanos
#   3. 启动并做健康检查，输出写入/查询地址（供 install-server.sh --tsdb-addr 使用）
#
# 既支持交互式，也支持非交互式（--yes）。
#
set -uo pipefail

# ============================ 默认参数 ============================
BACKEND="victoriametrics"
USE_DOCKER=""
VM_BINARY=""
VM_PKG=""
PKG_DIR=""
VM_LISTEN=":8428"
ASSUME_YES=0
UPGRADE=0

ARCH=""
OS=""

BIN_DIR="/usr/local/bin"
SERVICE_DIR="/etc/systemd/system"
DATA_DIR="/var/lib/victoria-metrics-data"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 运行时填充
TSDB_ADDR=""
TSDB_QUERY_ADDR=""

# ============================ 日志/工具 ============================
c_info()  { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()    { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn()  { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
c_err()   { printf '\033[31m[错误]\033[0m %s\n' "$*"; }
die()     { c_err "$*"; exit 1; }

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

choose() {
  local __q="$1" __def="$2"; shift 2
  local __opts=("$@") __i __n
  for __i in "${!__opts[@]}"; do
    printf '  %d) %s\n' $((__i+1)) "${__opts[$__i]}"
  done
  while true; do
    read -r -p "$__q [${__def}]: " __n || true
    __n="${__n:-$__def}"
    if [[ "$__n" =~ ^[0-9]+$ ]] && (( __n>=1 && __n<=${#__opts[@]} )); then
      CHOICE=$__n
      CHOICE_VAL="${__opts[$((__n-1))]}"
      return 0
    fi
    echo "请输入 1-${#__opts[@]} 之间的数字"
  done
}

# ============================ 参数解析 ============================
usage() {
  cat <<EOF
用法: $0 [选项]

  --backend <type>      时序库: victoriametrics(默认)|mimir|cortex|thanos
  --docker              用 Docker 拉起（VM 默认用二进制+systemd；mimir/cortex/thanos 自动 Docker）
  --vm-binary <path>    直接使用本地 victoria-metrics 二进制（不扫描 offline/）
  --vm-package <name>   指定 offline/ 内的 VM 压缩包文件名
  --packages <dir>      离线包目录（默认自动探测 ../dist/artifacts/packages 或 ../offline）
  --listen <addr>       VictoriaMetrics 监听地址（默认 :8428）
  --yes                 非交互式
  --upgrade              升级模式：强制覆盖二进制，保留数据，重启服务
  -h, --help            显示本帮助
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --backend)     BACKEND="$2"; shift 2 ;;
    --docker)      USE_DOCKER="yes"; shift ;;
    --vm-binary)   VM_BINARY="$2"; shift 2 ;;
    --vm-package)  VM_PKG="$2"; shift 2 ;;
    --packages)    PKG_DIR="$2"; shift 2 ;;
    --listen)      VM_LISTEN="$2"; shift 2 ;;
    --yes)         ASSUME_YES=1; shift ;;
    --upgrade)     UPGRADE=1; shift ;;
    -h|--help)     usage ;;
    *) die "未知参数: $1（用 -h 查看帮助）" ;;
  esac
done

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

# 将包名解析为绝对路径：支持绝对路径 / 相对路径 / offline 内文件名
resolve_pkg() {
  local name="$1" dir="$2"
  [[ -z "$name" ]] && return 1
  if [[ -f "$name" ]]; then printf '%s' "$name"; return 0; fi
  if [[ -n "$dir" && -f "$dir/$name" ]]; then printf '%s' "$dir/$name"; return 0; fi
  return 1
}

# 默认 VictoriaMetrics 包名（按 arch；内置当前 offline/ 中的版本）。
# 用户可用 --vm-package 覆盖，或自行下载更高版本放入 offline/ 后用 --vm-package 指定文件名。
default_vm_pkg() {
  case "$ARCH" in
    amd64) echo "victoria-metrics-linux-amd64-v1.148.0.tar.gz" ;;
    arm64) echo "victoria-metrics-linux-arm64-v1.148.0.tar.gz" ;;
    arm)   echo "victoria-metrics-linux-arm-v1.148.0.tar.gz" ;;
  esac
}

# 扫描 offline/：优先用默认包名，否则按 arch 匹配列出供确认；
# 支持用 --vm-package 直接指定包名（自行下载更高版本时）。
scan_vm_package() {
  [[ -n "$VM_BINARY" ]] && return 0
  [[ -n "$PKG_DIR" ]] || return 0
  # 已用 --vm-package 指定：解析为绝对路径
  if [[ -n "$VM_PKG" ]]; then
    local rp; rp="$(resolve_pkg "$VM_PKG" "$PKG_DIR")" || die "指定的 VictoriaMetrics 包不存在: $VM_PKG"
    VM_PKG="$rp"
    c_ok "将使用 VictoriaMetrics 包: $VM_PKG"
    return 0
  fi
  # 优先：默认包名
  local def; def="$(default_vm_pkg)"
  if [[ -n "$def" && -f "$PKG_DIR/$def" ]]; then
    if (( ASSUME_YES )); then
      VM_PKG="$def"
    else
      c_info "默认 VictoriaMetrics 包: $def"
      if confirm "是否使用默认 VictoriaMetrics 包？(n=扫描其它/手动指定)" "yes"; then
        VM_PKG="$def"
      fi
    fi
  fi
  # 回退：按 arch 扫描
  if [[ -z "$VM_PKG" ]]; then
    local found; found="$(ls "$PKG_DIR"/victoria-metrics-linux-"$ARCH"-*.tar.gz 2>/dev/null)"
    if [[ -n "$found" ]]; then
      echo "在 offline 中检测到以下 VictoriaMetrics 安装包 (arch=$ARCH):"
      echo "$found" | sed 's#.*/##' | cat -n
      if (( ASSUME_YES )) || confirm "是否使用上述 VictoriaMetrics 包？(n=手动指定/跳过)" "yes"; then
        VM_PKG="$(echo "$found" | head -1)"
      fi
    fi
    if [[ -z "$VM_PKG" ]] && ! (( ASSUME_YES )); then
      local inp=""
      prompt inp "请输入 offline 中的 VictoriaMetrics 包文件名（留空=跳过）" ""
      VM_PKG="$inp"
    fi
  fi
  if [[ -n "$VM_PKG" ]]; then
    local rp; rp="$(resolve_pkg "$VM_PKG" "$PKG_DIR")" || die "指定的 VictoriaMetrics 包不存在: $VM_PKG"
    VM_PKG="$rp"
    c_ok "将使用 VictoriaMetrics 包: $VM_PKG"
  fi
}

# 从解压目录挑选 VM 二进制（企业版含 -prod/-prod-fips，优先非 fips 的 -prod）
pick_vm_binary() {
  local d="$1" b=""
  [[ -f "$d/victoria-metrics-prod" ]] && b="$d/victoria-metrics-prod"
  [[ -z "$b" && -f "$d/victoria-metrics" ]] && b="$d/victoria-metrics"
  if [[ -z "$b" ]]; then
    b="$(ls "$d"/victoria-metrics* 2>/dev/null | grep -v -- '-fips$' | head -1)"
  fi
  [[ -z "$b" ]] && b="$(ls "$d"/victoria-metrics* 2>/dev/null | head -1)"
  printf '%s' "$b"
}

write_vm_service() {
  cat > "$SERVICE_DIR/victoriametrics.service" <<EOF
[Unit]
Description=VictoriaMetrics
After=network.target

[Service]
Type=simple
ExecStart=$BIN_DIR/victoria-metrics -storageDataPath=$DATA_DIR -httpListenAddr=$VM_LISTEN
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  c_ok "已写入 systemd 单元: $SERVICE_DIR/victoriametrics.service"
}

# 安装 VM 二进制（离线，不联网下载）
install_vm_binary() {
  if [[ -x "$BIN_DIR/victoria-metrics" ]] && (( ! UPGRADE )); then
    c_ok "已检测到 VictoriaMetrics: $BIN_DIR/victoria-metrics，跳过安装（升级请用 --upgrade）"
    [[ -f "$SERVICE_DIR/victoriametrics.service" ]] || write_vm_service
    return
  fi
  if [[ -n "$VM_BINARY" ]]; then
    [[ -f "$VM_BINARY" ]] || die "指定的 VictoriaMetrics 二进制不存在: $VM_BINARY"
    install -m 0755 "$VM_BINARY" "$BIN_DIR/victoria-metrics" || die "安装 victoria-metrics 失败"
    write_vm_service
    c_ok "已安装本地 VictoriaMetrics: $BIN_DIR/victoria-metrics"
    return
  fi
  if [[ -n "$VM_PKG" ]]; then
    c_info "从本地包解压并安装 VictoriaMetrics: $VM_PKG"
    local vtmp; vtmp="$(mktemp -d)"
    tar -xzf "$VM_PKG" -C "$vtmp" || die "解压 VictoriaMetrics 包失败: $VM_PKG"
    local vbin; vbin="$(pick_vm_binary "$vtmp")"
    [[ -n "$vbin" ]] || die "包内未找到 victoria-metrics* 二进制"
    install -m 0755 "$vbin" "$BIN_DIR/victoria-metrics" || die "安装 victoria-metrics 失败"
    rm -rf "$vtmp"
    write_vm_service
    c_ok "已安装 VictoriaMetrics: $BIN_DIR/victoria-metrics"
    return
  fi
  die "未找到 VictoriaMetrics 安装来源（离线安装，不联网下载）。可用：
  1) 把 victoria-metrics-linux-<arch>-*.tar.gz 放到 offline/
  2) --vm-package <offline 内的文件名>
  3) --vm-binary <已解压的二进制路径>"
}

# Docker 拉起各后端；设置 TSDB_ADDR / TSDB_QUERY_ADDR
install_tsdb_docker() {
  local backend="$1"
  local port_write port_query image cmd_extra="" mount=""
  case "$backend" in
    victoriametrics)
      port_write="${VM_LISTEN#:}"; port_query="$port_write"
      image="victoriametrics/victoria-metrics:latest"
      cmd_extra='["-storageDataPath=/victoria-metrics-data","-httpListenAddr=:'"$port_write"'"]'
      ;;
    mimir)
      port_write=8080; port_query=8080
      image="grafana/mimir:latest"
      mount='      - ./mimir.yaml:/etc/mimir.yaml:ro\n'
      cmd_extra='["-target=all","-config.file=/etc/mimir.yaml"]'
      write_mimir_config
      ;;
    cortex)
      port_write=9009; port_query=8080
      image="cortexproject/cortex:latest"
      mount='      - ./cortex.yaml:/etc/cortex.yaml:ro\n'
      cmd_extra='["-target=all","-config.file=/etc/cortex.yaml"]'
      write_cortex_config
      ;;
    thanos)
      port_write=19291; port_query=9090
      write_thanos_compose
      TSDB_ADDR="http://127.0.0.1:$port_write"
      TSDB_QUERY_ADDR="http://127.0.0.1:$port_query"
      ;;
    *) die "Docker 方式暂不支持后端: $backend" ;;
  esac

  if [[ "$backend" != "thanos" ]]; then
    cat > docker-compose.tsdb.yml <<EOF
services:
  tsdb:
    image: $image
    container_name: monitor-tsdb
    restart: unless-stopped
    command: $cmd_extra
    ports:
      - "$port_write:$port_write"
      - "$port_query:$port_query"
    volumes:
      - tsdb-data:/var/lib/tsdb
$mount
volumes:
  tsdb-data:
EOF
    TSDB_ADDR="http://127.0.0.1:$port_write"
    [[ "$port_query" != "$port_write" ]] && TSDB_QUERY_ADDR="http://127.0.0.1:$port_query"
  fi

  c_ok "已生成 docker-compose.tsdb.yml (后端: $backend)"
  if have_cmd docker; then
    if (( ASSUME_YES )) || confirm "是否现在启动该时序库容器？" "yes"; then
      docker compose -f docker-compose.tsdb.yml up -d || c_warn "Docker 启动失败，请手动检查 docker-compose.tsdb.yml"
    fi
  else
    c_warn "未检测到 docker，请手动执行: docker compose -f docker-compose.tsdb.yml up -d"
  fi
}

write_mimir_config() {
  cat > mimir.yaml <<'EOF'
# Mimir all-in-one 最小化演示配置（生产请参考官方文档加固）
memberlist:
  cluster_label: monitor-mimir
target: all
EOF
}

write_cortex_config() {
  cat > cortex.yaml <<'EOF'
# Cortex all-in-one 最小化演示配置（生产请参考官方文档加固）
target: all
ingester:
  chunk_idle_period: 5m
  chunk_retain_period: 30s
storage:
  tsdb:
    dir: /var/lib/tsdb/cortex
EOF
}

write_thanos_compose() {
  cat > docker-compose.tsdb.yml <<'EOF'
services:
  thanos-receive:
    image: quay.io/thanos/thanos:latest
    container_name: monitor-thanos-receive
    restart: unless-stopped
    command:
      - receive
      - --grpc-address=0.0.0.0:10901
      - --http-address=0.0.0.0:19291
      - --remote-write.address=0.0.0.0:19291
      - --tsdb.path=/thanos-receive-data
      - --receive.replication-factor=1
    ports:
      - "19291:19291"
      - "10901:10901"
    volumes:
      - thanos-receive-data:/thanos-receive-data
  thanos-query:
    image: quay.io/thanos/thanos:latest
    container_name: monitor-thanos-query
    restart: unless-stopped
    command:
      - query
      - --http-address=0.0.0.0:9090
      - --store=thanos-receive:10901
    ports:
      - "9090:9090"
    depends_on:
      - thanos-receive
volumes:
  thanos-receive-data:
EOF
}

start_vm() {
  if ! have_cmd systemctl; then
    c_warn "跳过 systemd 启动；可手动执行: $BIN_DIR/victoria-metrics -storageDataPath=$DATA_DIR -httpListenAddr=$VM_LISTEN"
    return
  fi
  systemctl daemon-reload
  (( UPGRADE )) && systemctl stop victoriametrics.service 2>/dev/null
  systemctl enable victoriametrics.service
  systemctl restart victoriametrics.service
  sleep 2
}

health_check() {
  c_info "健康检查"
  local port="${VM_LISTEN#:}"
  local url="http://127.0.0.1:${port}/health"
  if have_cmd curl; then
    local code; code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$url" 2>/dev/null || echo 000)"
    if [[ "$code" == "200" ]]; then
      c_ok "VictoriaMetrics 健康检查通过 ($url -> 200)"
    else
      c_warn "健康检查 HTTP $code，请查看: journalctl -u victoriametrics -n 50"
    fi
  fi
}

summary() {
  echo
  echo "============================================================"
  echo " 时序库安装完成"
  echo "------------------------------------------------------------"
  echo " 后端          : $BACKEND"
  echo " 写入地址      : $TSDB_ADDR"
  [[ -n "$TSDB_QUERY_ADDR" ]] && echo " 查询地址      : $TSDB_QUERY_ADDR"
  if [[ "$USE_DOCKER" != "yes" && "$BACKEND" == "victoriametrics" ]]; then
    echo " 二进制        : $BIN_DIR/victoria-metrics"
    echo " 数据目录      : $DATA_DIR"
    echo " systemd       : victoriametrics.service"
    echo " 查看状态      : systemctl status victoriametrics"
  fi
  echo "------------------------------------------------------------"
  echo " 接下来在 Server 机器运行（若分机部署，把地址换成时序库机 IP）："
  echo "   sudo bash deploy/install-server.sh --yes --tsdb-addr $TSDB_ADDR"
  echo "============================================================"
}

# ============================ 主流程 ============================
main() {
  detect_env
  preflight

  # 非 VM 后端只能走 Docker
  if [[ "$BACKEND" != "victoriametrics" ]]; then
    USE_DOCKER="yes"
  fi

  if [[ "$USE_DOCKER" == "yes" ]]; then
    install_tsdb_docker "$BACKEND"
  else
    scan_vm_package
    install_vm_binary
    start_vm
    TSDB_ADDR="http://127.0.0.1${VM_LISTEN}"
    health_check
  fi
  summary
}

main "$@"
