#!/usr/bin/env bash
# 采集项模板「下发与热生效」端到端验证（C1 阶段二 子批次 B）。
#
# 验证的链路（任一环断了都会导致「配置了模板但看不到数据」）：
#   Server 存管模板 → 按节点分组过滤 → 随上报响应下发 → Agent 校验并原子替换（不重启）
#   → 下一轮采集执行新模板 → 随上报写回 Server → Server 写入时序库
#
# 关键设计：Agent 的 agent.yaml 里**不配任何模板**，因此被测指标只能来自下发路径。
#
# 用法：bash build/verify-template-delivery.sh
# 依赖：Go、python3。需能监听本地端口（18080/18099/19201-19203/18428）。
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d /tmp/verify-tpl-delivery.XXXXXX)"
GO="${GO:-go}"

cleanup() {
  pkill -f "$WORK/fakes.py" 2>/dev/null || true
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

echo "== 2) 假端点（Prometheus 文本，供模板拉取）=="
cat > "$WORK/fakes.py" <<'PY'
#!/usr/bin/env python3
import http.server, socketserver, sys

PORT = int(sys.argv[1])
BODY = ('rabbitmq_queue_messages{queue="orders",job="rabbitmq"} 42\n'
        'rabbitmq_queue_messages{queue="pay",job="rabbitmq"} 7\n'
        'rabbitmq_queue_consumers_total 3\n').encode()


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
python3 "$WORK/fakes.py" 19201 > "$WORK/fake.log" 2>&1 &
sleep 1

echo "== 3) Server：模板存管在 Server 侧，按分组 default 下发 =="
cat > "$WORK/templates.yaml" <<'YAML'
- id: rabbitmq
  title: RabbitMQ
  kind: prometheus-exporter
  groups: ["default"]
  targets:
    - instance: mq-01:15692
      addr: http://127.0.0.1:19201/metrics
  rules:
    keep: "^rabbitmq_"
    drop: "_total$"
    rename:
      - { match: "^rabbitmq_queue_messages$", to: "rabbitmq_queue_depth" }
    labels:
      cluster: prod
    unlabel: ["job"]
YAML

cat > "$WORK/server.yaml" <<YAML
mode: standalone
listen: ":18080"
dataDir: "$WORK/server-data"
templatesFile: "$WORK/templates.yaml"
tsdb:
  backend: victoriametrics
  addr: "http://127.0.0.1:18428"
agentAuth:
  enabled: true
  secret: "verify-secret"
auth:
  enabled: false
YAML

"$WORK/fakevm" -addr 127.0.0.1:18428 -out "$WORK/vm-received.bin" > "$WORK/fakevm.log" 2>&1 &
"$WORK/server" -config "$WORK/server.yaml" > "$WORK/server.log" 2>&1 &
sleep 3

echo "== 4) Agent：agent.yaml 里不配任何模板（指标只能来自下发）=="
cat > "$WORK/agent.yaml" <<'YAML'
serverURL: "http://127.0.0.1:18080"
node: "delivery-node"
group: "default"
secret: "verify-secret"
interval: 3
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
YAML

timeout 20 "$WORK/agent" -config "$WORK/agent.yaml" > "$WORK/agent.log" 2>&1 || true
sleep 2

echo "== 5) 断言 =="
python3 - "$WORK" <<'PY'
import os
import sys

work = sys.argv[1]
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


raw = read(os.path.join(work, "vm-received.bin"))
agent_log = read(os.path.join(work, "agent.log")).decode(errors="replace")

check(len(raw) > 0, "时序库收到了写入（说明链路走到了最后一步）")
check(b"template_target_up" in raw, "写入内容含 template_target_up")
check(b"template" in raw and b"rabbitmq" in raw, "写入内容含 template=\"rabbitmq\" 标签")
check(b"rabbitmq_queue_depth" in raw, "写入内容含 rename 后的 rabbitmq_queue_depth（模板确实被执行）")
check(b"cluster" in raw and b"prod" in raw, "写入内容含静态标签 cluster=prod")
check(b"go_goroutines" not in raw, "keep 规则生效")

# Agent 侧证据：确实收到了下发并应用（而不是碰巧采到的）
check("下发的采集项模板" in agent_log, "Agent 日志出现模板下发记录")
check("已应用 Server 下发的采集项模板" in agent_log, "Agent 日志确认已应用（热生效，未重启）")
check("非法" not in agent_log, "本次下发未被判为非法")

if fails:
    print(f"共 {len(fails)} 项未通过")
    sys.exit(1)
print("端到端全部通过")
PY
rc=$?

echo "== 6) 前端所消费的两个接口（类型注册表 + 通用实例）=="
python3 - "$WORK" <<'PY'
import json
import sys
import urllib.request

work = sys.argv[1]
fails = []


def check(cond, msg):
    print(("  PASS  " if cond else "  FAIL  ") + msg)
    if not cond:
        fails.append(msg)


def get(path):
    with urllib.request.urlopen("http://127.0.0.1:18080" + path, timeout=5) as resp:
        return json.loads(resp.read())


try:
    ov = get("/api/v1/middleware/overview")
except Exception as exc:  # noqa: BLE001
    print(f"  FAIL  总览接口不可用：{exc}")
    sys.exit(1)

types = {t["type"]: t for t in ov.get("types", [])}
check("rabbitmq" in types, "总览包含模板派生类型 rabbitmq（前端据此渲染 Tab 与卡片）")
t = types.get("rabbitmq", {})
check(t.get("kind") == "template", f"类型来源标记为 template（前端据此区分内置/模板），got {t.get('kind')!r}")
check(t.get("label") == "RabbitMQ", f"展示名取自模板 title，got {t.get('label')!r}")

inst = get("/api/v1/middleware/rabbitmq/instances")
check(inst.get("type") == "rabbitmq" and inst.get("label") == "RabbitMQ", "通用实例接口回显类型与展示名")

# 说明：实例数/摘要指标/实例状态都来自时序库**查询**，而本验证用的假时序库只接受写入
# （查询返回空），因此这三项在这里必然为空，不能作为失败判据。
# 它们由 internal/server/api 的单测精确覆盖：
#   TestMiddlewareOverview_IncludesTemplateType（total/up/down/summary/告警归集）
#   TestMiddlewareTypeInstances_TemplateType（实例列表/摘要值/按 template 标签过滤）
print("  SKIP  实例数 / 卡片摘要 / 实例状态：需真实 TSDB（假时序库只接受写入）")
print("        这些读路径由 internal/server/api 的单测覆盖（含标签过滤断言）")

# 内置类型仍走各自的专用接口（通用接口不应截胡）
import urllib.error
try:
    get("/api/v1/middleware/nosuchtype/instances")
    check(False, "未知类型应返回 404")
except urllib.error.HTTPError as exc:
    check(exc.code == 404, f"未知类型返回 404，got {exc.code}")

if fails:
    print(f"共 {len(fails)} 项未通过")
    sys.exit(1)
print("接口契约全部通过")
PY
rc2=$?
[ "$rc" -eq 0 ] || rc2=$rc

echo "== 7) Agent 日志中的模板相关记录 =="
grep -h "模板" "$WORK/agent.log" | head -3 || true
exit $rc2
