#!/usr/bin/env bash
# nebula-monitor 卸载脚本
#   - 默认保留数据目录（仅停服务+删二进制+删配置），方便备份/恢复
#   - --purge 同时清理数据目录（不可逆！）
#   - --yes 跳过所有交互确认
#   - --all 卸载 server + agent + tsdb（默认根据探测到的服务决定）
#   - --server / --agent / --tsdb 限制范围
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 颜色
if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_YEL=$'\033[33m'; C_GRN=$'\033[32m'; C_BLU=$'\033[34m'; C_DIM=$'\033[2m'; C_RST=$'\033[0m'
else
  C_RED=""; C_YEL=""; C_GRN=""; C_BLU=""; C_DIM=""; C_RST=""
fi
c_info()  { echo -e "${C_BLU}[信息]${C_RST} $*"; }
c_ok()    { echo -e "${C_GRN}[完成]${C_RST} $*"; }
c_warn()  { echo -e "${C_YEL}[警告]${C_RST} $*"; }
c_err()   { echo -e "${C_RED}[错误]${C_RST} $*" >&2; }
c_dim()   { echo -e "${C_DIM}$*${C_RST}"; }

usage() {
  cat <<'USAGE'
用法:
  sudo ./uninstall.sh [选项]               # 自动探测后卸载
  sudo ./install.sh uninstall [选项]       # 通过统一入口调用（推荐）

选项:
  --all              卸载 server + agent + tsdb（默认行为：只卸探测到的）
  --server           仅卸载 server
  --agent            仅卸载 agent
  --tsdb             仅卸载时序库 (victoriametrics / mimir / cortex / thanos)
  --purge            同时清理数据目录（默认保留供备份）
  --yes              跳过所有交互确认
  -h | --help        显示本帮助

典型场景:
  # 保留数据，干净卸载所有组件
  sudo ./uninstall.sh

  # 完全清理（删数据，不可逆）
  sudo ./uninstall.sh --purge --yes

  # 只卸 server（agent 和 tsdb 保留）
  sudo ./uninstall.sh --server

USAGE
}

# ---------------------------- 参数解析 ----------------------------
PURGE=0
ASSUME_YES=0
DO_SERVER=0
DO_AGENT=0
DO_TSDB=0
DO_ALL=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --purge)      PURGE=1; shift ;;
    --yes|-y)     ASSUME_YES=1; shift ;;
    --all)        DO_ALL=1; shift ;;
    --server)     DO_SERVER=1; shift ;;
    --agent)      DO_AGENT=1; shift ;;
    --tsdb|--vm)  DO_TSDB=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *) c_err "未知参数: $1"; usage; exit 2 ;;
  esac
done
# --all 包含三项
if (( DO_ALL )); then
  DO_SERVER=1; DO_AGENT=1; DO_TSDB=1
fi
# 没指定任何范围 -> 进入自动探测
if (( ! DO_SERVER && ! DO_AGENT && ! DO_TSDB )); then
  DO_SERVER=1; DO_AGENT=1; DO_TSDB=1
fi

# 需要 root
if [[ $EUID -ne 0 ]]; then
  c_err "请用 root 或 sudo 执行（要删系统级二进制+systemd 单元）"
  exit 1
fi

# ---------------------------- 探测 ----------------------------
declare -a FOUND=()    # 探测到的组件

# Server
SERVER_SVC=0; [[ -f /etc/systemd/system/monitor-server.service ]] && SERVER_SVC=1
SERVER_BIN=0; [[ -x /usr/local/bin/monitor-server ]] && SERVER_BIN=1
SERVER_ETC=0; [[ -d /etc/monitor-server ]] && SERVER_ETC=1
SERVER_DATA=0; [[ -d /var/lib/monitor-server ]] && SERVER_DATA=1
SERVER_PRESENT=0
if (( SERVER_SVC || SERVER_BIN || SERVER_ETC )); then
  (( DO_SERVER )) && SERVER_PRESENT=1
fi

# Agent
AGENT_SVC=0; [[ -f /etc/systemd/system/monitor-agent.service ]] && AGENT_SVC=1
AGENT_BIN=0; [[ -x /usr/local/bin/monitor-agent ]] && AGENT_BIN=1
AGENT_ETC=0; [[ -d /etc/monitor-agent ]] && AGENT_ETC=1
AGENT_DATA=0; [[ -d /var/lib/monitor-agent ]] && AGENT_DATA=1
AGENT_PRESENT=0
if (( AGENT_SVC || AGENT_BIN || AGENT_ETC )); then
  (( DO_AGENT )) && AGENT_PRESENT=1
fi

