#!/usr/bin/env bash
#
# Agent 采集一致性回归比对（Agent collection parity check）
#
# 用途：把「当前工作树」与「指定基线 ref」两份源码分别构建成 agent 二进制，在受控的
#       假 exporter / 假 Redis 环境下各完整采集一轮，再比对上报体的
#       「指标名 + label 键集合」与各嵌套结构，输出 IDENTICAL 或差异清单。
#
# 适用场景：任何改动 Agent 采集链路的工作（采集器重构、新增中间件、指标改名等）。
#           比对只关心结构与指标名，忽略随时间变化的数值，因此可重复执行。
#
# 用法（Windows 下用 Git Bash 执行）：
#   bash build/verify-agent-parity.sh --baseline 45189d3
#   bash build/verify-agent-parity.sh --baseline HEAD~1 --race
#   bash build/verify-agent-parity.sh --baseline v1.23.3 --host dev-server --keep
#
# 参数：
#   --baseline <ref>   基线 git ref（必填）。用于代表「改造前」版本。
#   --host <alias>     ssh 别名，默认 dev-server（见 ~/.ssh/config）。
#   --race             额外在新版源码上执行 CGO_ENABLED=1 go test -race ./internal/agent/...
#   --install-go       远端无 Go 时自动从 golang.google.cn 安装（否则报错并给出提示）
#   --keep             比对完成后保留远端工作目录（便于排查），默认清理
#
# 依赖：本地 git / tar / scp / ssh；远端 bash、python3、go（--race 另需 gcc）
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

BASELINE=""
HOST="dev-server"
RACE=0
INSTALL_GO=0
KEEP=0

while [ $# -gt 0 ]; do
  case "$1" in
    --baseline) BASELINE="${2:?--baseline 需要参数}"; shift 2 ;;
    --host)     HOST="${2:?--host 需要参数}"; shift 2 ;;
    --race)     RACE=1; shift ;;
    --install-go) INSTALL_GO=1; shift ;;
    --keep)     KEEP=1; shift ;;
    -h|--help)  sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "未知参数: $1（-h 查看用法）" >&2; exit 2 ;;
  esac
done

if [ -z "$BASELINE" ]; then
  echo "错误：必须指定 --baseline <ref>（可比对的「改造前」版本）" >&2
  exit 2
fi
if ! git -C "$REPO_ROOT" rev-parse --verify "$BASELINE^{commit}" >/dev/null 2>&1; then
  echo "错误：基线 ref 不存在：$BASELINE" >&2
  exit 2
fi

WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

PAYLOAD="$WORK/payload"
mkdir -p "$PAYLOAD"

# ---------------------------------------------------------------- 1) 导出两份源码
# 当前工作树优先用 `git stash create` 捕获未提交改动；无改动时退回 HEAD。
# 注意：stash create 不含未跟踪文件（新增文件请先 git add）。
NEW_REF="$(git -C "$REPO_ROOT" stash create 2>/dev/null || true)"
if [ -n "$NEW_REF" ]; then
  echo "== 新版 = 当前工作树（含未提交改动，stash $NEW_REF）"
else
  NEW_REF=HEAD
  echo "== 新版 = HEAD（工作树干净）"
fi
echo "== 基线 = $BASELINE ($(git -C "$REPO_ROOT" rev-parse --short "$BASELINE^{commit}"))"

git -C "$REPO_ROOT" archive --format=tar.gz -o "$PAYLOAD/new.tgz" "$NEW_REF"
git -C "$REPO_ROOT" archive --format=tar.gz -o "$PAYLOAD/old.tgz" "$BASELINE^{commit}"

# ---------------------------------------------------------------- 2) 生成远端辅助文件
cat > "$PAYLOAD/fake_exporter.py" <<'PY'
#!/usr/bin/env python3
"""假 exporter：一个端口同时提供 /metrics（Prometheus 文本）与 /nginx_status（stub_status）。"""
import http.server
import socketserver

METRICS = """# HELP nginx_instance_up nginx up
nginx_instance_up 1
nginx_active_connections 7
nginx_requests 123
mysql_instance_up 1
mysql_threads_connected 12
mysql_qps 8
postgres_instance_up 1
postgres_numbackends 3
redis_instance_up 1
redis_connected_clients 5
kafka_instance_up 1
kafka_broker_count 3
rocketmq_instance_up 1
rocketmq_broker_count 2
mongodb_up 1
mongodb_connections_current 9
fastdfs_up 1
fastdfs_storage_count 4
"""

