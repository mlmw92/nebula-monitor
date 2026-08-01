// Package upgrader handles self-upgrade of the agent by downloading a new
// binary from the server and replacing the current one.
package upgrader

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/nebula/monitor/internal/agent/config"
)

// ReadyFile 是升级信号文件路径：非 systemd 环境下，升级脚本下载校验完成后
// 写入该文件，agent 主进程检测到后自行退出，以便脚本替换二进制。
const ReadyFile = "/tmp/monitor-agent-upgrade.ready"

// upgrading 防止同一时刻重复启动多个升级脚本（脚本内还有 flock 兜底）。
var upgrading atomic.Bool

// Run downloads the latest agent binary for the current architecture from the
// server, verifies its SHA256 checksum (and HMAC signature when a secret is
// shared), then replaces the running binary and restarts the service.
//
// 关键设计：
//  1. 脚本必须脱离 agent 的 cgroup/session，否则 agent 退出时 systemd
//     （默认 KillMode=control-group）会杀掉整个 cgroup，升级脚本也被杀，
//     因此用 setsid 启动独立会话。
//  2. 脚本先下载并校验新二进制（此时 agent 继续正常运行与上报），准备就绪
//     后再停止 agent 并替换，避免"等待退出 -> 下载"的无效阻塞与 ETXTBSY。
//  3. Run 本身不阻塞、不退出进程：失败时 agent 保持运行，等待 Server 下次
//     心跳重试；成功后由脚本（systemctl stop / 信号文件）停止本进程。
func Run(cfg *config.Config) {
	if !upgrading.CompareAndSwap(false, true) {
		slog.Warn("已有升级任务进行中，忽略本次升级指令")
		return
	}
	defer upgrading.Store(false)

	secret := cfg.Secret
	serverURL := strings.TrimRight(cfg.ServerURL, "/")
	binPath, err := os.Executable()
	if err != nil {
		slog.Warn("获取自身二进制路径失败", "err", err)
		return
	}

	url := fmt.Sprintf("%s/bin/linux/%s/agent", serverURL, runtime.GOARCH)
	script := buildUpgradeScript(serverURL, url, binPath, secret)

	// 脚本写到 /tmp（不放在 bin 目录，避免权限问题）
	tmp, err := os.CreateTemp("/tmp", "monitor-agent-upgrade-*.sh")
	if err != nil {
		slog.Warn("创建升级脚本失败", "err", err)
		return
	}
	if _, err := tmp.WriteString(script); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	_ = os.Chmod(tmp.Name(), 0o700)

	// 用 setsid 启动独立会话，脱离 agent 的 cgroup/process group。
	// 这样 agent 退出（或被 systemd 停止）时，升级脚本不会被一并杀掉。
	// 日志写到固定文件，便于排查。
	logFile := "/var/log/monitor-agent-upgrade.log"
	cmd := exec.Command("setsid", "bash", "-c",
		fmt.Sprintf("bash %q >%q 2>&1 &", tmp.Name(), logFile))
	// Setpgid 确保子进程独立进程组（仅 Linux 平台生效）
	if attr, ok := setSysProcAttr().(*syscall.SysProcAttr); ok {
		cmd.SysProcAttr = attr
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("启动升级脚本失败", "err", err)
	}
}

