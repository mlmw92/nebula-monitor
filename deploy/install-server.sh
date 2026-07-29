#!/usr/bin/env bash
#
# nebula-monitor Server 交互式安装脚本（分步引导）
# ----------------------------------------------------------------------------
# 功能：
#   1. 预检环境（root / 架构 / systemd）
#   2. 对接时序库（输入已有实例地址；如需本机安装时序库请先运行 deploy/install-tsdb.sh）
#   3. 配置监听地址、告警通知
#   4. 获取并安装 Server 二进制、生成配置与 systemd 单元
#   5. 启动服务并做健康检查，输出后续步骤
#
# 既支持交互式分步引导，也支持非交互式（--yes 使用默认值，或显式传参）。
#
set -uo pipefail

# ============================ 默认参数 ============================
MODE=""
LISTEN=""
TSDB_BACKEND=""
TSDB_ADDR=""
TSDB_QUERY_ADDR=""
DIST_DIR=""
NODE_PKG=""             # 可选：offline 中的 Node 压缩包（或 --node-package 指定文件名）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_DIR=""               # 可选：本地离线包目录（含 node / victoria-metrics tarball）；默认自动探测 ./offline
DATA_DIR="/var/lib/monitor-server"
CONFIG_DIR="/etc/monitor-server"
WEB_DIR="/etc/monitor-server/web"
BIN_DIR="/usr/local/bin"
SERVICE_DIR="/etc/systemd/system"
ALERT_WEBHOOK=""
ENABLE_AGENT_AUTH=""    # yes/no：是否启用 Agent 接入授权密钥（未显式指定时默认 yes）
AGENT_SECRET=""         # 授权密钥（启用时生成或显式传入）
AGENT_AUTH_EXPLICIT=0   # 是否由命令行显式指定（--agent-auth/--agent-secret），升级时据此决定是否更新配置
AGENT_BIN_DIR="./dist"  # Agent 二进制分发根目录（自带 CDN），由 stage_agent_dist 覆盖
AGENT_SCRIPT_PATH="./deploy/agent-install.sh"  # 安装脚本路径
ASSUME_YES=0
UPGRADE=0

ARCH=""
OS=""

# ============================ 日志/工具 ============================
c_info()  { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()    { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn()  { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
c_err()   { printf '\033[31m[错误]\033[0m %s\n' "$*"; }
die()     { c_err "$*"; exit 1; }

have_cmd() { command -v "$1" >/dev/null 2>&1; }

# 交互：读取一行，缺省取 $2
prompt() {
  local __var="$1" __q="$2" __def="$3"
  local __val
  read -r -p "$__q [$__def]: " __val || true
  __val="${__val:-$__def}"
  printf -v "$__var" '%s' "$__val"
}

# 交互：确认 y/n，缺省取 $2；返回 0=yes 1=no
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

# 交互：菜单选择；参数：提示 默认序号 选项1 选项2 ...
# 结果写入全局 CHOICE(序号) / CHOICE_VAL(文本)
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

  --listen <addr>               HTTP 监听地址，如 :8080
  --tsdb-backend <type>         时序库后端: victoriametrics|mimir|cortex|thanos|prometheus|custom
  --tsdb-addr <url>             时序库地址，如 http://10.0.0.10:8428
  --tsdb-query-addr <url>       可选：查询基址（与写入端口不同时）
  --dist <dir>                  本地已构建产物目录（含 server 二进制；默认探测 dist/artifacts/bin)
  --packages <dir>              本地离线包目录（默认自动探测 dist/artifacts/packages 或 offline，含 node tarball）
  --node-package <name>         指定离线包内的 Node 压缩包文件名（如 node-v24.18.0-linux-x64.tar.xz）
  --alert-webhook <urls>        告警 webhook 地址（逗号分隔，可选）
  --agent-auth                  启用 Agent 接入授权密钥
  --agent-secret <key>          指定授权密钥（与 --agent-auth 配合；缺省则自动生成）
  --yes                         非交互式；未提供二进制来源且处于源码目录时自动从源码构建
  --upgrade                      升级模式：强制覆盖二进制，保留已有配置（备份为 .bak），重启服务
  -h, --help                    显示本帮助

时序库说明：
  本脚本仅对接已有时序库（通过 --tsdb-addr 指定）。
  如需在本机安装时序库，请先运行：sudo bash deploy/install-tsdb.sh
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --listen)          LISTEN="$2"; shift 2 ;;
    --tsdb-backend)    TSDB_BACKEND="$2"; shift 2 ;;
    --tsdb-addr)       TSDB_ADDR="$2"; shift 2 ;;
    --tsdb-query-addr) TSDB_QUERY_ADDR="$2"; shift 2 ;;
    --dist)            DIST_DIR="$2"; shift 2 ;;
    --packages)        PKG_DIR="$2"; shift 2 ;;
    --node-package)    NODE_PKG="$2"; shift 2 ;;
    --alert-webhook)   ALERT_WEBHOOK="$2"; shift 2 ;;
    --agent-auth)      ENABLE_AGENT_AUTH="yes"; AGENT_AUTH_EXPLICIT=1; shift ;;
    --agent-secret)    AGENT_SECRET="$2"; ENABLE_AGENT_AUTH="yes"; AGENT_AUTH_EXPLICIT=1; shift 2 ;;
    --yes)             ASSUME_YES=1; shift ;;
    --upgrade)         UPGRADE=1; shift ;;
    -h|--help)         usage ;;
    *) die "未知参数: $1（用 -h 查看帮助）" ;;
  esac
done

