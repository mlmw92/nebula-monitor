#!/usr/bin/env bash
# ----------------------------------------------------------------------------
# nebula-monitor 离线发布包组装脚本
#   产出两种 tarball 到 dist/release/：
#     1) -full       首次部署用（bin + web + deploy + packages + install.sh）
#     2) -upgrade    增量升级用（仅 bin + web + VERSION + SHA256SUMS + UPGRADE.md）
#
# 用法: ./build/release.sh
# 前置:
#   - ./build/cross-compile.sh   生成 dist/artifacts/bin/
#   - ./build/build-web.sh       生成 dist/artifacts/web/
#   - 第三方依赖（node / vm）放入 dist/artifacts/packages/（仅 full 必需）
#     可用 ./build/fetch-packages.sh 自动拉取（CI 常用）
#
# 输出:
#   dist/release/nebula-monitor-v{VERSION}-full.tar.gz
#   dist/release/nebula-monitor-v{VERSION}-upgrade.tar.gz
# ----------------------------------------------------------------------------
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="$(bash "$ROOT/build/version.sh")"
NAME="nebula-monitor-v${VERSION}"
STAGE_ROOT="${ROOT}/dist/release"
OUT_FULL="${STAGE_ROOT}/${NAME}-full.tar.gz"
OUT_UPGRADE="${STAGE_ROOT}/${NAME}-upgrade.tar.gz"

c_info() { printf '\033[36m[步骤]\033[0m %s\n' "$*"; }
c_ok()   { printf '\033[32m[完成]\033[0m %s\n' "$*"; }
c_warn() { printf '\033[33m[警告]\033[0m %s\n' "$*"; }
die()    { printf '\033[31m[错误]\033[0m %s\n' "$*"; exit 1; }

# ============================ 前置检查 ============================
# 注：NTFS 不一定保留 unix +x 位，所以用"存在+非空"判即可；chmod 会在拷贝后补上。
BIN_DIR="dist/artifacts/bin"
WEB_DIR="dist/artifacts/web"
PKG_DIR="dist/artifacts/packages"
[[ -s "${BIN_DIR}/server/linux/amd64/server" ]] \
  || die "缺少 ${BIN_DIR}/server/linux/amd64/server，请先运行 build/cross-compile.sh"
[[ -s "${BIN_DIR}/agent/linux/amd64/agent" ]] \
  || die "缺少 ${BIN_DIR}/agent/linux/amd64/agent，请先运行 build/cross-compile.sh"
[[ -s "${WEB_DIR}/index.html" ]] \
  || die "缺少 ${WEB_DIR}/index.html，请先运行 build/build-web.sh"

# ============================ 准备 full 包 stage ============================
# 让 stage 目录直接命名为包顶层目录名（${NAME}-full），打包时无需 -s 重命名
c_info "组装 ${NAME}-full"
STAGE_FULL="${STAGE_ROOT}/${NAME}-full"
rm -rf "$STAGE_FULL"
mkdir -p "$STAGE_FULL/bin" "$STAGE_FULL/web" "$STAGE_FULL/deploy" "$STAGE_FULL/packages"

# 1) 二进制（保持 install-server.sh --dist 期望的 bin/{server,agent}/linux/<arch>/）
cp -a "$BIN_DIR/server" "$STAGE_FULL/bin/"
cp -a "$BIN_DIR/agent"  "$STAGE_FULL/bin/"
chmod +x "$STAGE_FULL/bin/server/linux/"*"/server" "$STAGE_FULL/bin/agent/linux/"*"/agent"

# 2) 前端（web 已平铺，直接把所有内容复制到 stage/web/）
cp -a "$WEB_DIR"/. "$STAGE_FULL/web/"

# 3) 部署脚本（含 docker/ 子目录）
cp -a deploy/install-server.sh  "$STAGE_FULL/deploy/"
cp -a deploy/install-tsdb.sh    "$STAGE_FULL/deploy/"
cp -a deploy/agent-install.sh   "$STAGE_FULL/deploy/"
[[ -d deploy/docker ]] && cp -a deploy/docker "$STAGE_FULL/deploy/"
chmod +x "$STAGE_FULL/deploy/"*.sh

# 4) 可选依赖（node / vm tarball；与 install-server.sh install-tsdb.sh 兼容）
shopt -s nullglob
any_pkg=0
for f in "$PKG_DIR"/node-*.tar.xz "$PKG_DIR"/victoria-metrics-*.tar.gz; do
  cp -a "$f" "$STAGE_FULL/packages/"
  any_pkg=1
