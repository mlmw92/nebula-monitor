#!/usr/bin/env bash
# 采集项模板「本机护栏」实机验证（C1 阶段三：jdbc / exec / file）。
#
# 用法：bash build/verify-template-guards.sh [agent 二进制路径]
#   - 传二进制：直接用它验证（例如本机构建的 Linux 二进制）；
#   - 不传：在临时目录从当前源码构建（需要 go，Linux 上建议用 /usr/local/go/bin/go）。
#
# 为什么需要这个脚本：这三类会以 root 触碰被监考机本身，护栏是它们唯一由机器掌握的那道门，
# 而护栏的行为（放行/拒绝/不执行）无法靠单元测试在真实进程与文件系统上证明。
#
# 断言（任一失败退出码非 0）：
#   1. exec 白名单内命令：取值成功且 up=1
#   2. exec 命令不在白名单：**不执行**、只产 up=0
#   3. file 白名单内文件：取值成功且 up=1
#   4. file 路径不在白名单：只产 up=0
#   5. jdbc 目标库不可达：只产 up=0，且不产数据
#   6. 护栏未启用却配了护栏类模板：Agent 拒绝启动并打印原因（fail-fast）
set -uo pipefail

BIN="${1:-}"
# 脚本位于 build/ 下：切到仓库根，便于从源码构建与相对路径打包
cd "$(dirname "$0")/.." || exit 1
PORT=$((RANDOM % 2000 + 18000))
WORK=$(mktemp -d /tmp/verify-guards.XXXXXX)
PAYLOAD="$WORK/payload.jsonl"
AGENT_LOG="$WORK/agent.log"
SRC_DIR=""

cleanup() {
  [ -n "${AGENT_PID:-}" ] && kill "$AGENT_PID" 2>/dev/null
  [ -n "${CAP_PID:-}" ] && kill "$CAP_PID" 2>/dev/null
  rm -rf "$WORK"
  [ -n "$SRC_DIR" ] && rm -rf "$SRC_DIR"
}
trap cleanup EXIT

c_ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }
c_bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; FAILED=$((FAILED + 1)); }
FAILED=0

echo "=== 准备：$WORK（端口 $PORT）==="

# 待读取的「应用状态文件」
printf 'stale 1\nqueue_depth 42\n' > "$WORK/app-metrics.txt"

# 上报捕获：把每个上报体写成一行 JSON，随后用 python 精确断言（不靠 grep 猜结构）
cat > "$WORK/capture.py" <<'PY'
import http.server, sys
out = open(sys.argv[2], "a", buffering=1)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out.write(self.rfile.read(n).decode("utf-8", "replace") + "\n")
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(b"{}")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

cat > "$WORK/assert.py" <<'PY'
import json, sys
path = sys.argv[1]
metrics = []
try:
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        metrics.extend(json.loads(line).get("metrics") or [])
except FileNotFoundError:
    pass

def series(name, **labels):
    out = []
    for m in metrics:
        if m.get("name") != name:
            continue
        lb = m.get("labels") or {}
        if all(lb.get(k) == v for k, v in labels.items()):
            out.append(m)
    return out

fails = []
def check(desc, ok):
    print(("  \033[32m✓\033[0m " if ok else "  \033[31m✗\033[0m ") + desc)
    if not ok:
        fails.append(desc)

def up(tpl):
    # Agent 会在若干轮采集里各上报一次，取最后一次（并只断言「每次都一致」由 all_values 负责）
    s = series("template_target_up", template=tpl)
    return s[-1]["value"] if s else None

def all_values(name, want, **labels):
    s = series(name, **labels)
    return len(s) >= 1 and all(m["value"] == want for m in s)

# 1. exec 白名单内命令
check("exec：白名单内命令取值成功（up=1 且取到 42）",
      up("execcmd") == 1 and all_values("execcmd_queue_depth", 42))
# 2. exec 命令不在白名单
check("exec：命令不在白名单 → up=0 且不产数据",
      up("execdenied") == 0 and len(series("execdenied_x")) == 0)
# 3. file 白名单内文件
check("file：白名单内文件取值成功（up=1 且取到 42）",
      up("filestate") == 1 and all_values("filestate_queue_depth", 42))
# 4. file 路径不在白名单
check("file：路径不在白名单 → up=0 且不产数据",
      up("filedenied") == 0 and len(series("filedenied_y")) == 0)
# 5. jdbc 不可达
check("jdbc：目标库不可达 → up=0 且不产数据",
      up("dbtest") == 0 and len(series("dbtest_rows")) == 0)
# 附带：护栏类模板的产出里带 template 标签（可辨识来源）
check("护栏类模板产出带 template 标签", len(series("template_target_up", template="execcmd")) >= 1)

if fails:
    print("\n未通过：%d 项" % len(fails))
    # 诊断：把探到的 up 序列与数据指标名列出来（失败时不至于只能靠猜）
    ups = [(m["labels"].get("template"), m["value"]) for m in metrics if m.get("name") == "template_target_up"]
    print("  实际 template_target_up：%s" % sorted(ups))
    print("  实际数据指标：%s" % sorted({m["name"] for m in metrics if m.get("name") != "template_target_up"}))
    sys.exit(1)