STUB = """Active connections: 3
server accepts handled requests
 100 100 250
Reading: 0 Writing: 1 Waiting: 2
"""


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith("/metrics"):
            body = METRICS.encode()
        elif self.path.startswith("/nginx_status"):
            body = STUB.encode()
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Server", "nginx/1.24.0")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", 19113), Handler) as srv:
    srv.serve_forever()
PY

cat > "$PAYLOAD/fake_redis.py" <<'PY'
#!/usr/bin/env python3
"""极简 RESP 服务端：仅响应 INFO / CLUSTER / AUTH / PING，用于验证 Redis 直连采集链路。"""
import socket
import threading

INFO = """# Server
redis_version:7.2.0
redis_mode:standalone
role:master
connected_clients:5
used_memory:1048576
total_commands_processed:1000
keyspace_hits:100
keyspace_misses:10
db0:keys=42,expires=0,avg_ttl=0
uptime_in_seconds:3600
"""


def bulk(payload: bytes) -> bytes:
    return b"$%d\r\n" % len(payload) + payload + b"\r\n"


def handle(conn: socket.socket) -> None:
    f = conn.makefile("rwb", buffering=0)
    try:
        while True:
            line = f.readline()
            if not line:
                return
            if not line.startswith(b"*"):
                continue
            n = int(line[1:].strip())
            args = []
            for _ in range(n):
                hdr = f.readline()
                ln = int(hdr[1:].strip())
                args.append(f.read(ln + 2)[:-2])
            cmd = args[0].upper() if args else b""
            if cmd == b"INFO":
                f.write(bulk(INFO.encode()))
            elif cmd == b"CLUSTER":
                f.write(bulk(b"cluster_enabled:0\r\ncluster_state:ok\r\n"))
            elif cmd == b"AUTH":
                f.write(b"+OK\r\n")
            elif cmd == b"PING":
                f.write(b"+PONG\r\n")
            else:
                f.write(b"-ERR unknown command\r\n")
    except Exception:
        pass
    finally:
        conn.close()


srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 16379))
srv.listen(16)
while True:
    c, _ = srv.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
PY

cat > "$PAYLOAD/capture.py" <<'PY'
#!/usr/bin/env python3
"""上报捕获器：把 Agent POST /api/v1/report 的 body 逐行追加到文件。"""
import http.server
import socketserver
import sys

