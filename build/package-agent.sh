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
