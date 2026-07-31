#!/usr/bin/env bash
# 为指定架构打包 Agent 离线安装 tar 包。
# 用法: ./package-agent.sh <arch>   (arch: amd64 | arm64 | arm，默认 amd64)
# 产物: dist/agent-linux-<arch>.tar.gz，含二进制 + 示例配置 + systemd 单元 + install.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:-amd64}"
DIST_DIR="${ROOT_DIR}/dist"
BIN="${DIST_DIR}/agent/linux/${ARCH}/agent"
PKG_DIR="${DIST_DIR}/agent-pkg-${ARCH}"
OUT="${DIST_DIR}/agent-linux-${ARCH}.tar.gz"

if [[ ! -f "${BIN}" ]]; then
  echo "!! 未找到二进制 ${BIN}，请先运行 build/cross-compile.sh" >&2
  exit 1
fi

rm -rf "${PKG_DIR}"
mkdir -p "${PKG_DIR}/bin" "${PKG_DIR}/config"

cp "${BIN}" "${PKG_DIR}/bin/agent"

cat > "${PKG_DIR}/config/agent.yaml" <<'YAML'
serverURL: http://127.0.0.1:8080
group: default
interval: 15
collectors:
  cpu: true
  memory: true
  disk: true
  network: true
  process: true
  load: true
  # === 中间件监控（默认关闭，按需开启；实例配置见下方示例）===
  redis: false
  mysql: false
  postgres: false
  nginx: false
  kafka: false
  docker: false
  rocketmq: false
  # === TCP 端口存活检测（默认关闭）===
  port: false

# ==================== 中间件实例配置示例 ====================
# 以下示例默认全部以注释形式给出，请按需取消注释、开启上方对应 collectors 开关后填写。
# 注意：各中间件密码仅存本机，不上报 Server。

# Redis：支持 standalone / replication / sentinel / cluster 四种拓扑
# redisInstances:
#   - name: "redis-standalone"
#     addr: "127.0.0.1:6379"
#     password: "yourpassword"     # 仅存本地，不上报 Server
#     topology: "standalone"
#   - name: "redis-sentinel"
#     addr: "127.0.0.1:26379"
#     password: "yourpassword"
#     topology: "sentinel"
#     sentinelName: "mymaster"
#   - name: "redis-cluster"
#     addr: "127.0.0.1:7000"
#     password: "yourpassword"
#     topology: "cluster"
#   - name: "redis-exporter"
#     addr: "127.0.0.1:6379"
#     password: ""
#     topology: "standalone"
#     exporterURL: "http://127.0.0.1:9121/metrics"

# MySQL：支持 standalone / replication
# mysqlInstances:
#   - name: "mysql-master"
#     addr: "127.0.0.1:3306"
#     user: "monitor"
#     password: "yourpassword"     # 仅存本地，不上报 Server
#     topology: "standalone"
#   - name: "mysql-exporter"
#     addr: "127.0.0.1:3306"
#     user: "monitor"
#     password: ""
#     topology: "standalone"
#     exporterURL: "http://127.0.0.1:9104/metrics"

# PostgreSQL：支持 standalone / replication
# postgresInstances:
#   - name: "pg-primary"
#     addr: "127.0.0.1:5432"
#     database: "postgres"
#     user: "monitor"
#     password: "yourpassword"     # 仅存本地，不上报 Server
#     sslMode: "disable"
#     topology: "standalone"
#   - name: "pg-exporter"
#     addr: "127.0.0.1:5432"
#     database: "postgres"
#     user: "monitor"
#     password: ""
#     sslMode: "disable"
#     topology: "standalone"
#     exporterURL: "http://127.0.0.1:9187/metrics"

# Nginx
# nginxInstances:
#   - name: "nginx-01"
#     addr: "127.0.0.1:80"
#     statusPath: "/nginx_status"
#   - name: "nginx-vts"
#     addr: "127.0.0.1:80"
#     statusPath: "/nginx_status"
#     exporterURL: "http://127.0.0.1:9913/metrics"

# Kafka（addr 填任一 Broker 地址）
# kafkaInstances:
#   - name: "kafka-cluster"
#     addr: "127.0.0.1:9092"
#     version: "2.8.0"
#   - name: "kafka-exporter"
#     addr: "127.0.0.1:9092"
#     version: "2.8.0"
#     exporterURL: "http://127.0.0.1:9308/metrics"

# Docker（本地用 unix socket，远程用 tcp://host:2375）
# dockerInstances:
#   - name: "local-docker"
#     addr: "unix:///var/run/docker.sock"

# RocketMQ（addr 填 NameServer 地址）
# rocketmqInstances:
#   - name: "rocketmq-cluster"
#     addr: "127.0.0.1:9876"
#   - name: "rocketmq-exporter"
#     addr: "127.0.0.1:9876"
#     exporterURL: "http://127.0.0.1:5557/metrics"

# TCP 端口存活检测（开启 collectors.port 后生效）
# portChecks:
#   - "80"
#   - "443"
#   - "3306"
YAML

cat > "${PKG_DIR}/agent.service" <<'UNIT'
[Unit]
Description=Monitor Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/monitor-agent -config /etc/monitor-agent/agent.yaml
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
UNIT

cat > "${PKG_DIR}/install.sh" <<'INSTALL'
#!/usr/bin/env bash
# Agent 离线安装脚本：注册为 systemd 服务。
set -euo pipefail
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install -Dm755 "${SRC}/bin/agent" /usr/local/bin/monitor-agent
install -Dm644 "${SRC}/config/agent.yaml" /etc/monitor-agent/agent.yaml
install -Dm644 "${SRC}/agent.service" /etc/systemd/system/monitor-agent.service
systemctl daemon-reload
systemctl enable --now monitor-agent
echo ">> monitor-agent 已安装并启动"
INSTALL
chmod +x "${PKG_DIR}/install.sh"

tar -czf "${OUT}" -C "${PKG_DIR}" .
echo ">> 已生成离线包: ${OUT}"