# TSDB (VictoriaMetrics 二进制模式)
VM_SVC=0; [[ -f /etc/systemd/system/victoriametrics.service ]] && VM_SVC=1
VM_BIN=0; [[ -x /usr/local/bin/victoria-metrics ]] && VM_BIN=1
VM_ETC=0; [[ -d /etc/victoria-metrics ]] && VM_ETC=1
VM_DATA=0; [[ -d /var/lib/victoria-metrics-data ]] && VM_DATA=1
VM_OPT=0;  [[ -d /opt/victoria-metrics ]] && VM_OPT=1
VM_PRESENT=0
if (( VM_SVC || VM_BIN || VM_ETC )); then
  (( DO_TSDB )) && VM_PRESENT=1
fi

# TSDB (Mimir / Cortex / Thanos - Docker 模式)
DOCKER_TSDB_PRESENT=0
DOCKER_COMPOSE_FILE=""
if [[ -f docker-compose.tsdb.yml ]] || [[ -f /opt/nebula-monitor/docker-compose.tsdb.yml ]]; then
  [[ -f /opt/nebula-monitor/docker-compose.tsdb.yml ]] && DOCKER_COMPOSE_FILE=/opt/nebula-monitor/docker-compose.tsdb.yml
  [[ -f docker-compose.tsdb.yml ]] && DOCKER_COMPOSE_FILE="$(pwd)/docker-compose.tsdb.yml"
  (( DO_TSDB )) && DOCKER_TSDB_PRESENT=1
fi

# 检查是否真有任何东西要卸
ANY_PRESENT=0
if (( SERVER_PRESENT || AGENT_PRESENT || VM_PRESENT || DOCKER_TSDB_PRESENT )); then
  ANY_PRESENT=1
fi

# ---------------------------- 报告要删的内容 ----------------------------
echo
echo "================================================"
echo "  nebula-monitor 卸载预览"
echo "================================================"
if (( ! ANY_PRESENT )); then
  c_info "未检测到已安装组件，无需卸载"
  echo "  探测路径："
  echo "    Server:    /etc/monitor-server /usr/local/bin/monitor-server"
  echo "    Agent:     /etc/monitor-agent  /usr/local/bin/monitor-agent"
  echo "    VM:        /etc/victoria-metrics /usr/local/bin/victoria-metrics"
  echo "    Docker TSDB: docker-compose.tsdb.yml"
  exit 0
fi

if (( SERVER_PRESENT )); then
  echo -e "${C_BLU}Server (monitor-server)${C_RST}"
  (( SERVER_SVC  )) && echo "  - 服务:    monitor-server.service"
  (( SERVER_BIN  )) && echo "  - 二进制:  /usr/local/bin/monitor-server"
  (( SERVER_ETC  )) && echo "  - 配置:    /etc/monitor-server/"
  (( SERVER_DATA && PURGE )) && echo -e "  - ${C_RED}数据(将删):  /var/lib/monitor-server/${C_RST}"
  (( SERVER_DATA && !PURGE )) && echo -e "  - ${C_DIM}数据(保留):  /var/lib/monitor-server/${C_RST}"
fi
if (( AGENT_PRESENT )); then
  echo -e "${C_BLU}Agent (monitor-agent)${C_RST}"
  (( AGENT_SVC  )) && echo "  - 服务:    monitor-agent.service"
  (( AGENT_BIN  )) && echo "  - 二进制:  /usr/local/bin/monitor-agent"
  (( AGENT_ETC  )) && echo "  - 配置:    /etc/monitor-agent/"
  (( AGENT_DATA && PURGE )) && echo -e "  - ${C_RED}数据(将删):  /var/lib/monitor-agent/${C_RST}"
  (( AGENT_DATA && !PURGE )) && echo -e "  - ${C_DIM}数据(保留):  /var/lib/monitor-agent/${C_RST}"
fi
if (( VM_PRESENT )); then
  echo -e "${C_BLU}VictoriaMetrics${C_RST}"
  (( VM_SVC )) && echo "  - 服务:    victoriametrics.service"
  (( VM_BIN )) && echo "  - 二进制:  /usr/local/bin/victoria-metrics"
  (( VM_ETC )) && echo "  - 配置:    /etc/victoria-metrics/"
  (( VM_DATA && PURGE )) && echo -e "  - ${C_RED}数据(将删):  /var/lib/victoria-metrics-data/${C_RST}"
  (( VM_DATA && !PURGE )) && echo -e "  - ${C_DIM}数据(保留):  /var/lib/victoria-metrics-data/${C_RST}"
  (( VM_OPT  && PURGE )) && echo -e "  - ${C_RED}安装目录:    /opt/victoria-metrics/${C_RST}"
