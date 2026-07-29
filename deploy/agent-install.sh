#!/usr/bin/env bash
#
# nebula-monitor Agent 交互式安装脚本（分步引导）
# ----------------------------------------------------------------------------
# 功能：
#   1. 预检环境（root / 架构 / systemd / 网络）
#   2. 配置上报 Server 地址、节点名、分组、采集间隔
#   3. 选择采集项（CPU/内存/磁盘/网络/进程/负载，默认全开）
#   4. 可选附加标签（key=value，逗号分隔）
#   5. 获取并安装 Agent 二进制、生成 agent.yaml 与 systemd 单元
#   6. 启动服务并做连通性检查，输出后续步骤
#
# 既支持交互式分步引导，也支持非交互式（--yes 使用默认值，或显式传参）。
# 在线安装（从 Server 拉取脚本）：
#   curl -fsSL http://<server>/install/agent-install.sh | bash -s -- --server http://10.0.0.1:8080
#
set -uo pipefail

# ============================ 默认参数 ============================
SERVER_URL=""
NODE=""
GROUP=""
SECRET=""
INTERVAL=""
BASE_URL=""
DIST_DIR=""
ASSUME_YES=0
ASK_BINARY=0

# 采集项开关（默认全开）
C_CPU=""; C_MEM=""; C_DISK=""; C_NET=""; C_PROC=""; C_LOAD=""

# 附加标签（解析后的 YAML 片段）
LABELS_YAML=""

ARCH=""
OS=""

CONFIG_DIR="/etc/monitor-agent"
BIN_DIR="/usr/local/bin"
SERVICE_DIR="/etc/systemd/system"

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

  --server <url>      上报的 Server 地址，如 http://10.0.0.1:8080
  --secret <key>      接入授权密钥（Server 启用 agentAuth 时必填）
  --node <name>       节点名（默认本机 hostname）
  --group <name>      分组名（默认 default）
  --interval <sec>    采集间隔秒（默认 15）
  --dist <dir>        本地已构建产物目录（含 agent 二进制）
  --base-url <url>    二进制下载基址（默认从 \$SERVER_URL/bin 拉取）
  --yes               非交互式，未提供的项使用默认值
  --ask-binary        弹出"获取 Agent 二进制"菜单（交互选择下载源）
  -h, --help          显示本帮助

示例：
  bash agent-install.sh                      # 交互式分步引导
  bash agent-install.sh --yes --server http://10.0.0.1:8080
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)   SERVER_URL="$2"; shift 2 ;;
    --secret)   SECRET="$2"; shift 2 ;;
    --node)     NODE="$2"; shift 2 ;;
    --group)    GROUP="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --dist)     DIST_DIR="$2"; shift 2 ;;
    --base-url) BASE_URL="$2"; shift 2 ;;
    --yes)      ASSUME_YES=1; shift ;;
    --ask-binary) ASK_BINARY=1; shift ;;
    -h|--help)  usage ;;
    *) die "未知参数: $1（用 -h 查看帮助）" ;;
  esac
done

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
  if ! have_cmd curl && ! have_cmd wget; then
    die "需要 curl 或 wget 用于下载"
  fi
  c_ok "环境检测: OS=$OS ARCH=$ARCH"
}