OUT = sys.argv[1]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        ln = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(ln)
        with open(OUT, "ab") as fh:
            fh.write(body + b"\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *args):
        pass


socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", 18099), Handler) as srv:
    srv.serve_forever()
PY

cat > "$PAYLOAD/compare.py" <<'PY'
#!/usr/bin/env python3
"""对比两份上报体的「指标名 + label 键集合」与各嵌套结构，忽略随时间变化的数值。"""
import json
import sys

INSTANCE_FIELDS = [
    "redisInstances", "mysqlInstances", "postgresInstances", "nginxInstances",
    "kafkaInstances", "dockerInstances", "rocketmqInstances", "k8sInstances",
    "mongodbInstances", "fastdfsInstances", "nginxAccessStats", "securityEvents",
    "listeners", "firewallRules",
]
OBJECT_FIELDS = ["firewallStatus", "hostInfo", "securityBaseline", "capabilities", "defenseStatus"]


def norm(payload: dict) -> dict:
    out = {"top": sorted(payload.keys())}

    sig = set()
    for m in payload.get("metrics") or []:
        labels = m.get("labels") or {}
        sig.add(m.get("name", "") + "|" + ",".join(sorted(labels.keys())))
    out["metric_signature"] = sorted(sig)

    procs = payload.get("processes") or []
    out["processes#keys"] = sorted({",".join(sorted(p.keys())) for p in procs if isinstance(p, dict)})

    for field in INSTANCE_FIELDS:
        v = payload.get(field)
        if isinstance(v, list):
            out[field + "#len"] = len(v)
            out[field + "#keys"] = sorted(
                {",".join(sorted(it.keys())) for it in v if isinstance(it, dict)}
            )

    for field in OBJECT_FIELDS:
        v = payload.get(field)
        if isinstance(v, dict):
            out[field + "#keys"] = sorted(v.keys())

    return out


def main() -> int:
    old = norm(json.load(open(sys.argv[1], encoding="utf-8")))
    new = norm(json.load(open(sys.argv[2], encoding="utf-8")))

    diffs = 0
    for key in sorted(set(old) | set(new)):
        a, b = old.get(key), new.get(key)
        if a == b:
            continue
        diffs += 1
        print(f"DIFF {key}")
        if isinstance(a, list) and isinstance(b, list):
            only_old = [x for x in a if x not in b]
            only_new = [x for x in b if x not in a]
            if only_old:
                print("  仅旧版有:", only_old[:30])
            if only_new:
                print("  仅新版有:", only_new[:30])
        else:
            print("  old:", str(a)[:400])
            print("  new:", str(b)[:400])

    print("=" * 60)
    if diffs == 0:
        print("RESULT: IDENTICAL —— 指标名/label 集合与上报体结构完全一致")
        return 0
    print(f"RESULT: {diffs} 处结构差异")
    return 1


if __name__ == "__main__":
    sys.exit(main())
PY

cat > "$PAYLOAD/agent.yaml" <<'YAML'
serverURL: "http://127.0.0.1:18099"
node: "verify-node"
group: "default"
interval: 3600

collectors:
  cpu: true
  memory: true
  disk: true
  network: true
  process: true
  load: true
  redis: true
  mysql: true
  postgres: true
  nginx: true
  nginxLog: false
  kafka: true
  docker: false
  rocketmq: true
  k8s: false
  mongodb: true
  fastdfs: true
  port: true
  security: true

redisInstances:
  - name: "r-exporter"
    addr: "127.0.0.1:16379"
    topology: "standalone"
    exporterURL: "http://127.0.0.1:19113/metrics"
  - name: "r-direct"
    addr: "127.0.0.1:16379"
    topology: "standalone"

mysqlInstances:
  - name: "m-exporter"
    addr: "127.0.0.1:13306"
    user: "monitor"
    topology: "standalone"
    exporterURL: "http://127.0.0.1:19113/metrics"

postgresInstances:
  - name: "pg-exporter"
    addr: "127.0.0.1:15432"
    database: "postgres"
    user: "monitor"
    topology: "standalone"
    exporterURL: "http://127.0.0.1:19113/metrics"

nginxInstances:
  - name: "n-exporter"
    addr: "127.0.0.1:19113"
    statusPath: "/nginx_status"
    exporterURL: "http://127.0.0.1:19113/metrics"
  - name: "n-stub"
    addr: "127.0.0.1:19113"
    statusPath: "/nginx_status"

kafkaInstances:
  - name: "k-exporter"
    addr: "127.0.0.1:19092"
    exporterURL: "http://127.0.0.1:19113/metrics"

rocketmqInstances:
  - name: "rmq-exporter"
    addr: "127.0.0.1:19876"
    exporterURL: "http://127.0.0.1:19113/metrics"

mongoInstances:
  - name: "mg-exporter"
    addr: "127.0.0.1:27017"
    topology: "standalone"
    exporterURL: "http://127.0.0.1:19113/metrics"

fastdfsInstances:
  - name: "fd-tracker"
    role: "tracker"
    addr: "127.0.0.1:22122"
    exporterURL: "http://127.0.0.1:19113/metrics"

portChecks: ["18099", "19113"]

security:
  fimPaths: []
  sshLogPaths: []
  bruteForceThreshold: 5
  bruteForceWindowSec: 300
  weakPasswordCheck: false
YAML

cat > "$PAYLOAD/remote_run.sh" <<'SH'
#!/usr/bin/env bash
# 远端执行体：构建新旧 agent → 起假服务 → 各采一轮 → 比对结构。
set -u
GO="${GO:-/usr/local/go/bin/go}"
V="$(cd "$(dirname "$0")" && pwd)"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB=off GOTOOLCHAIN=local
cd "$V" || exit 1

echo "== 1) 解包源码 =="
rm -rf "$V/nebula-new" "$V/nebula-old"
mkdir -p "$V/nebula-new" "$V/nebula-old"
tar -xzf "$V/new.tgz" -C "$V/nebula-new"
tar -xzf "$V/old.tgz" -C "$V/nebula-old"

echo "== 2) 构建新旧 Agent =="
(cd "$V/nebula-new" && CGO_ENABLED=0 "$GO" build -p 2 -o "$V/agent-new" ./cmd/agent) || exit 1
(cd "$V/nebula-old" && CGO_ENABLED=0 "$GO" build -p 2 -o "$V/agent-old" ./cmd/agent) || exit 1

if [ "${RACE:-0}" = "1" ]; then
  echo "== 2.1) 新版 -race 竞态检测 =="
  (cd "$V/nebula-new" && CGO_ENABLED=1 "$GO" test -race -count=1 -p 2 ./internal/agent/...) || exit 1
fi

echo "== 3) 启动假服务（exporter + redis）=="
pkill -f "$V/fake_exporter.py" 2>/dev/null || true
pkill -f "$V/fake_redis.py" 2>/dev/null || true
sleep 0.5
nohup python3 "$V/fake_exporter.py" > "$V/fake_exporter.log" 2>&1 &
nohup python3 "$V/fake_redis.py" > "$V/fake_redis.log" 2>&1 &
sleep 1

run_agent() {
  local bin="$1" out="$2" log="$3"
  pkill -f "$V/capture.py" 2>/dev/null || true
  rm -f "$out"
  nohup python3 "$V/capture.py" "$out" > "$V/capture.log" 2>&1 &
  sleep 1
  timeout "${AGENT_RUN_SEC:-30}" "$V/$bin" -config "$V/agent.yaml" > "$log" 2>&1 || true
  sleep 0.5
  pkill -f "$V/capture.py" 2>/dev/null || true
  echo "   $bin 捕获上报行数: $(wc -l < "$out" 2>/dev/null || echo 0)"
}

echo "== 4) 运行旧版 Agent（基线）=="
run_agent agent-old "$V/cap-old.jsonl" "$V/old.log"
echo "== 5) 运行新版 Agent（当前）=="
run_agent agent-new "$V/cap-new.jsonl" "$V/new.log"

pkill -f "$V/fake_exporter.py" 2>/dev/null || true
pkill -f "$V/fake_redis.py" 2>/dev/null || true

echo "== 6) 结构比对 =="
if [ ! -s "$V/cap-old.jsonl" ] || [ ! -s "$V/cap-new.jsonl" ]; then
  echo "错误：任一侧未捕获到上报，无法比对（日志见 $V/old.log / $V/new.log）" >&2
  exit 1
fi
tail -n 1 "$V/cap-old.jsonl" > "$V/old.json"
tail -n 1 "$V/cap-new.jsonl" > "$V/new.json"
python3 "$V/compare.py" "$V/old.json" "$V/new.json"
SH
chmod +x "$PAYLOAD/remote_run.sh"

# ---------------------------------------------------------------- 3) 传输
REMOTE_DIR="/tmp/nebula-parity-$$"
echo "== 上传到 $HOST:$REMOTE_DIR =="
ssh -o BatchMode=yes "$HOST" "mkdir -p '$REMOTE_DIR'"
scp -q -o BatchMode=yes -r "$PAYLOAD/." "$HOST:$REMOTE_DIR/"

# ---------------------------------------------------------------- 4) 远端执行
GO_HINT=0
if ! ssh -o BatchMode=yes "$HOST" "test -x \${GO:-/usr/local/go/bin/go} || command -v go >/dev/null"; then
  if [ "$INSTALL_GO" = "1" ]; then
    echo "== 远端无 Go，自动安装 =="
    ssh -o BatchMode=yes "$HOST" 'set -e; V=$(curl -s -m 20 https://golang.google.cn/VERSION?m=text | head -1); echo "安装 $V"; curl -sL -o /tmp/go.tgz "https://golang.google.cn/dl/${V}.linux-amd64.tar.gz"; sudo rm -rf /usr/local/go; sudo tar -C /usr/local -xzf /tmp/go.tgz; sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go; go version'
  else
    GO_HINT=1
  fi
fi

set +e
if [ "$GO_HINT" = "1" ]; then
  echo "错误：远端 $HOST 未安装 Go。加 --install-go 自动安装，或手动安装后重试。" >&2
  RC=1
else
  ssh -o BatchMode=yes "$HOST" "RACE=$RACE bash '$REMOTE_DIR/remote_run.sh'"
  RC=$?
fi
set -e

# ---------------------------------------------------------------- 5) 收尾
if [ "$KEEP" = "1" ]; then
  echo "== 保留远端目录：$HOST:$REMOTE_DIR =="
else
  ssh -o BatchMode=yes "$HOST" "rm -rf '$REMOTE_DIR'" || true
fi

if [ "$RC" -eq 0 ]; then
  echo "✅ 一致性回归通过"
else
  echo "❌ 一致性回归未通过（退出码 $RC）" >&2
fi
exit "$RC"