# 升级模式：非交互 + 从已有配置复用 tsdb / agentAuth 设置（避免重新询问、误报）
if (( UPGRADE )); then
  ASSUME_YES=1
  if [[ -z "$TSDB_ADDR" && -f "$CONFIG_DIR/server.yaml" ]]; then
    TSDB_ADDR="$(grep -E '^[[:space:]]*addr:' "$CONFIG_DIR/server.yaml" | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
    TSDB_BACKEND="$(grep -E '^[[:space:]]*backend:' "$CONFIG_DIR/server.yaml" | head -1 | awk '{print $2}')"
    [[ -n "$TSDB_ADDR" ]] && c_info "升级：从现有配置复用 tsdb=$TSDB_BACKEND @ $TSDB_ADDR"
  fi
  # Agent 授权状态由 step_agent_auth 统一处理（升级/重装均沿用现有配置，显式 --agent-auth 时更新）
fi

# 自动探测本地离线包目录（dist/artifacts/packages 优先；旧 offline 兼容）
if [[ -z "$PKG_DIR" ]]; then
  if [[ -d "$SCRIPT_DIR/../dist/artifacts/packages" ]]; then PKG_DIR="$SCRIPT_DIR/../dist/artifacts/packages"
  elif [[ -d "$SCRIPT_DIR/../offline" ]]; then PKG_DIR="$SCRIPT_DIR/../offline"
  elif [[ -d ./dist/artifacts/packages ]]; then PKG_DIR="./dist/artifacts/packages"
  elif [[ -d ./offline ]]; then PKG_DIR="./offline"
  fi
fi
[[ -n "$PKG_DIR" ]] && c_info "检测到本地离线包目录: $PKG_DIR"

# 自动探测预编译二进制目录（为 --dist 兜底，--dist 显式传值则跳过）
#   优先级：dist/artifacts/bin（新结构） > offline/bin（旧结构，兼容）
if [[ -z "$DIST_DIR" ]]; then
  if [[ -d "$SCRIPT_DIR/../dist/artifacts/bin" ]]; then DIST_DIR="$SCRIPT_DIR/../dist/artifacts/bin"
  elif [[ -d "$SCRIPT_DIR/../offline/bin" ]]; then DIST_DIR="$SCRIPT_DIR/../offline/bin"
  elif [[ -d ./dist/artifacts/bin ]]; then DIST_DIR="./dist/artifacts/bin"
  elif [[ -d ./offline/bin ]]; then DIST_DIR="./offline/bin"
  fi
  [[ -n "$DIST_DIR" ]] && c_info "自动检测到 --dist: $DIST_DIR"
fi

# ============================ 检测 ============================
detect_env() {
  OS="$(uname -s)"
  local m
  m="$(uname -m)"
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
    c_warn "未检测到 systemctl，将跳过 systemd 单元安装（可手动用 nohup 运行）"
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

# 默认 Node 包名（按 arch；内置当前 offline/ 中的版本）。
# 用户可用 --node-package 覆盖，或自行下载更高版本放入 offline/ 后用 --node-package 指定文件名。
default_node_pkg() {
  case "$ARCH" in
    amd64) echo "node-v24.18.0-linux-x64.tar.xz" ;;
    arm64) echo "node-v24.18.0-linux-arm64.tar.xz" ;;
    arm)   echo "node-v24.18.0-linux-armv7l.tar.xz" ;;
  esac
}

# 扫描 offline/：优先用默认包名，否则按 arch 匹配列出供确认；
# 支持用 --node-package 直接指定包名（自行下载更高版本时）。
scan_packages() {
  [[ -n "$PKG_DIR" ]] && { [[ -d "$PKG_DIR" ]] || die "指定的离线包目录不存在: $PKG_DIR"; }

  # ---------- Node ----------
  if [[ -z "$NODE_PKG" ]] && [[ -n "$PKG_DIR" ]]; then
    # 优先：默认包名
    local def; def="$(default_node_pkg)"
    if [[ -n "$def" && -f "$PKG_DIR/$def" ]]; then
      if (( ASSUME_YES )); then
        NODE_PKG="$def"
      else
        c_info "默认 Node 包: $def"
        if confirm "是否使用默认 Node 包？(n=扫描其它/手动指定)" "yes"; then
          NODE_PKG="$def"
        fi
      fi
    fi
    # 回退：按 arch 扫描（node 的 amd64 包名为 linux-x64）
    if [[ -z "$NODE_PKG" ]]; then
      local narch="$ARCH"; [[ "$ARCH" == "amd64" ]] && narch="x64"
      local found; found="$(ls "$PKG_DIR"/node-v*-linux-"$narch".tar.xz 2>/dev/null)"
      if [[ -n "$found" ]]; then
        echo "在 offline 中检测到以下 Node 安装包 (arch=$ARCH):"
        echo "$found" | sed 's#.*/##' | cat -n
        if (( ASSUME_YES )) || confirm "是否使用上述 Node 包？(n=手动指定/跳过)" "yes"; then
          NODE_PKG="$(echo "$found" | head -1)"
        fi
      fi
      if [[ -z "$NODE_PKG" ]] && ! (( ASSUME_YES )); then
        local inp=""
        prompt inp "请输入 offline 中的 Node 包文件名（留空=跳过/使用系统 Node）" ""
        NODE_PKG="$inp"
      fi
    fi
  fi
  if [[ -n "$NODE_PKG" ]]; then
    local rp; rp="$(resolve_pkg "$NODE_PKG" "$PKG_DIR")" || die "指定的 Node 包不存在: $NODE_PKG"
    NODE_PKG="$rp"
    c_ok "将使用 Node 包: $NODE_PKG"
  fi
}