// buildUpgradeScript returns a bash script that downloads the new agent binary,
// verifies its checksum/signature, replaces the running binary and restarts.
//
// 脚本执行顺序：
//  1. flock 防并发（同一时间只允许一个升级脚本）
//  2. 下载新二进制 + 校验 SHA256/HMAC（此时 agent 仍正常运行，失败不伤及 agent）
//  3. systemd 环境：systemctl stop -> 备份 -> mv 替换 -> systemctl start
//     -> 校验服务存活，失败则回滚旧二进制
//  4. 非 systemd 环境：写信号文件 -> agent 自行退出 -> 备份 -> mv 替换
//     -> nohup 拉起新进程
//  5. 同步 agent-install.sh 到本地配置目录
func buildUpgradeScript(serverURL, url, binPath, secret string) string {
	serverEsc := strings.ReplaceAll(serverURL, "'", `'\''`)
	urlEsc := strings.ReplaceAll(url, "'", `'\''`)
	binEsc := strings.ReplaceAll(binPath, "'", `'\''`)
	secEsc := strings.ReplaceAll(secret, "'", `'\''`)

	return fmt.Sprintf(`#!/usr/bin/env bash
set -e
SCRIPT="$0"
trap 'rm -f "$SCRIPT"' EXIT

LOG() { echo "[$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] $*"; }

BIN='%s'
DIR="$(dirname "$BIN")"
TMP="$(mktemp "$DIR/.agent-upgrade-bin.XXXXXX")"
URL='%s'
SERVER='%s'
SECRET='%s'
# 升级信号文件（非 systemd 环境使用）与并发锁
READY='/tmp/monitor-agent-upgrade.ready'
LOCK='/tmp/monitor-agent-upgrade.lock'
# 配置目录与脚本安装路径（与 agent-install.sh 的 CONFIG_DIR 一致）
CONF_DIR="/etc/monitor-agent"
INSTALL_SCRIPT="$CONF_DIR/agent-install.sh"
# agent-install.sh 在 Server 上的顶层路由：{server}/install/agent-install.sh
SCRIPT_URL="${SERVER%%/}/install/agent-install.sh"
MAX=3

LOG "=== agent 升级脚本启动 ==="
LOG "BIN=$BIN URL=$URL PID=$$"

# 防并发：同一时间只允许一个升级脚本（flock 不可用则跳过该保护）
if command -v flock >/dev/null 2>&1; then
  exec 9>"$LOCK"
  if ! flock -n 9; then
    LOG "另一个升级脚本正在运行，本次跳过"
    exit 0
  fi
fi

# ---------- 下载与校验阶段：agent 仍正常运行，失败可直接退出，agent 不受影响 ----------
if [ -n "$SECRET" ]; then HDR=(-H "X-Agent-Secret: $SECRET"); else HDR=(); fi

ok=0
for i in $(seq 1 $MAX); do
  LOG "下载二进制 (尝试 $i/$MAX): $URL"
  if curl -fsSL "${HDR[@]}" "$URL" -o "$TMP"; then ok=1; LOG "下载成功"; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then LOG "错误: 二进制下载失败，agent 保持运行，等待下次重试"; rm -f "$TMP"; exit 1; fi

sumok=0
for i in $(seq 1 $MAX); do
  LOG "下载校验和 (尝试 $i/$MAX): $URL.sha256"
  if curl -fsSL "${HDR[@]}" "$URL.sha256" -o "$TMP.sha256"; then sumok=1; LOG "校验和下载成功"; break; fi
  sleep 2
done
if [ "$sumok" -ne 1 ]; then LOG "错误: 校验和下载失败，agent 保持运行，等待下次重试"; rm -f "$TMP"; exit 1; fi

chk=$(grep -oE '"checksum"[[:space:]]*:[[:space:]]*"[^"]+"' "$TMP.sha256" | sed -E 's/.*:"([^"]+)"/\1/')
sig=$(grep -oE '"sig"[[:space:]]*:[[:space:]]*"[^"]+"' "$TMP.sha256" | sed -E 's/.*:"([^"]+)"/\1/')
if [ -z "$chk" ]; then LOG "错误: 校验和缺失"; rm -f "$TMP" "$TMP.sha256"; exit 1; fi

localchk=$(sha256sum "$TMP" | awk '{print $1}')
if [ "$localchk" != "$chk" ]; then
  LOG "错误: 校验和不匹配 local=$localchk remote=$chk"
  rm -f "$TMP" "$TMP.sha256"; exit 1
fi
LOG "校验和验证通过"

if [ -n "$SECRET" ] && [ -n "$sig" ]; then
  if command -v openssl >/dev/null 2>&1; then
    calc=$(printf '%%s' "$chk" | openssl dgst -sha256 -hmac "$SECRET" -r | awk '{print $1}')
    if [ "$calc" != "$sig" ]; then
      LOG "错误: 签名不匹配"; rm -f "$TMP" "$TMP.sha256"; exit 1
    fi
    LOG "签名验证通过"
  else
    LOG "警告: openssl 不可用，跳过签名校验"
  fi
fi

chmod +x "$TMP"

# ---------- 停止旧进程（准备替换） ----------
if command -v systemctl >/dev/null 2>&1; then
  LOG "停止 monitor-agent 服务..."
  systemctl stop monitor-agent 2>/dev/null || true
  sleep 1
else
  # 非 systemd：写信号文件，等待 agent 自行退出（agent 启动后每 2s 检查该文件）
  LOG "非 systemd 环境，写入升级信号文件，等待 agent 退出..."
  echo "$BIN" > "$READY"
  SELF_PID=$(cat /var/run/monitor-agent.pid 2>/dev/null || echo "")
  waited=0
  while [ -n "$SELF_PID" ] && kill -0 "$SELF_PID" 2>/dev/null && [ "$waited" -lt 60 ]; do
    sleep 1
    waited=$((waited+1))
  done
  if [ -n "$SELF_PID" ] && kill -0 "$SELF_PID" 2>/dev/null; then
    LOG "等待 agent 退出超时，强制终止"
    kill -9 "$SELF_PID" 2>/dev/null || true
    sleep 2
  fi
  rm -f "$READY"
  LOG "agent 已退出"
fi

# ---------- 备份旧二进制，替换新二进制（带重试，处理 ETXTBSY） ----------
BACKUP="$DIR/.monitor-agent.old.$$"
if [ -f "$BIN" ]; then
  mv -f "$BIN" "$BACKUP" 2>/dev/null || true
fi
replaced=0
for i in $(seq 1 5); do
  if mv -f "$TMP" "$BIN" 2>/dev/null; then replaced=1; LOG "替换成功"; break; fi
  LOG "替换失败 (尝试 $i/5)，等待重试..."
  sleep 1
done
if [ "$replaced" -ne 1 ]; then
  LOG "错误: 二进制替换最终失败"
  [ -f "$BACKUP" ] && mv -f "$BACKUP" "$BIN" 2>/dev/null || true
  rm -f "$TMP"
  exit 1
fi
rm -f "$TMP.sha256"

# ---------- 启动新版本并校验；失败则回滚旧二进制 ----------
if command -v systemctl >/dev/null 2>&1; then
  LOG "启动 monitor-agent 服务..."
  systemctl start monitor-agent 2>/dev/null || true
  sleep 2
  if systemctl is-active --quiet monitor-agent 2>/dev/null; then
    rm -f "$BACKUP"
    LOG "服务状态: active（新版本运行正常）"
  else
    LOG "错误: 新版本启动失败，回滚到旧版本"
    mv -f "$BIN" "$TMP" 2>/dev/null || true
    [ -f "$BACKUP" ] && mv -f "$BACKUP" "$BIN" 2>/dev/null || true
    systemctl start monitor-agent 2>/dev/null || true
    rm -f "$TMP"
    exit 1
  fi
else
  # 非 systemd：尽力拉起新进程（配置路径与 agent-install.sh 保持一致）
  rm -f "$BACKUP"
  if [ -f /etc/monitor-agent/agent.yaml ]; then
    nohup "$BIN" -config /etc/monitor-agent/agent.yaml >/dev/null 2>&1 &
    LOG "已尝试拉起新 agent（nohup 模式）"
  else
    LOG "警告: 未找到 /etc/monitor-agent/agent.yaml，请手动启动新 agent: $BIN -config <config>"
  fi
fi

# ---------- 同步 agent-install.sh 到本地配置目录（方便用户后续执行 redis 子命令等配置操作） ----------
# 首次安装若走 curl|bash 管道，本地不会残留脚本；此处补齐。
mkdir -p "$CONF_DIR" 2>/dev/null || true
LOG "同步配置脚本: $SCRIPT_URL -> $INSTALL_SCRIPT"
if curl -fsSL "${HDR[@]}" "$SCRIPT_URL" -o "$INSTALL_SCRIPT" 2>/dev/null; then
  chmod +x "$INSTALL_SCRIPT" 2>/dev/null || true
  LOG "配置脚本已同步: $INSTALL_SCRIPT"
else
  LOG "警告: 未能同步 agent-install.sh（不影响 Agent 运行；可手动 curl 下载）"
fi

LOG "=== agent 升级完成 ==="
`, binEsc, urlEsc, serverEsc, secEsc)
}
