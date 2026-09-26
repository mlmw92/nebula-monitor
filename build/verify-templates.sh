#!/usr/bin/env bash
# 采集项模板（C1 阶段一）实机验证：三种 kind 的映射 / target 失败隔离 / 无模板零变化。
#
# 用法：
#   bash build/verify-templates.sh                        # 三场景（Agent 侧）
#   bash build/verify-templates.sh <pre-C1-Agent-二进制>   # 追加「与改造前上报体等价」对照
#
# 依赖：Go（编译 Agent）、python3（假端点与断言）。在 Linux 上跑（Windows 下可用 Git Bash 试，
# 但端口/进程管理在 Linux 上更可靠）。
#
# 为什么需要它：模板的正确性大量体现在「跨进程」行为上——任务是否真被挂上、失败是否只影响
# 单个 target、无模板时是否真的零变化。单测覆盖不了这些，而阶段二要改 DSL 与取数逻辑，
# 每次改完都应能重跑一遍。
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d /tmp/verify-templates.XXXXXX)"
PRE_C1="${1:-}"
GO="${GO:-go}"

echo "== 工作目录 $WORK =="
cleanup() {
  pkill -f "$WORK/fakes.py" 2>/dev/null || true
  pkill -f "$WORK/capture.py" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== 1) 编译 Agent（当前工作区）=="
(cd "$ROOT" && CGO_ENABLED=0 "$GO" build -o "$WORK/agent-new" ./cmd/agent) || exit 1

echo "== 2) 生成假端点与上报捕获器 =="
cat > "$WORK/fakes.py" <<'PY'
#!/usr/bin/env python3
"""假端点：按端口提供 prometheus / json / text 三种响应。"""
import http.server, socketserver, sys

PORT, KIND = int(sys.argv[1]), sys.argv[2]
PROM = ('rabbitmq_queue_messages{queue="orders",job="rabbitmq"} 42\n'
        'rabbitmq_queue_messages{queue="pay",job="rabbitmq"} 7\n'
        'rabbitmq_queue_consumers_total 3\ngo_goroutines 10\n')
JSON = '{"http":{"requests":{"total":1234}},"worker":{"queue":{"size":5}},"nodes":[{"value":1},{"value":2}]}'
TEXT = 'Active connections: 291 \nReading: 0 Writing: 1 Waiting: 106 \n'
BODY = {"prom": PROM, "json": JSON, "text": TEXT}[KIND].encode()


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Length", str(len(BODY)))
        self.end_headers()
        self.wfile.write(BODY)

    def log_message(self, *a):
        pass


socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", PORT), H) as srv:
    srv.serve_forever()
PY

cat > "$WORK/capture.py" <<'PY'
#!/usr/bin/env python3
"""上报捕获器：把 Agent POST 的 body 逐行追加到文件。"""
import http.server, socketserver, sys

