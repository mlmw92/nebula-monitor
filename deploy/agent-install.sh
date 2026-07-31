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
用法: $0 [子命令] [选项]

子命令：
  (无)                安装 Agent（交互式分步引导，默认不开启中间件监控）
  redis               配置 Redis 中间件监控（引导填写实例并合并到 agent.yaml）
  mysql               配置 MySQL 中间件监控
  postgres            配置 PostgreSQL 中间件监控
  nginx               配置 Nginx 中间件监控
  kafka               配置 Kafka 中间件监控
  rocketmq            配置 RocketMQ 中间件监控

选项：
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
  bash agent-install.sh                      # 交互式安装 Agent
  bash agent-install.sh --yes --server http://10.0.0.1:8080
  bash agent-install.sh redis                # 配置 Redis 监控（安装后执行）
  bash agent-install.sh mysql                # 配置 MySQL 监控
  bash agent-install.sh postgres             # 配置 PostgreSQL 监控
  bash agent-install.sh nginx                # 配置 Nginx 监控
  bash agent-install.sh kafka                # 配置 Kafka 监控
  bash agent-install.sh rocketmq             # 配置 RocketMQ 监控
EOF
  exit 0
}

# ============================ 子命令分发 ============================
# 首个参数为中间件子命令时进入配置流程（不安装/不覆盖 Agent 二进制）。
# 支持的子命令: redis mysql postgres nginx kafka rocketmq
SUBCOMMAND=""
if [[ $# -gt 0 ]]; then
  case "$1" in
    redis|mysql|postgres|nginx|kafka|rocketmq)
      SUBCOMMAND="$1"; shift ;;
    -h|--help) usage ;;
    --*) ;;  # 以 -- 开头的是选项，不是子命令
    *)
      local_lower="$(echo "$1" | tr '[:upper:]' '[:lower:]')"
      case "$local_lower" in
        redis|mysql|postgres|nginx|kafka|rocketmq)
          SUBCOMMAND="$local_lower"; shift ;;
        *)
          die "未知参数或子命令: $1（用 -h 查看帮助；支持的子命令: redis mysql postgres nginx kafka rocketmq）"
          ;;
      esac
      ;;
  esac
fi

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
  # === 中间件监控（默认关闭，按需开启；实例示例见 README「中间件监控」章节）===
  redis: false
  mysql: false
  postgres: false
  nginx: false
  kafka: false
  docker: false
  rocketmq: false
  # === TCP 端口存活检测（默认关闭）===
  port: false
$( [[ -n "$LABELS_YAML" ]] && printf 'labels:\n%s' "$LABELS_YAML" )

# ==================== 中间件实例配置示例（默认注释，按需取消注释并填写）====================
# 各中间件密码仅存本机，不上报 Server。配置后可执行子命令重新引导：
#   bash $CONFIG_DIR/agent-install.sh redis / mysql / postgres / nginx / kafka / rocketmq
# redisInstances:
#   - name: "redis-standalone"
#     addr: "127.0.0.1:6379"
#     password: "yourpassword"
#     topology: "standalone"
# mysqlInstances:
#   - name: "mysql-master"
#     addr: "127.0.0.1:3306"
#     user: "monitor"
#     password: "yourpassword"
#     topology: "standalone"
# postgresInstances:
#   - name: "pg-primary"
#     addr: "127.0.0.1:5432"
#     database: "postgres"
#     user: "monitor"
#     password: "yourpassword"
#     sslMode: "disable"
#     topology: "standalone"
# nginxInstances:
#   - name: "nginx-01"
#     addr: "127.0.0.1:80"
#     statusPath: "/nginx_status"
# kafkaInstances:
#   - name: "kafka-cluster"
#     addr: "127.0.0.1:9092"
#     version: "2.8.0"
# dockerInstances:
#   - name: "local-docker"
#     addr: "unix:///var/run/docker.sock"
# rocketmqInstances:
#   - name: "rocketmq-cluster"
#     addr: "127.0.0.1:9876"
# portChecks:
#   - "80"
#   - "443"
EOF
  c_ok "配置已写入: $CONFIG_DIR/agent.yaml"
}

