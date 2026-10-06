package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// 容器/K8s 只读查询动作（见 docs/superpowers/specs/2026-09-30-container-observability-design.md §3）。
//
// 链路：Server 提交任务 → 随 Agent 上报响应下发 → 这里执行只读 GET → 回执随下一轮上报送回。
// 因此延迟至少一个上报周期，接口语义是"异步任务"而不是"点开即返回"。

// maxContainerJSONBytes 是结构化结果的大小上限。
//
// 行数上限（collector 侧 300/500/200）已经压过一轮，这里只是兜底：
// 回执要搭在下一轮上报的请求体里，撑爆它的表现是"这台机器突然不上报了"，
// 比"表格少几行"严重得多——所以宁可整条失败并说清原因。
const maxContainerJSONBytes = 256 << 10

// K8sQuerier 是容器只读查询的本机实现，由 internal/agent/collector 提供。
//
// 定义成接口而不是直接依赖 collector：ops 包不需要知道 kubeconfig 怎么解析、apiserver 怎么连——
// 与凭据相关的东西只该待在一个地方。测试里也就能用一个假实现跑通护栏与分派。
type K8sQuerier interface {
	QueryWorkloads(ctx context.Context, cluster, namespace string) (*model.ContainerQueryResult, error)
	QueryPods(ctx context.Context, cluster, namespace string) (*model.ContainerQueryResult, error)
	QueryEvents(ctx context.Context, cluster, namespace, name string) (*model.ContainerQueryResult, error)
	QueryObject(ctx context.Context, cluster, namespace, resource, name string) (*model.ContainerQueryResult, error)
	// QueryLogs 拉取某个 Pod 的最近日志；tailLines/sinceSeconds 已在调用方按上限校过。
	QueryLogs(ctx context.Context, cluster, namespace, pod, container string, tailLines, sinceSeconds int) (*model.ContainerQueryResult, error)
}

// containerKinds 是容器类动作清单（全部只读）。
var containerKinds = []string{
	model.OpsKindContainerWorkloads,
	model.OpsKindContainerPods,
	model.OpsKindContainerDescribe,
	model.OpsKindContainerEvents,
	model.OpsKindContainerLogs,
}

// SetK8sQuerier 注入容器查询实现。
//
// 未注入时容器类动作**既不声明也不执行**：让能力协商如实显示"该节点不支持"，
// 好过接受任务后在执行期才失败——后者在界面上表现为一条永远不成功的任务。
func (e *Executor) SetK8sQuerier(q K8sQuerier) { e.k8s = q }