# ============================ 交互步骤 ============================
step_server() {
  if [[ -z "$SERVER_URL" ]]; then
    if (( ASSUME_YES )); then SERVER_URL="http://127.0.0.1:8080"; else
      prompt SERVER_URL "上报的 Server 地址(HTTP URL)" "http://127.0.0.1:8080"
    fi
  fi
  # 校验 URL 形式
  [[ "$SERVER_URL" == http://* || "$SERVER_URL" == https://* ]] \
    || die "Server 地址需以 http:// 或 https:// 开头: $SERVER_URL"
  c_ok "Server 地址: $SERVER_URL"
}

step_node() {
  local def_host
  def_host="$(hostname 2>/dev/null || echo unknown)"
  if [[ -z "$NODE" ]]; then
    if (( ASSUME_YES )); then NODE="$def_host"; else
      prompt NODE "节点名（留空则用本机 hostname）" "$def_host"
    fi
  fi
  [[ -z "$NODE" ]] && NODE="$def_host"
  c_ok "节点名: $NODE"
}

step_group() {
  if [[ -z "$GROUP" ]]; then
    if (( ASSUME_YES )); then GROUP="default"; else
      prompt GROUP "分组名（便于在控制台归类）" "default"
    fi
  fi
  c_ok "分组: $GROUP"
}

step_interval() {
  if [[ -z "$INTERVAL" ]]; then
    if (( ASSUME_YES )); then INTERVAL=15; else
      prompt INTERVAL "采集间隔（秒，建议 10~60）" "15"
    fi
  fi
  # 必须是正整数
  [[ "$INTERVAL" =~ ^[0-9]+$ ]] && (( INTERVAL>0 )) \
    || die "采集间隔需为正整数秒: $INTERVAL"
  c_ok "采集间隔: ${INTERVAL}s"
}

step_secret() {
  if [[ -z "$SECRET" ]]; then
    if (( ASSUME_YES )); then SECRET=""; else
      prompt SECRET "接入授权密钥（Server 启用 agentAuth 时必填，留空跳过）" ""
    fi
  fi
  if [[ -n "$SECRET" ]]; then
    c_ok "接入授权密钥: 已设置（将写入 agent.yaml 并通过请求头上报）"
  else
    c_ok "接入授权密钥: 未设置（仅当 Server 未启用 agentAuth 时可正常上报）"
  fi
}

step_collectors() {
  c_info "选择采集项（默认全开）"
  if (( ASSUME_YES )); then
    C_CPU=true; C_MEM=true; C_DISK=true; C_NET=true; C_PROC=true; C_LOAD=true
    c_ok "采集项: 全部启用"
    return
  fi
  if confirm "是否启用全部采集项（CPU/内存/磁盘/网络/进程/负载）？" "yes"; then
    C_CPU=true; C_MEM=true; C_DISK=true; C_NET=true; C_PROC=true; C_LOAD=true
    c_ok "采集项: 全部启用"
  else
    confirm "  启用 CPU 采集？"        "yes" && C_CPU=true  || C_CPU=false
    confirm "  启用内存采集？"          "yes" && C_MEM=true  || C_MEM=false
    confirm "  启用磁盘采集？"          "yes" && C_DISK=true || C_DISK=false
    confirm "  启用网络采集？"          "yes" && C_NET=true  || C_NET=false
    confirm "  启用进程采集？"          "yes" && C_PROC=true || C_PROC=false
    confirm "  启用系统负载采集？"      "yes" && C_LOAD=true || C_LOAD=false
    c_ok "采集项: CPU=$C_CPU MEM=$C_MEM DISK=$C_DISK NET=$C_NET PROC=$C_PROC LOAD=$C_LOAD"
  fi
}

step_labels() {
  c_info "附加标签（可选）"
  local raw=""
  if (( ASSUME_YES )); then
    raw=""
  else
    prompt raw "附加标签（格式 key=value，多个用逗号分隔，留空跳过）" ""
  fi
  [[ -z "$raw" ]] && { LABELS_YAML=""; c_ok "附加标签: 无"; return; }

  # 解析 key=value,key2=value2 -> YAML 映射
  local out=""
  local IFS_OLD="$IFS"; IFS=','
  local pair
  for pair in $raw; do
    IFS="$IFS_OLD"
    pair="$(echo "$pair" | xargs)"   # 去首尾空格
    [[ -z "$pair" ]] && continue
    [[ "$pair" == *=* ]] || die "标签格式错误（需 key=value）: $pair"
    local k="${pair%%=*}" v="${pair#*=}"
    k="$(echo "$k" | xargs)"; v="$(echo "$v" | xargs)"
    out+="  ${k}: \"${v}\""$'\n'
    IFS=','
  done
  IFS="$IFS_OLD"

  [[ -n "$out" ]] || die "未解析到有效标签: $raw"
  LABELS_YAML="$out"
  c_ok "附加标签: $(echo -n "$raw" | tr ',' ' ')"
}

# ============================ 二进制获取 ============================
# download <url> <out> [secret]
# 当提供 secret 时，通过 X-Agent-Secret 请求头鉴权下载（Server 启用 agentAuth 时必填）。
download() {
  local url="$1" out="$2" secret="${3:-}"
  if have_cmd curl; then
    if [[ -n "$secret" ]]; then
      curl -fsSL -H "X-Agent-Secret: $secret" "$url" -o "$out"
    else
      curl -fsSL "$url" -o "$out"
    fi
  else
    if [[ -n "$secret" ]]; then
      wget -q --header="X-Agent-Secret: $secret" "$url" -O "$out"
    else
      wget -q "$url" -O "$out"
    fi
  fi
}

# verify_checksum <bin> <sha_url> [secret]
# 下载校验和（JSON: checksum/sig），比对本地 sha256；若提供 secret 且服务器返回 sig，
# 再校验 HMAC-SHA256 签名。校验失败直接中止安装，防止运行被篡改的二进制。
verify_checksum() {
  local bin="$1" sha_url="$2" secret="${3:-}" tmpc chk sig localchk calc
  tmpc="$(mktemp)"
  if ! download "$sha_url" "$tmpc" "$secret"; then
    c_warn "无法下载校验和，跳过完整性校验"
    rm -f "$tmpc"
    return 0
  fi
  chk="$(grep -oE '"checksum"[[:space:]]*:[[:space:]]*"[^"]+"' "$tmpc" | sed -E 's/.*:"([^"]+)"/\1/')"
  sig="$(grep -oE '"sig"[[:space:]]*:[[:space:]]*"[^"]+"' "$tmpc" | sed -E 's/.*:"([^"]+)"/\1/')"
  rm -f "$tmpc"
  [[ -n "$chk" ]] || { c_warn "校验和缺失，跳过完整性校验"; return 0; }
  localchk="$(sha256sum "$bin" | awk '{print $1}')"
  [[ "$localchk" == "$chk" ]] || die "二进制校验和不匹配，疑似被篡改，已中止安装"
  if [[ -n "$secret" && -n "$sig" ]]; then
    if have_cmd openssl; then
      calc="$(printf '%s' "$chk" | openssl dgst -sha256 -hmac "$secret" -r | awk '{print $1}')"
      [[ "$calc" == "$sig" ]] || die "二进制签名不匹配，疑似被篡改，已中止安装"
    else
      c_warn "openssl 不可用，仅完成校验和比对"
    fi
  fi
  c_ok "二进制完整性校验通过"
}