# ============================ 安装脚本自身到本地 ============================
# 把 agent-install.sh 拷贝到 CONFIG_DIR，方便用户后续执行 redis 子命令等配置操作。
# 安装方式可能是 curl|bash 管道（无本地文件），也可能 bash agent-install.sh（有本地文件）。
# 优先用本地文件；管道方式则从 Server CDN 下载。
install_self_script() {
  local target="${CONFIG_DIR}/agent-install.sh"
  local src_path=""

  # 尝试定位脚本自身路径（非管道执行时 $0 是文件路径）
  if [[ -f "$0" && -s "$0" ]]; then
    src_path="$0"
  fi

  if [[ -n "$src_path" ]]; then
    cp -a "$src_path" "$target"
  else
    # 管道执行：从 Server CDN 下载
    local url="${SERVER_URL%/}/install/agent-install.sh"
    if ! download "$url" "$target" "$SECRET" 2>/dev/null; then
      c_warn "未能从 Server 下载 agent-install.sh 到本地（不影响 Agent 运行；后续配置可改用 curl 管道方式）"
      return 0
    fi
  fi
  chmod +x "$target" 2>/dev/null || true
  c_ok "配置脚本已安装: $target（后续可执行 bash $target redis 配置 Redis 监控）"
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
# 升级时只杀主进程，不杀整个 cgroup（升级脚本需独立存活）
KillMode=process

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
  echo " 配置脚本    : $CONFIG_DIR/agent-install.sh（执行 redis 子命令配置 Redis 监控）"
  echo " 二进制      : $BIN_DIR/monitor-agent"
  echo "------------------------------------------------------------"
  echo " 查看状态 : systemctl status monitor-agent"
  echo " 查看日志 : journalctl -u monitor-agent -f"
  echo " 控制台   : 打开 $SERVER_URL 查看本节点 $NODE"
  echo "============================================================"
}