// runContainer 是容器类动作的统一入口：护栏 → 参数复校 → 查询 → 结果封装。
//
// Agent 侧**再校验一次参数**不是重复劳动：Server 校验保证"下发的东西是干净的"，
// 这里保证"即使中心版本不对或被绕过，本机也不会拿着拼出来的路径去请求 apiserver"。
func (e *Executor) runContainer(cmd model.OpsCommand) model.OpsResult {
	fail := func(msg string) model.OpsResult {
		return model.OpsResult{State: model.OpsStateFailed, Message: msg}
	}

	if !e.guards.OpsReadOnlyEnabled() {
		return fail("本机护栏未放行只读操作（agent.yaml 的 guards.ops.readOnly=false）")
	}
	if !e.guards.OpsContainerEnabled() {
		return fail("本机护栏未放行容器查询（agent.yaml 的 guards.ops.container=false）")
	}
	if e.k8s == nil {
		return fail("该 Agent 未启用容器查询（未配置 k8sInstances 或凭据未加载）")
	}

	cluster := strings.TrimSpace(cmd.Params["cluster"])
	if !model.OpsClusterPattern.MatchString(cluster) {
		return fail("集群标识不合法：" + cluster)
	}
	namespace := strings.TrimSpace(cmd.Params["namespace"])
	if namespace != "" && !model.OpsNamespacePattern.MatchString(namespace) {
		return fail("命名空间不合法：" + namespace)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()

	var (
		res *model.ContainerQueryResult
		err error
	)
	switch cmd.Kind {
	case model.OpsKindContainerWorkloads:
		res, err = e.k8s.QueryWorkloads(ctx, cluster, namespace)
	case model.OpsKindContainerPods:
		res, err = e.k8s.QueryPods(ctx, cluster, namespace)
	case model.OpsKindContainerEvents:
		name := strings.TrimSpace(cmd.Params["name"])
		if name != "" && !model.OpsObjectNamePattern.MatchString(name) {
			return fail("对象名不合法：" + name)
		}
		res, err = e.k8s.QueryEvents(ctx, cluster, namespace, name)
	case model.OpsKindContainerDescribe:
		resource := strings.TrimSpace(cmd.Params["resource"])
		if !model.OpsContainerResourcePattern.MatchString(resource) {
			return fail("资源类型不合法：" + resource)
		}
		name := strings.TrimSpace(cmd.Params["name"])
		if !model.OpsObjectNamePattern.MatchString(name) {
			return fail("对象名不合法：" + name)
		}
		res, err = e.k8s.QueryObject(ctx, cluster, namespace, resource, name)
	case model.OpsKindContainerLogs:
		// 日志必须指定命名空间：Pod 名只在命名空间内唯一，用"全部命名空间"去猜等于随机取一个。
		if namespace == "" {
			return fail("拉取日志必须指定命名空间")
		}
		name := strings.TrimSpace(cmd.Params["name"])
		if !model.OpsObjectNamePattern.MatchString(name) {
			return fail("Pod 名不合法：" + name)
		}
		container := strings.TrimSpace(cmd.Params["container"])
		if container != "" && !model.OpsNamespacePattern.MatchString(container) {
			return fail("容器名不合法：" + container)
		}
		tailLines, ok := boundedCount(cmd.Params["tailLines"], model.OpsLogDefaultTailLines, model.OpsLogMaxTailLines)
		if !ok {
			return fail(fmt.Sprintf("行数不合法（应为 1-%d 的整数）：%s", model.OpsLogMaxTailLines, cmd.Params["tailLines"]))
		}
		// 时间窗留空表示"不按时间过滤"，所以下界是 0 而不是 1。
		sinceSeconds, ok := boundedCount(cmd.Params["sinceSeconds"], 0, model.OpsLogMaxSinceSeconds)
		if !ok {
			return fail(fmt.Sprintf("时间窗不合法（应为 1-%d 秒的整数）：%s", model.OpsLogMaxSinceSeconds, cmd.Params["sinceSeconds"]))
		}
		res, err = e.k8s.QueryLogs(ctx, cluster, namespace, name, container, tailLines, sinceSeconds)
	default:
		return fail("本机不支持该动作：" + cmd.Kind)
	}
	if err != nil {
		return fail("查询失败：" + err.Error())
	}

	payload, mErr := json.Marshal(res)
	if mErr != nil {
		return fail("结果序列化失败：" + mErr.Error())
	}
	if len(payload) > maxContainerJSONBytes {
		return fail(fmt.Sprintf("查询结果过大（%d 字节，上限 %d），请缩小命名空间范围后重试",
			len(payload), maxContainerJSONBytes))
	}

	summary := containerSummary(res)
	return model.OpsResult{
		State:   model.OpsStateSucceeded,
		Message: summary,
		// Data 只放一行摘要：结构化内容在 JSON 里，刻意不把同一份数据存两遍。
		Data: map[string]string{"概览": summary},
		JSON: string(payload),
	}
}

// boundedCount 解析可选的正整数参数：留空取默认值，非数字或超界一律拒绝。
//
// 为什么"超界"也拒绝而不是静默夹到上限：静默夹值会让用户以为"我拉了 5000 行"，
// 而实际只有 500 行——他可能据此判断"日志里没有那条错误"，这是最坏的一类误判。
func boundedCount(raw string, def, max int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > max {
		return 0, false
	}
	if n == 0 && def > 0 {
		return 0, false // 显式写 0 与"留空"语义不同，行数不能为 0
	}
	return n, true
}

// containerSummary 生成一句给人看的结论（任务列表里直接显示 Message）。
func containerSummary(res *model.ContainerQueryResult) string {
	if res == nil {
		return "查询完成，无结果"
	}
	where := res.Cluster
	if res.Namespace != "" {
		where += " / " + res.Namespace
	}
	s := fmt.Sprintf("%s：返回 %d 条（共 %d 个对象）", where, len(res.Rows), res.Total)
	if res.Truncated {
		// 截断必须说出来：否则使用者会以为"集群里就这么多"。
		s += "，已截断，仅显示前若干条"
	}
	if res.Notice != "" {
		s += "；" + res.Notice
	}
	return s
}