# 扫描"产物/离线"目录：存放提前编译好的 server / agent 二进制（完全离线，服务器无需 Go）
# 约定布局（与 build/cross-compile.sh 输出一致，可直接作为 --dist）：
#   dist/artifacts/bin/server/linux/<arch>/server   (新结构)
#   dist/artifacts/bin/agent/linux/<arch>/agent
#   offline/bin/server/linux/<arch>/server          (旧结构, 兼容)
#   offline/bin/agent/linux/<arch>/agent
scan_prebuilt() {
  [[ -n "$DIST_DIR" ]] && return 0          # 已用 --dist 显式指定
  # 探测顺序：dist/artifacts/bin（新） > $PKG_DIR/bin (旧)
  local candidates=()
  if [[ -d "$SCRIPT_DIR/../dist/artifacts/bin" ]]; then candidates+=("$SCRIPT_DIR/../dist/artifacts/bin"); fi
  if [[ -n "$PKG_DIR" && -d "$PKG_DIR/bin" ]]; then candidates+=("$PKG_DIR/bin"); fi
  if [[ -d ./dist/artifacts/bin ]]; then candidates+=("./dist/artifacts/bin"); fi
  if [[ -d ./offline/bin ]]; then candidates+=("./offline/bin"); fi
  local pdir=""
  for c in "${candidates[@]}"; do
    if [[ -f "$c/server/linux/$ARCH/server" ]]; then
      pdir="$c"; break
    fi
  done
  [[ -z "$pdir" ]] && return 0
  if (( ASSUME_YES )); then
    DIST_DIR="$pdir"
  else
    if confirm "检测到预编译二进制 (位于 $(basename "$pdir")), 是否使用？(n=改用源码构建/指定 --dist)" "yes"; then
      DIST_DIR="$pdir"
    fi
  fi
  [[ -n "$DIST_DIR" ]] && c_ok "将使用预编译二进制目录: $DIST_DIR"
}

# ============================ 交互步骤 ============================
step_mode() {
  MODE="standalone"
  c_ok "部署模式: $MODE (单机)"
}

step_tsdb() {
  c_info "时序库配置（对接已有实例；如需本机安装时序库请先运行 deploy/install-tsdb.sh）"

  if [[ -z "$TSDB_BACKEND" ]]; then
    if (( ASSUME_YES )); then
      TSDB_BACKEND="victoriametrics"
    else
      echo "请选择时序库后端类型（均兼容 Prometheus remote_write + PromQL）:"
      choose "后端类型" 1 \
        "victoriametrics (VictoriaMetrics)" \
        "mimir (Grafana Mimir)" \
        "cortex (Cortex)" \
        "thanos (Thanos Receive)" \
        "prometheus (经 remote_write receiver)" \
        "custom (手动指定写入/查询路径)"
      TSDB_BACKEND="$CHOICE_VAL"
      TSDB_BACKEND="${TSDB_BACKEND%% *}"
    fi
  fi

  if [[ -z "$TSDB_ADDR" ]]; then
    if (( ASSUME_YES )); then
      TSDB_ADDR="http://127.0.0.1:8428"
    else
      prompt TSDB_ADDR "请输入时序库写入基址(HTTP URL，如 http://10.0.0.10:8428)" "http://127.0.0.1:8428"
    fi
  fi

  # Thanos / Cortex 查询端口通常与写入不同
  if [[ "$TSDB_BACKEND" == "thanos" || "$TSDB_BACKEND" == "cortex" ]]; then
    if [[ -z "$TSDB_QUERY_ADDR" ]]; then
      if (( ASSUME_YES )); then
        [[ "$TSDB_BACKEND" == "thanos" ]] && TSDB_QUERY_ADDR="http://127.0.0.1:9090" || TSDB_QUERY_ADDR="http://127.0.0.1:8080"
      else
        prompt TSDB_QUERY_ADDR "该后端查询端口通常与写入不同，请输入查询基址(留空则同写入)" ""
      fi
    fi
  fi

  # custom：收集写入/查询路径
  if [[ "$TSDB_BACKEND" == "custom" ]]; then
    if [[ -z "$TSDB_WRITE_PATH" ]]; then prompt TSDB_WRITE_PATH "请输入 remote_write 写入路径(如 /api/v1/write)" "/api/v1/write"; fi
    if [[ -z "$TSDB_QUERY_PATH" ]]; then prompt TSDB_QUERY_PATH "请输入 PromQL 查询路径(默认 /api/v1/query)" "/api/v1/query"; fi
    if [[ -z "$TSDB_RANGE_PATH" ]];  then prompt TSDB_RANGE_PATH  "请输入区间查询路径(默认 /api/v1/query_range)" "/api/v1/query_range"; fi
  fi

  c_ok "时序库后端: $TSDB_BACKEND @ $TSDB_ADDR${TSDB_QUERY_ADDR:+ (查询 $TSDB_QUERY_ADDR)}"
}

step_network() {
  if [[ -z "$LISTEN" ]]; then
    if (( ASSUME_YES )); then LISTEN=":8080"; else
      prompt LISTEN "Server HTTP 监听地址(Agent 上报与前端均走此端口)" ":8080"
    fi
  fi
  c_ok "监听地址: $LISTEN"
}