# ============================ Redis 监控配置 ============================
# 子命令: agent-install.sh redis
# 功能：交互式引导用户填写 Redis 实例信息，生成 YAML 片段并合并到 agent.yaml，
#       开启 collectors.redis，重启 agent 服务。不安装/覆盖二进制。
redis_config() {
  echo "============================================================"
  echo " Redis 中间件监控配置"
  echo "------------------------------------------------------------"
  echo " 本向导引导你填写 Redis 实例，写入 ${CONFIG_DIR}/agent.yaml"
  echo " 并开启 collectors.redis，完成后自动重启 agent。"
  echo " 密码仅存本机 agent.yaml，不上报 Server。"
  echo "============================================================"
  echo

  # 前置检查：agent.yaml 必须已存在
  local cfg="${CONFIG_DIR}/agent.yaml"
  if [[ ! -f "$cfg" ]]; then
    die "未找到 $cfg，请先安装 Agent（bash agent-install.sh）"
  fi

  # 备份原配置
  cp -a "$cfg" "${cfg}.bak.$(date +%s)"
  c_ok "已备份原配置: ${cfg}.bak.*"

  # 收集实例（循环添加，直到用户选择结束）
  local instances_yaml=""
  local idx=0
  while true; do
    idx=$((idx + 1))
    echo
    echo "--- 配置第 $idx 个 Redis 实例（留空名称跳过结束）---"

    local name addr password topology sentinel_name exporter_url
    sentinel_name=""
    exporter_url=""
    prompt name "集群/逻辑组名（集群/哨兵模式下同名实例归为一组；单机下作展示别名，如 cache-primary）" ""
    [[ -z "$name" ]] && { echo "已结束实例添加。"; break; }

    prompt addr "实例地址 host:port（如 127.0.0.1:6379）" "127.0.0.1:6379"
    prompt password "认证密码（无密码留空，仅存本地不上报）" ""

    echo "请选择部署拓扑："
    choose "拓扑类型" 1 "单机(standalone)" "哨兵(sentinel)" "集群(cluster)" "exporter(Prometheus)"
    local topo=""
    case "$CHOICE_VAL" in
      单机*) topo="standalone" ;;
      哨兵*) topo="sentinel" ;;
      集群*) topo="cluster" ;;
      exporter*) topo="standalone" ;;  # exporter 模式 topology 填 standalone，靠 exporterURL 触发
    esac

    if [[ "$topo" == "sentinel" ]]; then
      prompt sentinel_name "哨兵监控的 master 名称（如 mymaster）" "mymaster"
    fi

    if [[ "$CHOICE_VAL" == exporter* ]]; then
      prompt exporter_url "exporter /metrics URL（如 http://127.0.0.1:9121/metrics）" "http://127.0.0.1:9121/metrics"
    fi

    # 生成该实例的 YAML 片段
    instances_yaml+="  - name: \"${name}\""$'\n'
    instances_yaml+="    addr: \"${addr}\""$'\n'
    if [[ -n "$password" ]]; then
      instances_yaml+="    password: \"${password}\""$'\n'
    fi
    instances_yaml+="    topology: \"${topo}\""$'\n'
    if [[ -n "$sentinel_name" ]]; then
      instances_yaml+="    sentinelName: \"${sentinel_name}\""$'\n'
    fi
    if [[ -n "$exporter_url" ]]; then
      instances_yaml+="    exporterURL: \"${exporter_url}\""$'\n'
    fi

    c_ok "已添加实例: $name ($addr, $topo)"

    if ! confirm "是否继续添加下一个 Redis 实例？" "no"; then
      break
    fi
  done

  if [[ -z "$instances_yaml" ]]; then
    c_warn "未添加任何 Redis 实例，配置未变更。"
    exit 0
  fi

  echo
  c_info "即将写入以下 Redis 配置到 $cfg："
  echo "----------------------------------------"
  printf 'collectors:\n  redis: true\n\nredisInstances:\n%s' "$instances_yaml"
  echo "----------------------------------------"

  if ! confirm "确认写入并重启 agent？" "yes"; then
    echo "已取消，配置未变更。"
    exit 0
  fi

  # 合并到 agent.yaml：
  # 1) 若已有 collectors 段，确保其中 redis: true（无则追加）
  # 2) 若已有 redisInstances 段，整体替换；否则追加到文件末尾
  # 采用 sed/awk 操作，兼容已存在的 collectors 段。

  # 步骤1：处理 collectors.redis
  # 先检查是否存在 collectors 段且含 redis 字段
  if grep -qE '^[[:space:]]*redis:' "$cfg" 2>/dev/null; then
    # 已有 redis 行，强制改为 true（不管原来 true/false）
    sed -i -E 's/^([[:space:]]*)redis:[[:space:]]*.*/\1redis: true/' "$cfg"
  elif grep -qE '^[[:space:]]*collectors:' "$cfg" 2>/dev/null; then
    # 有 collectors 段但无 redis 行，在 collectors: 下一行插入
    sed -i -E '/^[[:space:]]*collectors:/a\  redis: true' "$cfg"
  else
    # 无 collectors 段，追加
    printf '\ncollectors:\n  redis: true\n' >> "$cfg"
  fi
  c_ok "已开启 collectors.redis"

  # 步骤2：处理 redisInstances
  # 先删除已存在的 redisInstances 段（从 redisInstances: 行到文件末尾或下一个顶层键）
  # 用 awk 找到 redisInstances: 行，跳过其后所有缩进行，保留其他内容
  local tmp_file
  tmp_file="$(mktemp)"
  awk -v found=0 '
    /^redisInstances:/ { found=1; next }
    found == 1 && /^[[:space:]]+/ { next }
    found == 1 && /^[^[:space:]]/ { found=0 }
    found == 0 { print }
  ' "$cfg" > "$tmp_file"
  mv "$tmp_file" "$cfg"

  # 追加新的 redisInstances 段
  printf '\nredisInstances:\n%s' "$instances_yaml" >> "$cfg"
  c_ok "已写入 redisInstances 配置"

  # 重启 agent
  if have_cmd systemctl; then
    c_info "重启 monitor-agent 服务..."
    systemctl restart monitor-agent 2>/dev/null || true
    sleep 2
    local st
    st="$(systemctl is-active monitor-agent 2>/dev/null || echo unknown)"
    if [[ "$st" == "active" ]]; then
      c_ok "Agent 已重启并运行中"
    else
      c_warn "Agent 服务状态: $st，请查看日志: journalctl -u monitor-agent -n 50"
    fi
  else
    c_warn "未检测到 systemctl，请手动重启 agent 进程"
  fi

  echo
  echo "============================================================"
  echo " Redis 监控配置完成"
  echo "------------------------------------------------------------"
  echo " 配置文件   : $cfg"
  echo " 查看日志   : journalctl -u monitor-agent -f | grep -i redis"
  echo " Web 端     : 中间件监控 → Redis Tab 查看实例"
  echo " 详细配置   : 参阅 README.md Redis 实例配置说明"
  echo "============================================================"
}