print("\n全部断言通过")
PY

# ---- Agent 二进制 ----
if [ -z "$BIN" ]; then
  GO=$(command -v go || echo /usr/local/go/bin/go)
  SRC_DIR=$(mktemp -d /tmp/verify-guards-src.XXXXXX)
  echo "  从源码构建（$GO）…"
  tar -czf "$SRC_DIR/src.tgz" cmd internal go.mod go.sum VERSION 2>/dev/null
  tar -xzf "$SRC_DIR/src.tgz" -C "$SRC_DIR"
  ( cd "$SRC_DIR" && export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB=off GOTOOLCHAIN=local && CGO_ENABLED=0 "$GO" build -o "$WORK/agent" ./cmd/agent ) || { echo "构建失败"; exit 1; }
  BIN="$WORK/agent"
fi
[ -x "$BIN" ] || { echo "二进制不可执行：$BIN"; exit 1; }

# ---- 场景一：护栏放行 + 四类模板 ----
# 用**引号 heredoc + 占位符替换**生成配置：反斜杠只在 YAML 里解释一次，
# 不与 heredoc 的转义规则叠加（写模板正则时踩过这个坑：`\\d` 会被 heredoc 折叠成 `\d`，而 YAML 双引号里 `\d` 非法）。
# 正则一律用 YAML 单引号（不做转义处理）。
sed -e "s|@WORK@|$WORK|g" -e "s|@PORT@|$PORT|g" > "$WORK/agent.yaml" <<'YAML'
mode: collect
serverURL: http://127.0.0.1:@PORT@
node: verify-guards
group: default
interval: 1
collectTimeout: 5
collectors:
  cpu: true
templateGuards:
  exec:
    enabled: true
    allow: ["/bin/echo"]
  file:
    enabled: true
    allow: ["@WORK@/app-metrics.txt"]
  jdbc:
    enabled: true
templates:
  - id: execcmd
    kind: exec
    targets:
      - { instance: local, command: /bin/echo, args: ["queue_depth 42"] }
    rules:
      metrics:
        - { name: queue_depth, pattern: '(\d+)' }
  - id: execdenied
    kind: exec
    targets:
      - { instance: local, command: /bin/cat, args: ["/etc/hostname"] }
    rules:
      metrics:
        - { name: x, pattern: '(\d+)' }
  - id: filestate
    kind: file
    targets:
      - { instance: local, path: "@WORK@/app-metrics.txt" }
    rules:
      metrics:
        - { name: queue_depth, pattern: '(?m)^queue_depth (\d+)$' }
  - id: filedenied
    kind: file
    targets:
      - { instance: local, path: /etc/hostname }
    rules:
      metrics:
        - { name: y, pattern: '(\d+)' }
  - id: dbtest
    kind: jdbc
    driver: mysql
    targets:
      - { instance: local, addr: "127.0.0.1:1", database: appdb,
          auth: { basic: { user: u, password: p } } }
    rules:
      metrics:
        - { name: rows, query: "SELECT 1" }
YAML

echo "=== 场景一：护栏放行（exec / file / jdbc）==="
python3 "$WORK/capture.py" "$PORT" "$PAYLOAD" &
CAP_PID=$!
sleep 1
timeout 8 "$BIN" -config "$WORK/agent.yaml" > "$AGENT_LOG" 2>&1 &
AGENT_PID=$!
sleep 6
kill "$AGENT_PID" 2>/dev/null; wait "$AGENT_PID" 2>/dev/null
kill "$CAP_PID" 2>/dev/null; wait "$CAP_PID" 2>/dev/null
python3 "$WORK/assert.py" "$PAYLOAD" || FAILED=$((FAILED + 1))

echo "=== 场景二：护栏未启用却配了护栏类模板（应拒绝启动）==="
cat > "$WORK/agent-bad.yaml" <<'YAML'
mode: collect
serverURL: http://127.0.0.1:18999
node: verify-guards
group: default
interval: 1
collectors:
  cpu: true
templates:
  - id: execcmd
    kind: exec
    targets:
      - { instance: local, command: /bin/echo, args: ["1"] }
    rules:
      metrics:
        - { name: v, pattern: '(\d+)' }
YAML
BAD_OUT=$(timeout 5 "$BIN" -config "$WORK/agent-bad.yaml" 2>&1)
if echo "$BAD_OUT" | grep -q "templateGuards.exec.enabled 未开启"; then
  c_ok "未放行却配了 exec 模板：拒绝启动并指出要改哪一项"
else
  c_bad "未放行却配了 exec 模板：应拒绝启动并指向 templateGuards.exec.enabled"
  echo "$BAD_OUT" | tail -5 | sed 's/^/       /'
fi

echo
if [ "$FAILED" -eq 0 ]; then
  echo "=== 全部通过 ==="
  exit 0
fi
echo "=== 有 $FAILED 处失败 ==="
exit 1