OUT = sys.argv[1]


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        with open(OUT, "ab") as fh:
            fh.write(self.rfile.read(n) + b"\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *a):
        pass


socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", 18099), H) as srv:
    srv.serve_forever()
PY

cat > "$WORK/agent-tpl.yaml" <<'YAML'
serverURL: "http://127.0.0.1:18099"
node: "verify-node"
group: "default"
interval: 3600
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
templates:
  - id: rabbitmq
    kind: prometheus-exporter
    targets:
      - instance: mq-01:15692
        addr: http://127.0.0.1:19201/metrics
    rules:
      keep: "^rabbitmq_"
      drop: "_total$"
      rename:
        - { match: "^rabbitmq_queue_messages$", to: "rabbitmq_queue_depth" }
      labels: { cluster: prod }
      unlabel: ["job"]
  - id: ownapp
    kind: http-json
    targets:
      - instance: app-01:8081
        addr: http://127.0.0.1:19202/stats
    rules:
      metrics:
        - { name: ownapp_requests_total, path: "http.requests.total" }
        - { name: ownapp_queue_depth, path: "worker.queue.size" }
        - { name: ownapp_node2, path: "nodes[1].value" }
      labels: { env: prod }
  - id: customtext
    kind: http-text
    targets:
      - instance: web-01
        addr: http://127.0.0.1:19203/status
    rules:
      metrics:
        - { name: customtext_active_conns, pattern: 'Active connections:\s+(\d+)' }
YAML

# 无模板配置：与 agent-tpl.yaml 同源，仅去掉 templates 段
sed '/^templates:/,$d' "$WORK/agent-tpl.yaml" > "$WORK/agent-notpl.yaml"

echo "== 3) 启动假端点 =="
python3 "$WORK/fakes.py" 19201 prom > "$WORK/fake-prom.log" 2>&1 &
python3 "$WORK/fakes.py" 19202 json > "$WORK/fake-json.log" 2>&1 &
python3 "$WORK/fakes.py" 19203 text > "$WORK/fake-text.log" 2>&1 &
sleep 1

run_agent() { # $1=binary $2=config $3=out $4=log
  pkill -f "$WORK/capture.py" 2>/dev/null || true
  rm -f "$3"
  python3 "$WORK/capture.py" "$3" > /dev/null 2>&1 &
  sleep 1
  timeout 12 "$1" -config "$2" > "$4" 2>&1 || true
  sleep 0.5
  pkill -f "$WORK/capture.py" 2>/dev/null || true
}

echo "== 4) 场景 1：三模板正常采集 =="
run_agent "$WORK/agent-new" "$WORK/agent-tpl.yaml" "$WORK/cap1.jsonl" "$WORK/cap1.log"

echo "== 5) 场景 2：停掉 JSON 端点后重跑（失败隔离）=="
pkill -f "fakes.py 19202" 2>/dev/null || true
sleep 0.5
run_agent "$WORK/agent-new" "$WORK/agent-tpl.yaml" "$WORK/cap2.jsonl" "$WORK/cap2.log"

echo "== 6) 场景 3：无模板（零变化）=="
run_agent "$WORK/agent-new" "$WORK/agent-notpl.yaml" "$WORK/cap3.jsonl" "$WORK/cap3.log"
if [[ -n "$PRE_C1" ]]; then
  echo "   （对照改造前二进制：$PRE_C1）"
  run_agent "$PRE_C1" "$WORK/agent-notpl.yaml" "$WORK/cap4.jsonl" "$WORK/cap4.log"
else
  echo "   （未提供 pre-C1 二进制，跳过「与改造前上报体等价」对照；可传入路径开启）"
fi

echo "== 7) 断言 =="
python3 - "$WORK/cap1.jsonl" "$WORK/cap2.jsonl" "$WORK/cap3.jsonl" "${PRE_C1:-}" "$WORK" <<'PY'
import json
import os
import sys

cap1, cap2, cap3, _, work = sys.argv[1:6]
fails = []


def check(cond, msg):
    print(("  PASS  " if cond else "  FAIL  ") + msg)
    if not cond:
        fails.append(msg)


def load(path):
    with open(path) as fh:
        for line in fh:
            if line.strip():
                return json.loads(line)
    raise SystemExit(f"未捕获到上报：{path}")


def find(ms, name):
    return [m for m in ms if m["name"] == name]


def up_map(ms):
    return {m["labels"].get("instance"): m["value"] for m in find(ms, "template_target_up")}


p1, p2, p3 = load(cap1), load(cap2), load(cap3)
m1, m2, m3 = (p.get("metrics") or [] for p in (p1, p2, p3))

print("场景 1：三模板正常采集")
up = up_map(m1)
for inst in ("mq-01:15692", "app-01:8081", "web-01"):
    check(up.get(inst) == 1, f"{inst} 的 template_target_up=1")

depth = find(m1, "rabbitmq_queue_depth")
check(len(depth) == 2, "rename 生效：rabbitmq_queue_depth 2 条")
check(bool(depth) and depth[0]["value"] == 42, "取值正确 orders=42")
if depth:
    lb = depth[0]["labels"]
    check(lb.get("cluster") == "prod", "静态标签 cluster=prod 已追加")
    check(lb.get("template") == "rabbitmq" and lb.get("node") == "verify-node", "引擎标签已注入")
    check(lb.get("instance") == "mq-01:15692", "instance 标签已注入")
    check("job" not in lb, "unlabel 生效：job 已删除")
check(not find(m1, "rabbitmq_queue_consumers_total"), "drop 生效：_total 已丢弃")
check(not find(m1, "go_goroutines"), "keep 生效：非 rabbitmq_ 前缀已丢弃")
reqs = find(m1, "ownapp_requests_total")
check(bool(reqs) and reqs[0]["value"] == 1234, "http-json 路径取值正确")
check(bool(find(m1, "ownapp_node2")), "http-json 数组下标可取值")
conns = find(m1, "customtext_active_conns")
check(bool(conns) and conns[0]["value"] == 291, "http-text 正则抓取正确")

print("场景 2：一个 target 失败（隔离 + up=0）")
up2 = up_map(m2)
check(up2.get("app-01:8081") == 0, "失败 target up=0")
check(up2.get("mq-01:15692") == 1 and up2.get("web-01") == 1, "其它 target 不受影响")
check(not find(m2, "ownapp_queue_depth"), "失败 target 不产出数据指标")
check(len(find(m2, "rabbitmq_queue_depth")) == 2, "其它模板数据不受影响")

print("场景 3：无模板（零变化）")
check(not [m for m in m3 if m["name"].startswith("template_")], "不产出任何 template_* 指标")
check(len(m3) == 0, "全部采集器关闭且无模板时指标为空（模板是唯一来源）")

cap4 = os.path.join(work, "cap4.jsonl")
if os.path.exists(cap4) and os.path.getsize(cap4) > 0:
    print("场景 3b：与改造前二进制对照")
    p4 = load(cap4)
    m4 = p4.get("metrics") or []
    check(sorted(p3.keys()) == sorted(p4.keys()), "上报体顶层字段一致")
    check(sorted(m["name"] for m in m3) == sorted(m["name"] for m in m4), "指标集合一致")
    check(len(m3) == len(m4), "指标条数一致")

print()
if fails:
    print(f"共 {len(fails)} 项未通过")
    sys.exit(1)
print("全部通过")
PY
rc=$?

echo "== 8) 失败时的采集告警（应含模板 id/instance/URL，不含凭据）=="
grep -h '"msg":"模板采集失败"' "$WORK/cap2.log" 2>/dev/null | head -2 || true
exit $rc