# ============================ 通用中间件配置 ============================
# 通用中间件配置函数，通过参数区分 MySQL/PostgreSQL/Nginx/Kafka/RocketMQ。
# 用法: middleware_config <type>
#   type: mysql|postgres|nginx|kafka|rocketmq
middleware_config() {
  local mw_type="$1"
  local mw_name mw_collector mw_instances_key
  case "$mw_type" in
    mysql)    mw_name="MySQL";       mw_collector="mysql";    mw_instances_key="mysqlInstances" ;;
    postgres) mw_name="PostgreSQL";  mw_collector="postgres"; mw_instances_key="postgresInstances" ;;
    nginx)    mw_name="Nginx";       mw_collector="nginx";    mw_instances_key="nginxInstances" ;;
    kafka)    mw_name="Kafka";       mw_collector="kafka";    mw_instances_key="kafkaInstances" ;;
    rocketmq) mw_name="RocketMQ";    mw_collector="rocketmq"; mw_instances_key="rocketmqInstances" ;;
    *) die "不支持的中间件类型: $mw_type" ;;
  esac

  echo "============================================================"
  echo " ${mw_name} 中间件监控配置"
  echo "------------------------------------------------------------"
  echo " 本向导引导你填写 ${mw_name} 实例，写入 ${CONFIG_DIR}/agent.yaml"
  echo " 并开启 collectors.${mw_collector}，完成后自动重启 agent。"
  echo " 密码仅存本机 agent.yaml，不上报 Server。"
  echo "============================================================"
  echo

  local cfg="${CONFIG_DIR}/agent.yaml"
  if [[ ! -f "$cfg" ]]; then
    die "未找到 $cfg，请先安装 Agent（bash agent-install.sh）"
  fi

  cp -a "$cfg" "${cfg}.bak.$(date +%s)"
  c_ok "已备份原配置: ${cfg}.bak.*"

  local instances_yaml=""
  local idx=0
  while true; do
    idx=$((idx + 1))
    echo
    echo "--- 配置第 $idx 个 ${mw_name} 实例（留空名称跳过结束）---"

    local name addr user password exporter_url extra_fields=""
    prompt name "实例别名（如 ${mw_type}-primary）" ""
    [[ -z "$name" ]] && { echo "已结束实例添加。"; break; }

    prompt addr "地址 host:port（如 127.0.0.1:${mw_type}_port）" "127.0.0.1"

    case "$mw_type" in
      mysql)
        prompt user "用户名" "root"
        prompt password "密码（仅存本地不上报）" ""
        echo "请选择部署拓扑："
        choose "拓扑类型" 1 "单机(standalone)" "主从(replication)" "exporter(Prometheus)"
        local topo=""
        case "$CHOICE_VAL" in
          单机*) topo="standalone" ;;
          主从*) topo="replication" ;;
          exporter*) topo="standalone" ;;
        esac
        if [[ "$CHOICE_VAL" == exporter* ]]; then
          prompt exporter_url "exporter /metrics URL" "http://127.0.0.1:9104/metrics"
        fi
        extra_fields="topology"
        ;;
      postgres)
        prompt user "用户名" "postgres"
        prompt password "密码（仅存本地不上报）" ""
        local database ssl_mode
        prompt database "数据库名" "postgres"
        prompt ssl_mode "SSL 模式（disable/require/verify-ca/verify-full）" "disable"
        extra_fields="database: \"${database}\""$'\n'"    sslMode: \"${ssl_mode}\""$'\n'"    topology: \"standalone\""
        ;;
      nginx)
        local status_path
        prompt status_path "stub_status 路径（如 /nginx_status）" "/nginx_status"
        extra_fields="statusPath: \"${status_path}\""
        ;;
      kafka)
        local version
        prompt version "Kafka 版本（如 2.8.0，用于展示）" "2.8.0"
        extra_fields="version: \"${version}\""
        ;;
      rocketmq)
        extra_fields=""
        ;;
    esac

    # 生成 YAML 片段
    instances_yaml+="  - name: \"${name}\""$'\n'
    instances_yaml+="    addr: \"${addr}\""$'\n'
    if [[ -n "$user" ]]; then
      instances_yaml+="    user: \"${user}\""$'\n'
    fi
    if [[ -n "$password" ]]; then
      instances_yaml+="    password: \"${password}\""$'\n'
    fi
    if [[ "$mw_type" == "nginx" && -n "$status_path" ]]; then
      instances_yaml+="    statusPath: \"${status_path}\""$'\n'
    fi
    if [[ "$mw_type" == "postgres" ]]; then
      instances_yaml+="    database: \"${database}\""$'\n'
      instances_yaml+="    sslMode: \"${ssl_mode}\""$'\n'
      instances_yaml+="    topology: \"standalone\""$'\n'
    fi
    if [[ "$mw_type" == "mysql" ]]; then
      instances_yaml+="    topology: \"${topo}\""$'\n'
    fi
    if [[ "$mw_type" == "kafka" && -n "$version" ]]; then
      instances_yaml+="    version: \"${version}\""$'\n'
    fi
    if [[ -n "$exporter_url" ]]; then
      instances_yaml+="    exporterURL: \"${exporter_url}\""$'\n'
    fi

    c_ok "已添加实例: $name ($addr)"

    if ! confirm "是否继续添加下一个 ${mw_name} 实例？" "no"; then
      break
    fi
  done

  if [[ -z "$instances_yaml" ]]; then
    c_warn "未添加任何 ${mw_name} 实例，配置未变更。"
    exit 0
  fi

  echo
  c_info "即将写入以下 ${mw_name} 配置到 $cfg："
  echo "----------------------------------------"
  printf 'collectors:\n  %s: true\n\n%s:\n%s' "$mw_collector" "$mw_instances_key" "$instances_yaml"
  echo "----------------------------------------"

  if ! confirm "确认写入并重启 agent？" "yes"; then
    echo "已取消，配置未变更。"
    exit 0
  fi

  # 步骤1：开启 collectors
  if grep -qE "^[[:space:]]*${mw_collector}:" "$cfg" 2>/dev/null; then
    sed -i -E "s/^([[:space:]]*)${mw_collector}:[[:space:]]*.*/\\1${mw_collector}: true/" "$cfg"
  elif grep -qE '^[[:space:]]*collectors:' "$cfg" 2>/dev/null; then
    sed -i -E "/^[[:space:]]*collectors:/a\  ${mw_collector}: true" "$cfg"
  else
    printf "\ncollectors:\n  %s: true\n" "$mw_collector" >> "$cfg"
  fi
  c_ok "已开启 collectors.${mw_collector}"

  # 步骤2：写入 instances 段
  local tmp_file
  tmp_file="$(mktemp)"
  awk -v found=0 -v key="${mw_instances_key}:" '
    $0 ~ "^" key { found=1; next }
    found == 1 && /^[[:space:]]+/ { next }
    found == 1 && /^[^[:space:]]/ { found=0 }
    found == 0 { print }
  ' "$cfg" > "$tmp_file"
  mv "$tmp_file" "$cfg"

  printf '\n%s:\n%s' "$mw_instances_key" "$instances_yaml" >> "$cfg"
  c_ok "已写入 ${mw_instances_key} 配置"

  # 重启 agent
  if have_cmd systemctl; then
    c_info "重启 monitor-agent 服务..."
    systemctl restart monitor-agent 2>/dev/null || true
    sleep 2
    local st
    st="$(systemctl is-active monitor-agent 2>/dev/null || echo unknown)"
    if [[ "$st" == "active" ]]; then
      c_ok "Agent 已重启并运行中"
    else
      c_warn "Agent 服务状态: $st，请查看日志: journalctl -u monitor-agent -n 50"
    fi
  else
    c_warn "未检测到 systemctl，请手动重启 agent 进程"
  fi

  echo
  echo "============================================================"
  echo " ${mw_name} 监控配置完成"
  echo "------------------------------------------------------------"
  echo " 配置文件   : $cfg"
  echo " 查看日志   : journalctl -u monitor-agent -f | grep -i ${mw_collector}"
  echo " Web 端     : 中间件监控 → ${mw_name} Tab 查看实例"
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
  install_self_script
  write_service
  start_service
  connectivity_check
  summary
}

# 子命令分发：redis/mysql/postgres/nginx/kafka/rocketmq → 配置中间件监控；否则走默认安装流程
case "$SUBCOMMAND" in
  redis)    redis_config ;;
  mysql)    middleware_config mysql ;;
  postgres) middleware_config postgres ;;
  nginx)    middleware_config nginx ;;
  kafka)    middleware_config kafka ;;
  rocketmq) middleware_config rocketmq ;;
  *)        main ;;
esac