acquire_binary() {
  c_info "获取 Agent 二进制"
  local src=""
  if [[ -n "$DIST_DIR" ]]; then
    if [[ -f "$DIST_DIR/agent" ]]; then src="$DIST_DIR/agent";
    elif [[ -f "$DIST_DIR/agent/linux/$ARCH/agent" ]]; then src="$DIST_DIR/agent/linux/$ARCH/agent";
    else die "--dist 目录未找到 agent 二进制"; fi
  elif [[ -n "$BASE_URL" ]]; then
    local tmp; tmp="$(mktemp -d)"
    local url="$BASE_URL/linux/$ARCH/agent"
    c_info "下载 $url"
    download "$url" "$tmp/agent" "$SECRET" || die "下载 Agent 失败: $url"
    verify_checksum "$tmp/agent" "$url.sha256" "$SECRET"
    src="$tmp/agent"
  else
    if (( ASK_BINARY )); then
      # 仅显式加 --ask-binary 才交互选源；否则默认从 Server 在线下载，不再弹菜单。
      echo "如何获取 Agent 二进制？"
      choose "获取方式" 1 "从 Server 在线下载" "本地已构建产物目录" "从 URL 下载"
      case "$CHOICE_VAL" in
        "从 Server 在线下载")
          BASE_URL="${SERVER_URL%/}/bin"
          local tmp1; tmp1="$(mktemp -d)"
          local url1="$BASE_URL/linux/$ARCH/agent"
          c_info "下载 $url1"
          download "$url1" "$tmp1/agent" "$SECRET" || die "下载 Agent 失败: $url1"
          verify_checksum "$tmp1/agent" "$url1.sha256" "$SECRET"
          src="$tmp1/agent"
          ;;
        "本地已构建产物目录")
          prompt DIST_DIR "请输入含 agent 二进制的目录" "./bin"
          if [[ -f "$DIST_DIR/agent" ]]; then src="$DIST_DIR/agent";
          elif [[ -f "$DIST_DIR/agent/linux/$ARCH/agent" ]]; then src="$DIST_DIR/agent/linux/$ARCH/agent";
          else die "该目录未找到 agent 二进制"; fi
          ;;
        "从 URL 下载")
          prompt BASE_URL "请输入二进制下载基址" ""
          local tmp2; tmp2="$(mktemp -d)"
          local url2="$BASE_URL/linux/$ARCH/agent"
          c_info "下载 $url2"
          download "$url2" "$tmp2/agent" "$SECRET" || die "下载 Agent 失败: $url2"
          verify_checksum "$tmp2/agent" "$url2.sha256" "$SECRET"
          src="$tmp2/agent"
          ;;
      esac
    else
      # 默认从 Server 在线下载（--yes / 管道 / 交互直跑 均走此分支，不再弹菜单）。
      # 若需本地产物或从其它 URL 下载，请用 --dist / --base-url 显式指定，或加 --ask-binary 交互选源。
      BASE_URL="${SERVER_URL%/}/bin"
      local tmp; tmp="$(mktemp -d)"
      local url="$BASE_URL/linux/$ARCH/agent"
      c_info "下载 $url"
      download "$url" "$tmp/agent" "$SECRET" || die "下载 Agent 失败: $url（确认 Server 已提供 /bin 端点，或改用 --base-url/--dist）"
      verify_checksum "$tmp/agent" "$url.sha256" "$SECRET"
      src="$tmp/agent"
    fi
  fi
  install -m 0755 "$src" "$BIN_DIR/monitor-agent" || die "安装 monitor-agent 失败"
  c_ok "Agent 已安装: $BIN_DIR/monitor-agent"
}

