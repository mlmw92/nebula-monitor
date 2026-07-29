// Package upgrader handles self-upgrade of the agent by downloading a new
// binary from the server and replacing the current one.
package upgrader

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/nebula/monitor/internal/agent/config"
)

// Run downloads the latest agent binary for the current architecture from the
// server, verifies its SHA256 checksum (and HMAC signature when a secret is
// shared), then replaces the running binary and restarts the service.
//
// 关键设计：升级脚本必须脱离 agent 的 cgroup/session，否则 agent 退出时
// systemd（默认 KillMode=control-group）会杀掉整个 cgroup，升级脚本也被杀。
// 因此用 setsid 启动独立会话，脚本先等待 agent 退出，再下载替换。
func Run(cfg *config.Config) {
	secret := cfg.Secret
	serverURL := strings.TrimRight(cfg.ServerURL, "/")
	binPath, err := os.Executable()
	if err != nil {
		return
	}

	url := fmt.Sprintf("%s/bin/linux/%s/agent", serverURL, runtime.GOARCH)
	script := buildUpgradeScript(url, binPath, secret)

	// 脚本写到 /tmp（不放在 bin 目录，避免权限问题）
	tmp, err := os.CreateTemp("/tmp", "monitor-agent-upgrade-*.sh")
	if err != nil {
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
	// Setpgid 确保子进程独立进程组
	cmd.SysProcAttr = setSysProcAttr()
	_ = cmd.Start()
}

// buildUpgradeScript returns a bash script that downloads the new agent binary,
// verifies its checksum/signature, replaces the running binary and restarts.
//
// 脚本执行顺序：
//  1. 等待当前 agent 进程退出（最多 30s），避免与旧进程竞争文件
//  2. 下载新二进制 + 校验 SHA256/HMAC
//  3. systemctl stop -> mv 替换 -> systemctl start
//  4. 每步输出详细日志到 stdout（重定向到 /var/log/monitor-agent-upgrade.log）
func buildUpgradeScript(url, binPath, secret string) string {
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
SECRET='%s'
MAX=3

LOG "=== agent 升级脚本启动 ==="
LOG "BIN=$BIN URL=$URL PID=$$"

# 等待当前 agent 进程退出（最多 30s），避免占用二进制文件
SELF_PID=$(cat /var/run/monitor-agent.pid 2>/dev/null || echo "")
if [ -n "$SELF_PID" ]; then
  LOG "等待 agent (pid=$SELF_PID) 退出..."
  for i in $(seq 1 30); do
    if ! kill -0 "$SELF_PID" 2>/dev/null; then
      LOG "agent 已退出"
      break
    fi
    sleep 1
  done
else
  # 没有 pid 文件，用 sleep 等待 agent 自行退出
  LOG "无 pid 文件，sleep 8s 等待 agent 退出"
  sleep 8
fi

# 下载新二进制
if [ -n "$SECRET" ]; then HDR=(-H "X-Agent-Secret: $SECRET"); else HDR=(); fi

ok=0
for i in $(seq 1 $MAX); do
  LOG "下载二进制 (尝试 $i/$MAX): $URL"
  if curl -fsSL "${HDR[@]}" "$URL" -o "$TMP"; then ok=1; LOG "下载成功"; break; fi
  sleep 2
done
if [ "$ok" -ne 1 ]; then LOG "错误: 二进制下载失败"; rm -f "$TMP"; exit 1; fi

# 下载校验和
sumok=0
for i in $(seq 1 $MAX); do
  LOG "下载校验和 (尝试 $i/$MAX): $URL.sha256"
  if curl -fsSL "${HDR[@]}" "$URL.sha256" -o "$TMP.sha256"; then sumok=1; LOG "校验和下载成功"; break; fi
  sleep 2
done
if [ "$sumok" -ne 1 ]; then LOG "错误: 校验和下载失败"; rm -f "$TMP"; exit 1; fi

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

# 停止服务（如果还在运行）
if command -v systemctl >/dev/null 2>&1; then
  LOG "停止 monitor-agent 服务..."
  systemctl stop monitor-agent 2>/dev/null || true
  sleep 1
fi

# 替换二进制（带重试，处理 ETXTBSY）
LOG "替换二进制: $TMP -> $BIN"
for i in $(seq 1 5); do
  if mv -f "$TMP" "$BIN" 2>/dev/null; then
    LOG "替换成功"
    break
  fi
  LOG "替换失败 (尝试 $i/5)，等待重试..."
  sleep 1
done
if [ -f "$TMP" ]; then LOG "错误: 二进制替换最终失败"; rm -f "$TMP" "$TMP.sha256"; exit 1; fi
rm -f "$TMP.sha256"

# 启动新版本
if command -v systemctl >/dev/null 2>&1; then
  LOG "启动 monitor-agent 服务..."
  systemctl start monitor-agent
  sleep 2
  LOG "服务状态: $(systemctl is-active monitor-agent 2>/dev/null || echo unknown)"
fi

LOG "=== agent 升级完成 ==="
`, binEsc, urlEsc, secEsc)
}
