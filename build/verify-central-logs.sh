#!/usr/bin/env bash
# 集中日志「采集 → 上行 → 落盘 → 检索 → 保留清理」端到端验证（C2）。
#
# 验证的链路（任一环断了都会表现为「配了却看不到日志」）：
#   日志文件 → Agent 增量读取（模式过滤）→ 上传（鉴权）→ Server 校验 → 按来源/日期/节点分片落盘
#   → 检索接口（关键词 / 命中上限 / 游标翻页）→ 保留清理（按日期分片整天删除）
#   → 同时产出可配告警的指标（<来源>_log_<模式>_total）写入时序库
#
# 关键设计（也是本脚本最容易被写错的地方）：Agent **首次从文件尾开始**（不回溯历史），
# 因此这里刻意「先起 Agent、再追加日志」——顺带把这条决策也断言下来。
#
# 用法：bash build/verify-central-logs.sh
# 依赖：Go、python3、curl（或 python3 的 urllib）。需能监听本地端口（18081/18429）。
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d /tmp/verify-central-logs.XXXXXX)"
GO="${GO:-go}"
SERVER_PORT=18081
VM_PORT=18429
NODE="logs-node"

cleanup() {
  pkill -f "$WORK/agent" 2>/dev/null || true
  pkill -f "$WORK/server" 2>/dev/null || true
  pkill -f "$WORK/fakevm" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== 工作目录 $WORK =="
echo "== 1) 构建 Agent / Server / 假时序库 =="
(cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o "$WORK/agent" ./cmd/agent) || exit 1
(cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o "$WORK/server" ./cmd/server) || exit 1
(cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o "$WORK/fakevm" ./build/template-fakes/fakevm) || exit 1

echo "== 2) 造日志文件（含命中的与未命中的行）=="
mkdir -p "$WORK/logs-src"
cat > "$WORK/app.log" <<'EOF'
2026-09-26 10:00:00 INFO all good
2026-09-26 10:00:01 error: disk full on /data
EOF

echo "== 3) Server：集中日志目录 + 接入鉴权；关闭登录认证（脚本免取 token）=="
cat > "$WORK/server.yaml" <<YAML
mode: standalone
listen: ":$SERVER_PORT"
dataDir: "$WORK/server-data"
logDir: "$WORK/server-data/logs"
logMaxBytesPerDay: 10485760
logUploadRateBps: 10485760
tsdb:
  backend: victoriametrics
  addr: "http://127.0.0.1:$VM_PORT"
agentAuth:
  enabled: true
  secret: "verify-secret"
auth:
  enabled: false
YAML

"$WORK/fakevm" -addr "127.0.0.1:$VM_PORT" -out "$WORK/vm-received.bin" > "$WORK/fakevm.log" 2>&1 &
"$WORK/server" -config "$WORK/server.yaml" > "$WORK/server.log" 2>&1 &
sleep 3

echo "== 4) Agent：配 logSources（两个模式），首次从文件尾开始 =="
cat > "$WORK/agent.yaml" <<YAML
serverURL: "http://127.0.0.1:$SERVER_PORT"
node: "$NODE"
group: "default"
secret: "verify-secret"
interval: 3
logOffsetsFile: "$WORK/log-offsets.json"
collectors:
  cpu: false
  memory: false
  disk: false
  network: false
  process: false
  load: false
  redis: false
  mysql: false
  postgres: false
  nginx: false
  nginxLog: false
  kafka: false
  docker: false
  rocketmq: false
  k8s: false
  mongodb: false
  fastdfs: false
  port: false
  security: false
logSources:
  - id: applog
    paths: ["$WORK/app.log"]
    patterns:
      - { name: err, regex: "(?i)\\berror\\b" }
      - { name: slow, regex: "(?i)slow request" }
YAML

"$WORK/agent" -config "$WORK/agent.yaml" > "$WORK/agent.log" 2>&1 &
sleep 5   # 让第一轮跑完（它从文件尾开始，不会读到上面那两行）

echo "== 5) 追加新的日志行（模拟业务在写日志）=="
cat >> "$WORK/app.log" <<'EOF'
2026-09-26 10:01:00 error: disk full on /data
2026-09-26 10:01:01 slow request 3.2s
2026-09-26 10:01:02 INFO fine
EOF
sleep 7   # 等下一轮采集上报

echo "== 6) 断言：落盘 / 检索 / 翻页 / 指标 / 保留清理 =="
python3 - "$WORK" "$SERVER_PORT" "$NODE" <<'PY'
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

work, port, node = sys.argv[1], sys.argv[2], sys.argv[3]
base = f"http://127.0.0.1:{port}"
fails = []


def check(cond, msg):
    print(("  PASS  " if cond else "  FAIL  ") + msg)
    if not cond:
        fails.append(msg)


def read(path):
    try:
        with open(path, "rb") as fh:
            return fh.read()
    except FileNotFoundError:
        return b""


def get(path):
    with urllib.request.urlopen(base + path, timeout=5) as resp:
        return json.loads(resp.read())


def post(path, body=b"{}"):
    req = urllib.request.Request(base + path, data=body, method="POST",
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read())


# —— 落盘：按 来源/日期/节点 分片，一行一条 JSON ——
today = time.strftime("%Y-%m-%d")
shard = os.path.join(work, "server-data", "logs", "applog", today, node + ".log")
raw = read(shard)
check(len(raw) > 0, f"日志已按 来源/日期/节点 落盘（{os.path.relpath(shard, work)}）")
check(b"disk full" in raw, "落盘内容含命中的 error 行")
check(b"slow request" in raw, "落盘内容含命中的 slow 行")
check(b"INFO fine" not in raw, "未命中模式的行**没有**上传（隐私默认值生效）")
check(b"INFO all good" not in raw, "启动前已存在的历史行没有被回溯上传（首次从文件尾开始）")

# —— 检索接口：关键词 / 节点过滤 ——
now = int(time.time() * 1000)
frm = now - 3600 * 1000
res = get(f"/api/v1/logs?from={frm}&to={now}&q=disk&limit=100")
check(len(res.get("lines", [])) == 1, f"关键词检索命中 1 条（实际 {len(res.get('lines', []))}）")
line = (res.get("lines") or [{}])[0]
check(line.get("node") == node, "命中结果带正确的节点名")
check(line.get("source") == "applog", "命中结果带正确的来源")
check(res.get("truncated") is False, "未达上限时 truncated=false")

# —— 命中上限与游标翻页（不重不漏）——
p1 = get(f"/api/v1/logs?from={frm}&to={now}&limit=2")
check(len(p1.get("lines", [])) == 2 and p1.get("truncated") is True, "命中上限触发 truncated=true")
check(bool(p1.get("cursor")), "截断时给出续读游标")
p2 = get(f"/api/v1/logs?from={frm}&to={now}&limit=2&cursor={p1['cursor']}")
texts = [l["text"] for l in p1["lines"]] + [l["text"] for l in p2.get("lines", [])]
check(len(texts) == len(set(texts)), "两页之间没有重复行")
check(len(texts) == 3, f"两页合计覆盖全部 3 条（实际 {len(texts)}）")
check(p2.get("truncated") is False, "最后一页不再截断")

# —— 诊断字段：解释「为什么有/没有结果」——
check(res.get("scannedLines", 0) > 0 and res.get("files", 0) >= 1, "响应带扫描诊断（scanLines/files）")

# —— 非法参数 ——
try:
    get("/api/v1/logs?regex=" + urllib.parse.quote("("))
    check(False, "非法正则应返回 400")
except urllib.error.HTTPError as e:
    check(e.code == 400, f"非法正则返回 400（实际 {e.code}）")

# —— 指标：模式进指标名（阈值规则才能按模式精确告警）——
vm = read(os.path.join(work, "vm-received.bin"))
check(b"applog_log_err_total" in vm, "时序库收到 applog_log_err_total（模式在指标名里）")
check(b"applog_log_slow_total" in vm, "时序库收到 applog_log_slow_total")
check(b"applog_log_lines_total" in vm, "时序库收到 applog_log_lines_total")
check(b"pattern=" not in vm, "指标里不再带 pattern 标签（否则阈值规则无法按模式筛选）")

# —— 保留清理：按日期分片整天删除，当天不删 ——
old = time.strftime("%Y-%m-%d", time.localtime(time.time() - 30 * 86400))
old_dir = os.path.join(work, "server-data", "logs", "applog", old)
os.makedirs(old_dir, exist_ok=True)
with open(os.path.join(old_dir, node + ".log"), "w", encoding="utf-8") as fh:
    fh.write('{"ts":1,"node":"' + node + '","source":"applog","text":"old"}\n')

res = post("/api/v1/system/retention/cleanup")
check(res.get("logDirsRemoved", 0) == 1, f"保留清理删掉 1 个过期日期分片（实际 {res.get('logDirsRemoved')}）")
check(res.get("logFilesRemoved", 0) == 1, "清理结果报告删除的文件数")
check(not os.path.exists(old_dir), "过期分片目录已从磁盘移除")
check(os.path.exists(shard), "当天分片**未**被清理（清理不碰正在写的数据）")
st = get("/api/v1/system/retention")
check(st.get("logs", {}).get("files", 0) >= 1, "保留策略状态里能看到集中日志占用")

if fails:
    print(f"共 {len(fails)} 项未通过")
    sys.exit(1)
print("端到端全部通过")
PY
rc=$?

echo "== 7) Agent 侧证据 =="
grep -E "日志上传失败|日志采集失败" "$WORK/agent.log" && echo "  FAIL  Agent 日志出现上传/采集失败" || echo "  PASS  Agent 无上传失败记录"
echo "== Server 侧证据 =="
grep -E "panic|集中日志" "$WORK/server.log" | head -5 || true

exit "$rc"
