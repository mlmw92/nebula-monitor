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
// secret is the agent auth secret shared with the server; when non-empty it is
// sent as the X-Agent-Secret header and used to verify the download signature.
func Run(cfg *config.Config) {
	secret := cfg.Secret
	serverURL := strings.TrimRight(cfg.ServerURL, "/")
	binPath, err := os.Executable()
	if err != nil {
		return
	}

	// 与 install 脚本、apply 存放结构保持一致：/bin/linux/<arch>/agent
	url := fmt.Sprintf("%s/bin/linux/%s/agent", serverURL, runtime.GOARCH)

	script := buildUpgradeScript(url, binPath, secret)

	tmp, err := os.CreateTemp("", "agent-upgrade-*.sh")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(script); err != nil {
		return
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0o700)

	// Run the script detached so it survives this process exiting.
	cmd := exec.Command("nohup", "bash", tmp.Name(), ">/dev/null", "2>&1", "&")
	_ = cmd.Start()
}

// buildUpgradeScript returns a bash script that downloads the new agent binary,
// verifies its checksum/signature, replaces the running binary and restarts.
func buildUpgradeScript(url, binPath, secret string) string {
	// Escape values for safe embedding into single-quoted bash strings.
	urlEsc := strings.ReplaceAll(url, "'", `'\''`)
	binEsc := strings.ReplaceAll(binPath, "'", `'\''`)
	secEsc := strings.ReplaceAll(secret, "'", `'\''`)

	return fmt.Sprintf(`#!/usr/bin/env bash
set -e
sleep 2

BIN='%s'
TMP="$(mktemp /tmp/agent-upgrade.XXXXXX)"
URL='%s'
SECRET='%s'
MAX=3

if [ -n "$SECRET" ]; then HDR=(-H "X-Agent-Secret: $SECRET"); else HDR=(); fi

ok=0
for i in $(seq 1 $MAX); do
  if curl -fsSL "${HDR[@]}" "$URL" -o "$TMP"; then ok=1; break; fi
  sleep 1
done
if [ "$ok" -ne 1 ]; then echo "agent upgrade: binary download failed"; rm -f "$TMP"; exit 1; fi

sumok=0
for i in $(seq 1 $MAX); do
  if curl -fsSL "${HDR[@]}" "$URL.sha256" -o "$TMP.sha256"; then sumok=1; break; fi
  sleep 1
done
if [ "$sumok" -ne 1 ]; then echo "agent upgrade: checksum download failed"; rm -f "$TMP"; exit 1; fi

chk=$(grep -oE '"checksum"[[:space:]]*:[[:space:]]*"[^"]+"' "$TMP.sha256" | sed -E 's/.*:"([^"]+)"/\1/')
sig=$(grep -oE '"sig"[[:space:]]*:[[:space:]]*"[^"]+"' "$TMP.sha256" | sed -E 's/.*:"([^"]+)"/\1/')
if [ -z "$chk" ]; then echo "agent upgrade: missing checksum"; rm -f "$TMP" "$TMP.sha256"; exit 1; fi

localchk=$(sha256sum "$TMP" | awk '{print $1}')
if [ "$localchk" != "$chk" ]; then
  echo "agent upgrade: checksum mismatch, aborting"; rm -f "$TMP" "$TMP.sha256"; exit 1
fi

if [ -n "$SECRET" ] && [ -n "$sig" ]; then
  if command -v openssl >/dev/null 2>&1; then
    calc=$(printf '%%s' "$chk" | openssl dgst -sha256 -hmac "$SECRET" -r | awk '{print $1}')
    if [ "$calc" != "$sig" ]; then
      echo "agent upgrade: signature mismatch, aborting"; rm -f "$TMP" "$TMP.sha256"; exit 1
    fi
  else
    echo "agent upgrade: openssl not found, skipped signature check (checksum OK)"
  fi
fi

chmod +x "$TMP"
mv -f "$TMP" "$BIN"
rm -f "$TMP.sha256"

if command -v systemctl >/dev/null 2>&1; then
  systemctl restart monitor-agent || true
fi
`, binEsc, urlEsc, secEsc)
}