step_alert() {
  if [[ -z "$ALERT_WEBHOOK" ]]; then
    if (( ASSUME_YES )); then ALERT_WEBHOOK=""; else
      if confirm "是否配置告警 webhook 通知？(留空则暂不使用)" "no"; then
        prompt ALERT_WEBHOOK "请输入 webhook 地址(多个用逗号分隔)" ""
      fi
    fi
  fi
  [[ -n "$ALERT_WEBHOOK" ]] && c_ok "告警 webhook: $ALERT_WEBHOOK" || c_ok "告警 webhook: 未配置"
}

# 生成随机授权密钥（hex）
gen_agent_secret() {
  if have_cmd openssl; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

# 生成强登录口令（仅字母数字，避免 YAML/终端歧义；长度 24，约 140 bit 熵）
gen_password() {
  if have_cmd openssl; then
    openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 24
  else
    tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 24
  fi
}

# 升级模式：从已有 server.yaml 读取 agentAuth 真实状态（避免覆盖后误报"未启用"）
# 仅当调用方未显式通过 --agent-auth/--agent-secret 指定时才以配置文件为准。
read_existing_agent_auth() {
  local cfg="$CONFIG_DIR/server.yaml"
  [[ -f "$cfg" ]] || return 0
  # 截取 agentAuth: 到下一个顶层键之间的块
  local block
  block="$(awk '/^[[:space:]]*agentAuth:/{f=1;next} f&&/^[^[:space:]]/{f=0} f' "$cfg")"
  [[ -n "$block" ]] || return 0
  local en sec
  en="$(echo "$block" | grep -E '^[[:space:]]*enabled:' | head -1 | awk '{print $2}')"
  sec="$(echo "$block" | grep -E '^[[:space:]]*secret:' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
  if [[ "$en" == "true" || "$en" == "True" || "$en" == "TRUE" || "$en" == "yes" || "$en" == "Yes" || "$en" == "YES" || "$en" == "on" ]]; then
    ENABLE_AGENT_AUTH="yes"
    [[ -n "$sec" ]] && AGENT_SECRET="$sec"
  else
    ENABLE_AGENT_AUTH="no"
  fi
}

# 升级模式下显式 --agent-auth/--agent-secret 时，就地更新已有 server.yaml 的 agentAuth 块
# （升级模式默认保留原配置不覆盖；仅当用户显式指定授权时才更新这一块，其余配置不动）。
patch_agent_auth_config() {
  local cfg="$CONFIG_DIR/server.yaml"
  [[ -f "$cfg" ]] || return 0
  local en sec
  en="$([[ "$ENABLE_AGENT_AUTH" == "yes" ]] && echo true || echo false)"
  sec="$AGENT_SECRET"
  local new_block
  new_block="$(printf 'agentAuth:\n  enabled: %s\n  secret: "%s"' "$en" "$sec")"
  # 用 awk 替换已存在的 agentAuth 块；若不存在则原样输出（随后追加）
  awk -v new="$new_block" '
    /^[[:space:]]*agentAuth:[[:space:]]*$/ {f=1; print new; next}
    f && /^[^[:space:]]/ {f=0}
    f {next}
    {print}
  ' "$cfg" > "$cfg.tmp" && mv "$cfg.tmp" "$cfg"
  # 文件中若无 agentAuth 块（awk 未写入 new），则追加
  if ! grep -qE '^[[:space:]]*agentAuth:' "$cfg"; then
    printf '\n%s\n' "$new_block" >> "$cfg"
  fi
}

step_agent_auth() {
  c_info "Agent 接入授权"
  # 未显式指定时，优先沿用已有配置（避免重新安装/升级时误重置 agentAuth）
  if [[ -z "$ENABLE_AGENT_AUTH" && -f "$CONFIG_DIR/server.yaml" ]]; then
    read_existing_agent_auth
  fi
  # 显式 --agent-auth 但未给密钥：从现有配置取密钥（无则后续自动生成）
  if [[ "$ENABLE_AGENT_AUTH" == "yes" && -z "$AGENT_SECRET" && -f "$CONFIG_DIR/server.yaml" ]]; then
    AGENT_SECRET="$(awk '/^[[:space:]]*agentAuth:/{f=1;next} f&&/^[^[:space:]]/{f=0} f' "$CONFIG_DIR/server.yaml" | grep -E '^[[:space:]]*secret:' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
  fi
  if [[ -z "$ENABLE_AGENT_AUTH" ]]; then
    if (( ASSUME_YES )); then ENABLE_AGENT_AUTH="yes"; else
      if confirm "是否启用 Agent 接入授权密钥（启用后 Agent 必须携带密钥，防止非法节点上报）？" "yes"; then
        ENABLE_AGENT_AUTH="yes"
      else
        ENABLE_AGENT_AUTH="no"
      fi
    fi
  fi
  if [[ "$ENABLE_AGENT_AUTH" == "yes" ]]; then
    if [[ -z "$AGENT_SECRET" ]]; then
      AGENT_SECRET="$(gen_agent_secret)"
    fi
    c_ok "Agent 授权密钥: 已生成/指定（将写入 server.yaml 与安装命令）"
  else
    AGENT_SECRET=""
    c_ok "Agent 授权密钥: 未启用（任意 Agent 可上报）"
  fi
}

# 登录认证（Web 仪表盘访问控制）
ENABLE_AUTH=""      # yes/no
AUTH_USERNAME="admin"
AUTH_PASSWORD=""
AUTH_SECRET=""

# 从已有 server.yaml 读取登录认证配置（升级/重装时沿用，避免误改口令）
read_existing_auth() {
  local cfg="$CONFIG_DIR/server.yaml"
  [[ -f "$cfg" ]] || return 0
  local block
  block="$(awk '/^[[:space:]]*auth:/{f=1;next} f&&/^[^[:space:]]/{f=0} f' "$cfg")"
  [[ -n "$block" ]] || return 0
  local en u p s
  en="$(echo "$block" | grep -E '^[[:space:]]*enabled:' | head -1 | awk '{print $2}')"
  u="$(echo "$block" | grep -E '^[[:space:]]*username:' | head -1 | sed -E 's/.*"([^"]*)".*/\1/')"
  p="$(echo "$block" | grep -E '^[[:space:]]*password:' | head -1 | sed -E 's/.*"([^"]*)".*/\1/')"
  s="$(echo "$block" | grep -E '^[[:space:]]*secret:' | head -1 | sed -E 's/.*"([^"]*)".*/\1/')"
  if [[ "$en" == "true" || "$en" == "True" || "$en" == "TRUE" || "$en" == "yes" || "$en" == "Yes" || "$en" == "YES" || "$en" == "on" ]]; then
    ENABLE_AUTH="yes"
  else
    ENABLE_AUTH="no"
  fi
  [[ -n "$u" ]] && AUTH_USERNAME="$u"
  [[ -n "$p" ]] && AUTH_PASSWORD="$p"
  [[ -n "$s" ]] && AUTH_SECRET="$s"
}

step_auth() {
  c_info "登录认证（Web 仪表盘访问控制）"
  # 升级/重装：沿用已有配置（口令/密钥保持不变），避免误改导致无法登录
  if (( UPGRADE )) && [[ -f "$CONFIG_DIR/server.yaml" ]]; then
    read_existing_auth
    if [[ "$ENABLE_AUTH" == "yes" ]]; then
      c_ok "登录认证: 已启用（沿用现有配置：用户 $AUTH_USERNAME）"
    else
      c_ok "登录认证: 未启用（沿用现有配置）"
    fi
    return
  fi
  if [[ -z "$ENABLE_AUTH" ]]; then
    if (( ASSUME_YES )); then ENABLE_AUTH="yes"; else
      if confirm "是否启用 Web 登录认证（启用后访问仪表盘需登录）？" "yes"; then
        ENABLE_AUTH="yes"
      else
        ENABLE_AUTH="no"
      fi
    fi
  fi
  if [[ "$ENABLE_AUTH" == "yes" ]]; then
    if (( ASSUME_YES )); then
      # 非交互：admin 用户名 + 自动生成强口令（杜绝默认 admin/admin）
      AUTH_USERNAME="${AUTH_USERNAME:-admin}"
      [[ -n "$AUTH_PASSWORD" ]] || AUTH_PASSWORD="$(gen_password)"
    else
      prompt AUTH_USERNAME "登录用户名" "${AUTH_USERNAME:-admin}"
      prompt AUTH_PASSWORD "登录密码（留空=自动生成 24 位强口令）" ""
      [[ -n "$AUTH_PASSWORD" ]] || AUTH_PASSWORD="$(gen_password)"
    fi
    AUTH_SECRET="$(gen_agent_secret)"
    c_ok "登录认证: 已启用"
    c_info "  用户名: $AUTH_USERNAME"
    c_info "  密码:   $AUTH_PASSWORD   （请妥善保存，仅本次安装显示）"
  else
    c_ok "登录认证: 未启用（任意访问）"
  fi
}

# 部署前端静态资源到磁盘（Server 从 webDir 读取，改前端只需替换文件+重启，无需重编二进制）
# 探测顺序：
#   web/dist                                  (源码树本地)
#   $DIST_DIR/web/dist                        (旧结构)
#   $DIST_DIR/../web                          (新结构: dist/artifacts/bin → ../web = dist/artifacts/web)
#   $DIST_DIR/web                             (全量包 flat 结构: install.sh --dist $HERE)
#   dist/artifacts/web (相对脚本/当前)          (fallback)
stage_web_dist() {
  c_info "部署前端资源到 $WEB_DIR"
  local src=""
  if [[ -d web/dist ]]; then src="web/dist"
  elif [[ -n "$DIST_DIR" && -d "$DIST_DIR/web/dist" ]]; then src="$DIST_DIR/web/dist"
  elif [[ -n "$DIST_DIR" && -d "$DIST_DIR/../web" ]]; then src="$DIST_DIR/../web"
  elif [[ -n "$DIST_DIR" && -d "$DIST_DIR/web" ]]; then src="$DIST_DIR/web"
  elif [[ -d "$SCRIPT_DIR/../dist/artifacts/web" ]]; then src="$SCRIPT_DIR/../dist/artifacts/web"
  elif [[ -d ./dist/artifacts/web ]]; then src="./dist/artifacts/web"
  fi
  if [[ -z "$src" ]]; then
    c_warn "未找到 web/dist，前端将不可用（请把 web/dist 拷到 $WEB_DIR 后重启）"
    return
  fi
  mkdir -p "$WEB_DIR"
  rm -rf "$WEB_DIR"/*
  cp -a "$src"/. "$WEB_DIR"/
  c_ok "前端资源已部署: $WEB_DIR（$(du -sh "$WEB_DIR" | cut -f1)）"
}

# 暂存 Agent 二进制与安装脚本到本地目录，供 Server 自带 CDN 分发
stage_agent_dist() {
  c_info "暂存 Agent 分发文件（Server 自带 CDN）"
  local stage="$DATA_DIR/agent-dist"
  mkdir -p "$stage/agent/linux"

  # 升级模式：保留已分发的 Agent 二进制与脚本，避免影响已接入节点
  if (( UPGRADE )); then
    c_ok "升级模式：保留 Agent 分发文件（agent-dist 不覆盖）"
    AGENT_BIN_DIR="$stage/agent"
    [[ -f "$stage/agent-install.sh" ]] && AGENT_SCRIPT_PATH="$stage/agent-install.sh"
    return
  fi

  # 全新安装/重装：始终用本次包内最新版覆盖脚本与二进制。
  # 修复：旧版卸载未 --purge 时 dataDir/agent-dist 残留旧文件，原逻辑会整体跳过，
  # 导致重装后 /install/agent-install.sh 及 CDN 分发的 agent 仍是旧版本。

  # 安装脚本
  local script_src=""
  if [[ -n "$DIST_DIR" && -f "$DIST_DIR/agent-install.sh" ]]; then script_src="$DIST_DIR/agent-install.sh"
  elif [[ -f "./deploy/agent-install.sh" ]]; then script_src="./deploy/agent-install.sh"
  fi
  if [[ -n "$script_src" ]]; then
    cp "$script_src" "$stage/agent-install.sh" && c_ok "安装脚本已暂存: $stage/agent-install.sh"
  else
    c_warn "未找到 agent-install.sh，/install/agent-install.sh 将不可用（可手动放置到 $stage/）"
  fi

  # Agent 二进制（仅来自本地 --dist 或源码构建，不联网下载）
  local staged=0
  if [[ -n "$DIST_DIR" ]]; then
    local a
    for a in amd64 arm64 arm; do
      if [[ -f "$DIST_DIR/agent/linux/$a/agent" ]]; then
        mkdir -p "$stage/agent/linux/$a"
        cp "$DIST_DIR/agent/linux/$a/agent" "$stage/agent/linux/$a/agent"
        staged=$((staged+1))
      fi
    done
  fi

  if (( staged>0 )); then
    c_ok "已暂存 $staged 个架构的 Agent 二进制到 $stage"
    AGENT_BIN_DIR="$stage/agent"
    [[ -f "$stage/agent-install.sh" ]] && AGENT_SCRIPT_PATH="$stage/agent-install.sh"
  else
    c_warn "未暂存到 Agent 二进制：/bin/linux/{arch}/agent 将不可用；可在 $stage/agent/linux/<arch>/ 放置后重启 Server"
  fi
}

# ============================ 二进制获取 ============================
# 检测当前是否处于项目源码树（可用于从源码构建）
is_source_tree() {
  [[ -f go.mod && -d cmd/server && -d web ]]
}

# 从本地离线包解压 Node（避免联网安装 node/npm）；成功则把其 bin 加入 PATH
setup_local_node() {
  have_cmd node && return 0
  local f="${NODE_PKG:-}"
  [[ -n "$f" ]] || { c_warn "未提供 Node 安装包（offline/node-*-linux-$ARCH.tar.xz 或 --node-package），且系统无 node，前端可能无法构建"; return 1; }
  c_info "从本地包解压 Node: $f"
  local d; d="$(mktemp -d)"
  tar -xf "$f" -C "$d" || { rm -rf "$d"; return 1; }
  local bindir; bindir="$(ls -d "$d"/node-v*/bin 2>/dev/null | head -1)"
  [[ -n "$bindir" ]] || { rm -rf "$d"; return 1; }
  export PATH="$bindir:$PATH"
  c_ok "已启用本地 Node: $(node -v 2>/dev/null) / npm $(npm -v 2>/dev/null)"
}

# 从源码构建：先构建前端（Go embed 依赖 web/dist），再构建 server / agent
build_from_source() {
  c_info "从源码构建（含前端 web/dist）"
  have_cmd go || die "从源码构建需要 go 工具链（建议 >=1.22）"
  setup_local_node || true
  if [[ -d web ]]; then
    if have_cmd node && have_cmd npm; then
      c_info "构建前端（离线）"
      if [[ -d web/dist ]]; then
        c_info "检测到 web/dist，跳过前端构建（直接使用预构建产物）"
      elif [[ -d web/node_modules ]]; then
        ( cd web && npm run build ) || die "前端构建失败（请检查 node/npm 版本）"
        c_ok "前端构建完成: web/dist"
      else
        die "未检测到 web/dist 或 web/node_modules，无法离线构建前端（Go embed 依赖 web/dist）。请在本机提供预构建的 web/dist，或从联网机器拷贝 web/node_modules 到 web/，或把 node-*-linux-$ARCH.tar.xz 放入 offline/ 并同时提供 web/node_modules"
      fi
    else
      die "未检测到 node/npm，无法构建前端。可把 node-*-linux-$ARCH.tar.xz 放入 offline/（脚本自动解压），并提供 web/dist 或 web/node_modules"
    fi
  fi
  local tmp; tmp="$(mktemp -d)"
  c_info "go build server / agent"
  go build -o "$tmp/server" ./cmd/server || die "go build server 失败"
  go build -o "$tmp/agent"  ./cmd/agent  || die "go build agent 失败"
  install -m 0755 "$tmp/server" "$BIN_DIR/monitor-server" || die "安装 monitor-server 失败"
  c_ok "Server 已构建并安装: $BIN_DIR/monitor-server"
  # 将 agent 放入 dist 布局，供 stage_agent_dist 暂存到自带 CDN
  mkdir -p "$tmp/agent-dist/agent/linux/$ARCH"
  cp "$tmp/agent" "$tmp/agent-dist/agent/linux/$ARCH/agent"
  DIST_DIR="$tmp/agent-dist"
  c_ok "Agent 已构建（将暂存到 Server 自带 CDN）"
}

acquire_binary() {
  c_info "获取 Server 二进制"
  # 已安装且非升级模式则跳过
  if [[ -x "$BIN_DIR/monitor-server" ]] && (( ! UPGRADE )); then
    c_ok "已检测到 Server: $BIN_DIR/monitor-server，跳过安装（升级请用 --upgrade）"
    return
  fi
  # 1) 本地已构建产物目录
  if [[ -n "$DIST_DIR" ]]; then
    local src
    if [[ -f "$DIST_DIR/server" ]]; then src="$DIST_DIR/server";
    elif [[ -f "$DIST_DIR/server/linux/$ARCH/server" ]]; then src="$DIST_DIR/server/linux/$ARCH/server";
    else die "--dist 目录未找到 server 二进制"; fi
    install -m 0755 "$src" "$BIN_DIR/monitor-server" || die "安装 monitor-server 失败"
    c_ok "Server 已安装: $BIN_DIR/monitor-server"
    return
  fi
  # 2) 非交互：源码树内自动构建
  if (( ASSUME_YES )); then
    if is_source_tree; then build_from_source; return; fi
    die "非交互模式需提供 --dist 以获取 Server 二进制（或在本仓库源码目录内运行以自动从源码构建）"
  fi
  # 3) 交互选择
  echo "如何获取 Server 二进制？"
  local opts=("本地已构建产物目录")
  if is_source_tree; then opts+=("从源码构建（自动 go build + 前端构建）"); fi
  choose "获取方式" 1 "${opts[@]}"
  case "$CHOICE_VAL" in
    "本地已构建产物目录")
      prompt DIST_DIR "请输入含 server 二进制的目录" "./bin"
      local src
      if [[ -f "$DIST_DIR/server" ]]; then src="$DIST_DIR/server";
      elif [[ -f "$DIST_DIR/server/linux/$ARCH/server" ]]; then src="$DIST_DIR/server/linux/$ARCH/server";
      else die "该目录未找到 server 二进制"; fi
      install -m 0755 "$src" "$BIN_DIR/monitor-server" || die "安装 monitor-server 失败"
      c_ok "Server 已安装: $BIN_DIR/monitor-server"
      ;;
    "从源码构建"*) build_from_source ;;
    *) die "未知选择: $CHOICE_VAL" ;;
  esac
}

# ============================ 生成配置 ============================
generate_config() {
  c_info "生成配置文件"
  mkdir -p "$CONFIG_DIR" "$DATA_DIR"

  # 升级模式：已有配置则备份保留，不覆盖（避免丢失用户改动）
  if (( UPGRADE )) && [[ -f "$CONFIG_DIR/server.yaml" ]]; then
    cp -a "$CONFIG_DIR/server.yaml" "$CONFIG_DIR/server.yaml.bak.$(date +%s)"
    c_ok "已备份原配置: server.yaml.bak.*（升级保留原配置，不覆盖）"
    # 显式 --agent-auth/--agent-secret 时，仅就地更新 agentAuth 块（其余配置不动）
    if (( AGENT_AUTH_EXPLICIT )); then
      patch_agent_auth_config
      c_ok "已更新 agentAuth: enabled=$([[ "$ENABLE_AGENT_AUTH" == "yes" ]] && echo true || echo false)"
    fi
    return
  fi

  local tsdb_block
  if [[ "$TSDB_BACKEND" == "custom" ]]; then
    tsdb_block=$(printf '  backend: custom\n  addr: "%s"\n  writePath: "%s"\n  queryPath: "%s"\n  queryRangePath: "%s"' \
      "$TSDB_ADDR" "${TSDB_WRITE_PATH:-/api/v1/write}" "${TSDB_QUERY_PATH:-/api/v1/query}" "${TSDB_RANGE_PATH:-/api/v1/query_range}")
  else
    tsdb_block=$(printf '  backend: %s\n  addr: "%s"' "$TSDB_BACKEND" "$TSDB_ADDR")
    [[ -n "$TSDB_QUERY_ADDR" ]] && tsdb_block+=$(printf '\n  queryAddr: "%s"' "$TSDB_QUERY_ADDR")
  fi

  local webhook_block
  if [[ -n "$ALERT_WEBHOOK" ]]; then
    local urls_yaml; urls_yaml="$(echo "$ALERT_WEBHOOK" | sed 's/^/      - /; s/,/\n      - /g')"
    webhook_block="$(printf '  webhook:\n    enabled: true\n    urls:\n%s' "$urls_yaml")"
  else
    webhook_block="$(printf '  webhook:\n    enabled: false\n    urls: []')"
  fi

  cat > "$CONFIG_DIR/server.yaml" <<EOF
# nebula-monitor Server 配置（由 install-server.sh 生成）
mode: $MODE

listen: "$LISTEN"

# 时序库配置（对接已有实例；时序库由 deploy/install-tsdb.sh 单独安装）
# backend 可选: victoriametrics|mimir|cortex|thanos|prometheus|custom
tsdb:
$tsdb_block
  writeTimeout: 5
  queryTimeout: 10

nodeMeta: "$CONFIG_DIR/nodes.json"
dataDir: "$DATA_DIR"

offlineTimeout: 60

alert:
  enabled: true
  rulesFile: "$CONFIG_DIR/rules.yaml"
  evalInterval: 15
  recoverInterval: 30

notify:
  email:
    enabled: false
    smtpHost: "smtp.example.com"
    smtpPort: 587
    username: ""
    password: ""
    from: "monitor@example.com"
    to: []
    useTLS: true
$webhook_block

# Agent 接入授权（参考哪吒探针：启用后 Agent 需携带 secret）
agentAuth:
  enabled: $( [[ "$ENABLE_AGENT_AUTH" == "yes" ]] && echo true || echo false )
  secret: "$AGENT_SECRET"

# Agent 二进制分发目录（自带 CDN：/bin/linux/{arch}/agent 与 /install/agent-install.sh）
agentBinDir: "$AGENT_BIN_DIR"
agentScriptPath: "$AGENT_SCRIPT_PATH"

# 前端静态资源目录（磁盘读取，改前端只需替换文件+重启，无需重编二进制）
webDir: "$WEB_DIR"

# 登录认证（Web 仪表盘访问控制，启用后需登录）
auth:
  enabled: $( [[ "$ENABLE_AUTH" == "yes" ]] && echo true || echo false )
  username: "$AUTH_USERNAME"
  password: "$AUTH_PASSWORD"
  secret: "$AUTH_SECRET"
EOF

  # 生成默认告警规则（来自示例）
  if [[ ! -f "$CONFIG_DIR/rules.yaml" ]]; then
    generate_default_rules "$CONFIG_DIR/rules.yaml"
  fi
  c_ok "配置已写入: $CONFIG_DIR/server.yaml"
}

generate_default_rules() {
  local f="$1"
  cat > "$f" <<'EOF'
# 默认告警规则（由安装脚本生成；也可在 Web 界面「告警」中增删改）
# operator: > < >= <= == != ; for: 持续时间字符串如 "1m" / "60s" ; severity: warning|critical
# metric 取值参见前端「告警」新建规则时的指标字典（cpu_usage / mem_used_percent / disk_used_percent 等）
- name: "CPU 高使用率"
  metric: cpu_usage
  operator: ">"
  threshold: 85
  for: "1m"
  severity: warning
  enabled: true
- name: "内存高使用率"
  metric: mem_used_percent
  operator: ">"
  threshold: 90
  for: "1m"
  severity: warning
  enabled: true
- name: "磁盘高使用率"
  metric: disk_used_percent
  operator: ">"
  threshold: 90
  for: "2m"
  severity: critical
  enabled: true
EOF
}

write_server_service() {
  cat > "$SERVICE_DIR/monitor-server.service" <<EOF
[Unit]
Description=nebula-monitor Server
After=network.target victoriametrics.service
Wants=victoriametrics.service

[Service]
Type=simple
ExecStart=$BIN_DIR/monitor-server -config $CONFIG_DIR/server.yaml
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  c_ok "已写入 systemd 单元: $SERVICE_DIR/monitor-server.service"
}

# ============================ 启动 & 校验 ============================
start_services() {
  if ! have_cmd systemctl; then
    c_warn "跳过 systemd 启动；可手动执行: $BIN_DIR/monitor-server -config $CONFIG_DIR/server.yaml"
    return
  fi
  systemctl daemon-reload
  (( UPGRADE )) && systemctl stop monitor-server.service 2>/dev/null
  systemctl enable monitor-server.service
  systemctl restart monitor-server.service
  sleep 2
}

health_check() {
  c_info "健康检查"
  local port
  # 提取端口：兼容 ":8080" / "0.0.0.0:8080" / "8080" 三种写法
  port="${LISTEN##*:}"
  # 探测根路径（公开路径，无需登录 token；服务存活即返回 200）
  local url="http://127.0.0.1:${port}/"
  local code
  local i
  code="000"
  for (( i=0; i<5; i++ )); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$url" 2>/dev/null || echo 000)"
    [[ "$code" == "200" ]] && break
    sleep 1
  done
  if [[ "$code" == "200" ]]; then
    c_ok "Server 健康检查通过 ($url -> 200)"
  else
    c_warn "健康检查未通过 (HTTP $code)，请查看: journalctl -u monitor-server -n 50"
  fi
}

summary() {
  echo
  echo "============================================================"
  echo " nebula-monitor Server 安装完成"
  echo "------------------------------------------------------------"
  echo " 监听地址      : $LISTEN"
  echo " 时序库后端    : $TSDB_BACKEND"
  echo " 时序库地址    : $TSDB_ADDR${TSDB_QUERY_ADDR:+ (查询 $TSDB_QUERY_ADDR)}"
  echo " 配置文件      : $CONFIG_DIR/server.yaml"
  echo " 二进制        : $BIN_DIR/monitor-server"
  echo "------------------------------------------------------------"
  echo " 查看状态 : systemctl status monitor-server"
  echo " 查看日志 : journalctl -u monitor-server -f"
  echo " 仪表盘   : 浏览器打开 http://<本机IP>${LISTEN%/}/"
  echo " 安装 Agent: 参见 deploy/agent-install.sh（指向本机 $LISTEN）"
  echo "------------------------------------------------------------"
  local srv="http://<本机IP>${LISTEN%/}"
  echo " 一行安装 Agent（从此机自带的 CDN 拉取，无需公网）："
  if [[ "$ENABLE_AGENT_AUTH" == "yes" && -n "$AGENT_SECRET" ]]; then
    echo "   curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --server $srv --secret $AGENT_SECRET"
  else
    echo "   curl -fsSL ${srv}/install/agent-install.sh | bash -s -- --server $srv"
  fi
  echo "============================================================"
}

# ============================ 主流程 ============================
main() {
  detect_env
  scan_packages
  scan_prebuilt
  preflight
  step_mode
  step_tsdb
  step_network
  step_alert
  step_agent_auth
  step_auth
  acquire_binary
  stage_web_dist
  stage_agent_dist
  generate_config
  write_server_service
  start_services
  health_check
  summary
}

main "$@"