done
shopt -u nullglob
if (( !any_pkg )); then
  c_warn "未在 ${PKG_DIR}/ 找到 node / victoria-metrics tarball（full 包将不含相关可选依赖）"
fi

# 5) 统一入口 install.sh（透传 --dist/--packages 指向包内目录）
cat > "$STAGE_FULL/install.sh" <<'EOF'
#!/usr/bin/env bash
# nebula-monitor 统一安装入口
#   ./install.sh server  [...]   部署/升级 Server（透传给 deploy/install-server.sh）
#   ./install.sh agent   [...]   安装 Agent（透传给 deploy/agent-install.sh）
# 全部参数透传给 deploy/ 下的原始脚本；可用 --help 查看子命令帮助。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
usage() {
  cat <<USAGE
用法:
  ./install.sh server  [参数]   部署/升级 Server（透传给 deploy/install-server.sh）
  ./install.sh agent   [参数]   安装 Agent（透传给 deploy/agent-install.sh）

完整参数请用：
  ./install.sh server --help
  ./install.sh agent  --help

本入口会自动设置 --packages/--dist 指向解压目录，所以子命令只需要指定
tsdb 地址、监听端口、密钥等业务参数即可。
USAGE
}
cmd="${1:-}"
if [[ -z "$cmd" || "$cmd" == "-h" || "$cmd" == "--help" ]]; then
  usage; exit 0
fi
shift
case "$cmd" in
  server)
    exec sudo bash "$HERE/deploy/install-server.sh" \
      --packages "$HERE/packages" --dist "$HERE/bin" "$@"
    ;;
  agent)
    exec sudo bash "$HERE/deploy/agent-install.sh" "$@"
    ;;
  *)
    echo "未知子命令: $cmd（支持 server / agent）" >&2
    usage; exit 2
    ;;
esac
EOF
chmod +x "$STAGE_FULL/install.sh"

# 6) VERSION + README
printf '%s\n' "$VERSION" > "$STAGE_FULL/VERSION"

cat > "$STAGE_FULL/README.md" <<EOF
# nebula-monitor v${VERSION} 全量离线发布包