fi
if (( DOCKER_TSDB_PRESENT )); then
  echo -e "${C_BLU}Docker TSDB (Mimir / Cortex / Thanos)${C_RST}"
  echo "  - compose: $DOCKER_COMPOSE_FILE"
  if (( PURGE )); then
    echo "  - 将执行: docker compose down --volumes"
  else
    echo "  - 将执行: docker compose down (数据卷保留)"
  fi
fi
echo "================================================"
if (( ! PURGE )); then
  c_dim "数据目录默认保留（加 --purge 才会删除，不可逆）"
fi
echo

# ---------------------------- 确认 ----------------------------
if (( ! ASSUME_YES )); then
  read -r -p "确认卸载以上内容？[yes/N]: " ans
  case "$ans" in
    y|yes|Y|YES) ;;
    *) c_info "已取消"; exit 0 ;;
  esac
fi

# ---------------------------- 执行卸载 ----------------------------
# 工具函数
safe_rm() {
  # safe_rm <path>...
  local p
  for p in "$@"; do
    if [[ -e "$p" || -L "$p" ]]; then
      rm -rf -- "$p"
      c_ok "  删: $p"
    fi
  done
}
stop_disable() {
  local svc="$1"
  if systemctl list-unit-files "${svc}.service" 2>/dev/null | grep -q "^${svc}.service"; then
    systemctl stop "$svc" 2>/dev/null || true
    systemctl disable "$svc" 2>/dev/null || true
    c_ok "  停服务: $svc"
  fi
}

# 1) Server
if (( SERVER_PRESENT )); then
  echo
  c_info "卸载 Server..."
  stop_disable monitor-server
  safe_rm /usr/local/bin/monitor-server
  safe_rm /etc/systemd/system/monitor-server.service
  safe_rm /etc/monitor-server
  if (( PURGE )); then
    safe_rm /var/lib/monitor-server
  fi
  systemctl daemon-reload
  c_ok "Server 卸载完成"
fi

# 2) Agent
if (( AGENT_PRESENT )); then
  echo
  c_info "卸载 Agent..."
  stop_disable monitor-agent
  safe_rm /usr/local/bin/monitor-agent
  safe_rm /etc/systemd/system/monitor-agent.service
  safe_rm /etc/monitor-agent
  if (( PURGE )); then
    safe_rm /var/lib/monitor-agent
  fi
  systemctl daemon-reload
  c_ok "Agent 卸载完成"
fi

# 3) TSDB - VictoriaMetrics
if (( VM_PRESENT )); then
  echo
  c_info "卸载 VictoriaMetrics..."
  stop_disable victoriametrics
  safe_rm /usr/local/bin/victoria-metrics
  safe_rm /etc/systemd/system/victoria-metrics.service
  safe_rm /etc/victoria-metrics
  if (( PURGE )); then
    safe_rm /var/lib/victoria-metrics-data
    safe_rm /opt/victoria-metrics
  fi
  systemctl daemon-reload
  c_ok "VictoriaMetrics 卸载完成"
fi

# 4) TSDB - Docker (Mimir / Cortex / Thanos)
if (( DOCKER_TSDB_PRESENT )); then
  echo
  c_info "卸载 Docker TSDB..."
  if command -v docker >/dev/null 2>&1; then
    cd "$(dirname "$DOCKER_COMPOSE_FILE")"
    if (( PURGE )); then
      docker compose -f "$DOCKER_COMPOSE_FILE" down --volumes --remove-orphans 2>/dev/null || true
      c_ok "  停容器 + 删卷: $DOCKER_COMPOSE_FILE"
    else
      docker compose -f "$DOCKER_COMPOSE_FILE" down --remove-orphans 2>/dev/null || true
      c_ok "  停容器（卷保留）: $DOCKER_COMPOSE_FILE"
    fi
  else
    c_warn "docker 命令不可用，跳过容器清理"
  fi
  if (( PURGE )); then
    safe_rm "$DOCKER_COMPOSE_FILE"
  fi
  c_ok "Docker TSDB 卸载完成"
fi

echo
echo "================================================"
c_ok "卸载完成"
if (( ! PURGE )); then
  c_dim "数据目录已保留（备份后可手动 rm -rf 清理）"
fi
echo "================================================"
echo
c_info "如需重新安装："
c_dim "  curl -fsSL -O https://github.com/mlmw92/nebula-monitor/releases/download/<VER>/nebula-monitor-<VER>-full.tar.gz"
c_dim "  tar -xzf nebula-monitor-<VER>-full.tar.gz && cd nebula-monitor-<VER>-full"
c_dim "  sudo ./install.sh"