# ============================ 生成配置 ============================
generate_config() {
  c_info "生成配置文件"
  mkdir -p "$CONFIG_DIR"

  cat > "$CONFIG_DIR/agent.yaml" <<EOF
# nebula-monitor Agent 配置（由 agent-install.sh 生成）
serverURL: "$SERVER_URL"
node: "$NODE"
group: "$GROUP"
secret: "$SECRET"
interval: $INTERVAL
batchSize: 200

collectors:
  cpu: $C_CPU
  memory: $C_MEM
  disk: $C_DISK
  network: $C_NET
  process: $C_PROC
  load: $C_LOAD
$( [[ -n "$LABELS_YAML" ]] && printf 'labels:\n%s' "$LABELS_YAML" )
EOF
  c_ok "配置已写入: $CONFIG_DIR/agent.yaml"
}

write_service() {
  cat > "$SERVICE_DIR/monitor-agent.service" <<EOF
[Unit]
Description=nebula-monitor Agent (主机指标采集上报)
After=network.target

[Service]
Type=simple
ExecStart=$BIN_DIR/monitor-agent -config $CONFIG_DIR/agent.yaml
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF
  c_ok "已写入 systemd 单元: $SERVICE_DIR/monitor-agent.service"
}

# ============================ 启动 & 校验 ============================
start_service() {
  if ! have_cmd systemctl; then
    c_warn "跳过 systemd 启动；可手动执行: $BIN_DIR/monitor-agent -config $CONFIG_DIR/agent.yaml"
    return
  fi
  systemctl daemon-reload
  systemctl enable monitor-agent.service
  systemctl restart monitor-agent.service
  sleep 2
}

connectivity_check() {
  c_info "连通性检查"
  # 用 agent 视角的鉴权预检端点：与 Agent 真实上报走同一条 X-Agent-Secret 校验，
  # 因此 200/401 能真实反映密钥是否被 Server 接受（不受登录 Bearer token 影响）。
  local url="${SERVER_URL%/}/api/v1/agent/check"
  local auth_args=()
  if [[ -n "${SECRET:-}" ]]; then
    auth_args=(-H "X-Agent-Secret: $SECRET")
  fi
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "${auth_args[@]}" "$url" 2>/dev/null || echo 000)"
  if [[ "$code" == "200" ]]; then
    c_ok "接入鉴权校验通过（Server 接受本密钥，Agent 可正常上报）"
  elif [[ "$code" == "401" ]]; then
    c_err "接入密钥校验失败（HTTP 401）：请检查 --secret 是否与 server.yaml 中 agentAuth.secret 一致"
    c_err "修正方法：重新运行安装并传入正确密钥，或到 Server 端确认 agentAuth.secret 配置后重启"
  else
    c_warn "无法确认接入鉴权（HTTP $code），请确认 Server 地址与端口可达、且未启用额外网络隔离"
  fi
  if have_cmd systemctl; then
    local st
    st="$(systemctl is-active monitor-agent.service 2>/dev/null || echo unknown)"
    if [[ "$st" == "active" ]]; then
      c_ok "Agent 服务运行中 (systemctl is-active monitor-agent = active)"
    else
      c_warn "Agent 服务未运行 (状态: $st)，请查看: journalctl -u monitor-agent -n 50"
    fi
  fi
}

summary() {
  echo
  echo "============================================================"
  echo " nebula-monitor Agent 安装完成"
  echo "------------------------------------------------------------"
  echo " 上报 Server : $SERVER_URL"
  echo " 节点名      : $NODE"
  echo " 分组        : $GROUP"
  echo " 接入密钥    : ${SECRET:+已设置}${SECRET:-未设置}"
  echo " 采集间隔    : ${INTERVAL}s"
  echo " 采集项      : CPU=$C_CPU MEM=$C_MEM DISK=$C_DISK NET=$C_NET PROC=$C_PROC LOAD=$C_LOAD"
  echo " 配置文件    : $CONFIG_DIR/agent.yaml"
  echo " 二进制      : $BIN_DIR/monitor-agent"
  echo "------------------------------------------------------------"
  echo " 查看状态 : systemctl status monitor-agent"
  echo " 查看日志 : journalctl -u monitor-agent -f"
  echo " 控制台   : 打开 $SERVER_URL 查看本节点 $NODE"
  echo "============================================================"
}

# ============================ 主流程 ============================
main() {
  detect_env
  preflight
  step_server
  step_node
  step_group
  step_interval
  step_secret
  step_collectors
  step_labels
  acquire_binary
  generate_config
  write_service
  start_service
  connectivity_check
  summary
}

main "$@"