下载后 \`tar -xzf\` 解压到任意目录即可。三步完成部署：

## 1) 部署 Server（含自带 CDN，向已部署的时序库写入）

\`\`\`
tar -xzf nebula-monitor-v${VERSION}-full.tar.gz
cd ${NAME}-full
sudo ./install.sh server \\
    --listen :8080 \\
    --tsdb-backend victoriametrics \\
    --tsdb-addr http://10.0.0.10:8428 \\
    --agent-auth \\
    --yes
\`\`\`

> \`--agent-auth\` 会自动生成一个 agent 接入密钥并在结束时打印。务必记下，
> 后面装 agent 时要用 \`--secret\` 传入同一个密钥。

## 2) 在被监控节点上安装 Agent

\`\`\`
sudo ./install.sh agent \\
    --server http://10.0.0.10:8080 \\
    --secret <上面打印的密钥>
\`\`\`

## 3) 升级已有部署

> 升级请使用 **upgrade 包**（仅含 bin + web，体积小）。如需复用本包的 install.sh 自动化：
> \`sudo ./install.sh server --upgrade\` 会保留 server.yaml 重启服务。

## 目录说明

| 路径 | 用途 |
|---|---|
| \`install.sh\` | 统一入口（server / agent 子命令） |
| \`deploy/install-server.sh\` | 原始 Server 安装/升级脚本 |
| \`deploy/install-tsdb.sh\` | 时序库本地安装脚本 |
| \`deploy/agent-install.sh\` | 原始 Agent 安装脚本 |
| \`deploy/docker/\` | Docker 部署示例 |
| \`bin/server/linux/<arch>/server\` | Server 离线二进制 |
| \`bin/agent/linux/<arch>/agent\` | Agent 离线二进制 |
| \`web/\` | 前端构建产物（部署到 /etc/monitor-server/web） |
| \`packages/node-*.tar.xz\` | 可选（前端构建用） |
| \`packages/victoria-metrics-*.tar.gz\` | 可选（时序库本地安装用） |
| \`SHA256SUMS\` | 所有二进制、脚本、前端的 sha256 |

## 完整性校验

\`\`\`
sha256sum -c SHA256SUMS
\`\`\`
EOF

# 7) SHA256SUMS（仅对二进制、脚本、前端）
c_info "生成 SHA256SUMS (full)"
( cd "$STAGE_FULL" && \
  find bin deploy install.sh VERSION web -type f -print0 2>/dev/null \
    | xargs -0 sha256sum > SHA256SUMS )

# 8) 打包
c_info "打包 $(basename "$OUT_FULL")"
mkdir -p "$STAGE_ROOT"
rm -f "$OUT_FULL"
tar -czf "$OUT_FULL" -C "$STAGE_ROOT" "${NAME}-full"

# ============================ 准备 upgrade 包 stage ============================
c_info "组装 ${NAME}-upgrade"
STAGE_UPGRADE="${STAGE_ROOT}/${NAME}-upgrade"
rm -rf "$STAGE_UPGRADE"
mkdir -p "$STAGE_UPGRADE/bin" "$STAGE_UPGRADE/web"

# 1) 二进制
cp -a "$BIN_DIR/server" "$STAGE_UPGRADE/bin/"
cp -a "$BIN_DIR/agent"  "$STAGE_UPGRADE/bin/"
chmod +x "$STAGE_UPGRADE/bin/server/linux/"*"/server" "$STAGE_UPGRADE/bin/agent/linux/"*"/agent"

# 2) 前端
cp -a "$WEB_DIR"/. "$STAGE_UPGRADE/web/"

# 3) VERSION + UPGRADE.md
printf '%s\n' "$VERSION" > "$STAGE_UPGRADE/VERSION"

cat > "$STAGE_UPGRADE/UPGRADE.md" <<EOF
# nebula-monitor v${VERSION} 升级包（增量）

本包仅包含**编译后的产物**（Server / Agent 二进制 + 前端 web）。  
**不包含**部署脚本、install.sh、可选依赖（node / victoria-metrics），避免与现有部署版本错配。

> 升级会替换 Server 二进制并重启 \`monitor-server\` 服务，期间 web 端短暂不可用（约 5-15 秒）。

## 一、升级 Server（这台机器）

\`\`\`
# 1. 解压
tar -xzf nebula-monitor-v${VERSION}-upgrade.tar.gz
cd ${NAME}-upgrade

# 2. 替换 server 二进制（按本机架构取对应文件）
sudo cp bin/server/linux/amd64/server /usr/local/bin/monitor-server
# 也可: sudo install -m 0755 bin/server/linux/<arch>/server /usr/local/bin/monitor-server

# 3. 替换前端（Server 从 /etc/monitor-server/web 读取）
sudo rsync -a --delete web/ /etc/monitor-server/web/

# 4. 重启
sudo systemctl restart monitor-server
\`\`\`

## 二、升级 Agent（在被监控节点上）

升级包内已含新版本 Agent 二进制（按节点架构取对应文件）。手动替换：

\`\`\`
# 在被监控节点上
sudo cp <从升级包获取的>bin/agent/linux/<arch>/agent /usr/local/bin/monitor-agent
sudo systemctl restart monitor-agent
\`\`\`

> 也可在升级 Server 后访问 Server 自带 CDN：
> \`curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- --server http://<server>:8080 [--secret <KEY>]\`
> 此命令会自动下载新 Agent 并重启。

## 三、自动化（可选）

如需 web 端自动化升级，可调用：
\`\`\`
# 在装有同版本 full 包的机器上
sudo ./install.sh server --upgrade
# 或脚本内调用 deploy/install-server.sh --upgrade
\`\`\`
其行为与上面手动步骤一致。

## 四、完整性校验

\`\`\`
sha256sum -c SHA256SUMS
\`\`\`
EOF

# 4) SHA256SUMS
c_info "生成 SHA256SUMS (upgrade)"
( cd "$STAGE_UPGRADE" && \
  find bin web -type f -print0 2>/dev/null \
    | xargs -0 sha256sum > SHA256SUMS )

# 5) 打包
c_info "打包 $(basename "$OUT_UPGRADE")"
rm -f "$OUT_UPGRADE"
tar -czf "$OUT_UPGRADE" -C "$STAGE_ROOT" "${NAME}-upgrade"

# ============================ 报告 ============================
echo
c_ok "发布物已生成："
ls -lh "$OUT_FULL" "$OUT_UPGRADE"
echo
echo "  full 包    顶层条目（$(tar -tzf "$OUT_FULL" | wc -l) 项）："
tar -tzf "$OUT_FULL" | awk -F/ 'NR<=15{print "    " $0} NR==16{print "    ..."; exit}'
echo
echo "  upgrade 包 顶层条目（$(tar -tzf "$OUT_UPGRADE" | wc -l) 项）："
tar -tzf "$OUT_UPGRADE" | awk -F/ 'NR<=15{print "    " $0} NR==16{print "    ..."; exit}'
echo
c_ok "完成。可将上述 tarball 分发给运维；GitHub Release 自动化见 .github/workflows/release.yml"
