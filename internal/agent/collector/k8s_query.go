package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// 容器只读查询：供统一下行操作通道的 container.* 动作调用（见
// docs/superpowers/specs/2026-09-30-container-observability-design.md §3）。
//
// 为什么放在 collector 包：apiserver 的凭据（kubeconfig / token）只在这个包里被解析，
// 复制一份到 ops 包就等于多一个"凭据可能被打印或误上报"的地方。
//
// 三条硬约束：
//  1. **只发 GET**，不带任何请求体；写操作（exec）属 P2，不在本文件。
//  2. **行数有界**：所有列表都用 apiserver 的 `limit` + 自己的行上限，超出即标 truncated，
//     绝不"先全拉回来再截断"——集群大了会把 Agent 内存和上行体积一起拖垮。
//  3. **详情走白名单投影**，不把对象 JSON 原样脱敏后透出（理由见 projectObject 的注释）。

// 各类列表的行上限。取值依据：够覆盖常见集群的一次排障，同时把回执体积压在几十 KB 量级。
const (
	containerMaxWorkloads = 300
	containerMaxPods      = 500
	containerMaxEvents    = 200
)

// ContainerQueryResult 定义在 internal/model——它**是协议载荷**（随 OpsResult.JSON 上行、
// 被前端解析），不只属于本包。这里用别名，让本文件的签名读起来短一些。
type ContainerQueryResult = model.ContainerQueryResult

// ---- apiserver 返回体的最小结构 ----
//
// 只声明用得到的字段：K8s 的响应结构体极大，全量声明既没人维护也容易过时。

type cqMeta struct {
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	CreationTimestamp string `json:"creationTimestamp"`
}

type cqListMeta struct {
	// Continue 非空表示还有下一页（我们不会去翻，只用来标记 truncated）。
	Continue string `json:"continue"`
}

type cqWorkload struct {
	Metadata cqMeta `json:"metadata"`
	Spec     struct {
		Replicas *int32 `json:"replicas"`
	} `json:"spec"`
	Status struct {
		Replicas               int32 `json:"replicas"`
		ReadyReplicas          int32 `json:"readyReplicas"`
		AvailableReplicas      int32 `json:"availableReplicas"`
		NumberReady            int32 `json:"numberReady"`
		NumberScheduled        int32 `json:"numberScheduled"`
		DesiredNumberScheduled int32 `json:"desiredNumberScheduled"`
		Succeeded              int32 `json:"succeeded"`
		Failed                 int32 `json:"failed"`
		Active                 int32 `json:"active"`
	} `json:"status"`
}

type cqWorkloadList struct {
	Metadata cqListMeta   `json:"metadata"`
	Items    []cqWorkload `json:"items"`
}

type cqPod struct {
	Metadata cqMeta `json:"metadata"`
	Spec     struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase     string `json:"phase"`
		PodIP     string `json:"podIP"`
		StartTime string `json:"startTime"`
		// 容器级状态（复用 k8s.go 的类型）：ready/restartCount 是排障第一眼要看的，
		// state 里的 waiting/terminated reason 才是"这个 Pod 到底怎么了"的答案。
		ContainerStatuses []k8sContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type cqPodList struct {
	Metadata cqListMeta `json:"metadata"`
	Items    []cqPod    `json:"items"`
}

type cqEvent struct {
	Metadata cqMeta `json:"metadata"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Count    int32  `json:"count"`
	// 三个时间字段都可能为空：不同来源（kubelet / controller）填的不是同一个。
	LastTimestamp  string `json:"lastTimestamp"`
	EventTime      string `json:"eventTime"`
	FirstTimestamp string `json:"firstTimestamp"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"involvedObject"`
}

type cqEventList struct {
	Metadata cqListMeta `json:"metadata"`
	Items    []cqEvent  `json:"items"`
}

// K8s 返回本机的 K8s 采集器，供下行动作通道做容器只读查询；**未配置任何集群时返回 nil**。
//
// 返回 nil 而不是一个空采集器是有意的：调用方用它决定"要不要声明容器查询能力"，
// 而一台没配集群的机器声明了这项能力，只会让中心下发一条注定失败的任务。
func (c *Collector) K8s() *K8sCollector {
	if c.k8s == nil || len(c.k8s.instances) == 0 {
		return nil
	}
	return c.k8s
}

// ---- 入口 ----

// queryConn 按集群标识找到配置并建连。
//
// 集群标识优先匹配 agent.yaml 里 k8sInstances[].name；为空时回落到 instance（apiserver 地址）：
// 有些部署不给集群起名，此时上报的 Name 是空的，而前端拿到的也正是空字符串。
func (c *K8sCollector) queryConn(cluster string) (model.K8sInstanceConfig, *k8sConn, error) {
	if len(c.instances) == 0 {
		return model.K8sInstanceConfig{}, nil, errors.New("本机未配置任何 Kubernetes 集群（agent.yaml 的 k8sInstances）")
	}
	cluster = strings.TrimSpace(cluster)
	var picked *model.K8sInstanceConfig
	for i := range c.instances {
		cfg := &c.instances[i]
		if cluster == "" || cfg.Name == cluster {
			picked = cfg
			break
		}
		if cfg.Name == "" && strings.TrimRight(cfg.APIServer, "/") == cluster {
			picked = cfg
			break
		}
	}
	if picked == nil {
		names := make([]string, 0, len(c.instances))
		for _, cfg := range c.instances {
			if cfg.Name != "" {
				names = append(names, cfg.Name)
			} else {
				names = append(names, cfg.APIServer)
			}
		}
		return model.K8sInstanceConfig{}, nil, fmt.Errorf("本机没有名为 %q 的集群（可选：%s）", cluster, strings.Join(names, " / "))
	}
	conn, err := buildK8sConn(*picked)
	if err != nil {
		return *picked, nil, err
	}
	return *picked, conn, nil
}

func newContainerResult(kind, cluster, namespace string, columns []string) *ContainerQueryResult {
	return &ContainerQueryResult{
		Kind: kind, Cluster: cluster, Namespace: namespace,
		Columns: columns, Rows: make([][]string, 0, 16),
		FetchedAt: model.NowMillis(),
	}
}

// addRow 追加一行。类型定义在 model，所以这里只能是自由函数而不是方法。
func addRow(r *ContainerQueryResult, row ...string) { r.Rows = append(r.Rows, row) }

// QueryWorkloads 列工作负载与副本就绪情况。
func (c *K8sCollector) QueryWorkloads(ctx context.Context, cluster, namespace string) (*ContainerQueryResult, error) {
	cfg, conn, err := c.queryConn(cluster)
	if err != nil {
		return nil, err
	}
	defer conn.client.CloseIdleConnections()

	res := newContainerResult(model.OpsKindContainerWorkloads, cfg.Name, namespace,
		[]string{"类型", "命名空间", "名称", "期望", "就绪", "可用", "状态"})

	appsBase := "/apis/apps/v1"
	batchBase := "/apis/batch/v1"
	if namespace != "" {
		esc := url.PathEscape(namespace)
		appsBase = "/apis/apps/v1/namespaces/" + esc
		batchBase = "/apis/batch/v1/namespaces/" + esc
	}

	targets := []struct {
		label string
		base  string
		path  string
		limit int
	}{
		{"Deployment", appsBase, "/deployments", containerMaxWorkloads},
		{"StatefulSet", appsBase, "/statefulsets", containerMaxWorkloads},
		{"DaemonSet", appsBase, "/daemonsets", containerMaxWorkloads},
		{"Job", batchBase, "/jobs", containerMaxWorkloads},
	}
	for _, t := range targets {
		if err := c.appendWorkloads(ctx, conn, res, t.label, t.base+t.path, t.limit); err != nil {
			// 单个类型拉不到（例如没有 batch 权限）不该让整次查询失败——其余类型的结果仍然有用。
			res.Notice = appendNotice(res.Notice, fmt.Sprintf("%s 读取失败：%v", t.label, err))
		}
	}
	return res, nil
}

func (c *K8sCollector) appendWorkloads(ctx context.Context, conn *k8sConn, res *ContainerQueryResult, label, path string, limit int) error {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	var list cqWorkloadList
	if err := c.getJSON(ctx, conn, path+"?"+q.Encode(), &list); err != nil {
		return err
	}
	for _, it := range list.Items {
		res.Total++
		if len(res.Rows) >= limit {
			res.Truncated = true
			continue
		}
		desired, ready, extra, state := workloadReplicas(label, it)
		addRow(res, label, it.Metadata.Namespace, it.Metadata.Name, desired, ready, extra, state)
	}
	if list.Metadata.Continue != "" {
		res.Truncated = true
	}
	return nil
}

// workloadReplicas 把四种工作负载的"期望/就绪/可用/结论"归一成四列。
//
// 它们的字段名并不统一（Deployment 是 spec.replicas，DaemonSet 是 desiredNumberScheduled/
// numberReady，Job 是 active/succeeded/failed），这里做一次归一，前端就不必知道对象是哪种类型。
//
// 结论按类型分别判定：把 Job 的"成功 3/失败 1"套成 Deployment 的"副本不足"是错的——
// Job 跑完就该退出，它不是"没就绪"。
func workloadReplicas(label string, it cqWorkload) (desired, ready, extra, state string) {
	switch label {
	case "DaemonSet":
		desired = itoa32(it.Status.DesiredNumberScheduled)
		ready = itoa32(it.Status.NumberReady)
		extra = itoa32(it.Status.NumberScheduled)
		switch {
		case desired == "0":
			state = "无匹配节点"
		case desired == ready:
			state = "就绪"
		default:
			state = "副本不足"
		}
	case "Job":
		desired = "—"
		ready = itoa32(it.Status.Active)
		extra = fmt.Sprintf("成功 %s / 失败 %s", itoa32(it.Status.Succeeded), itoa32(it.Status.Failed))
		switch {
		case it.Status.Failed > 0:
			state = "有失败"
		case it.Status.Active > 0:
			state = "运行中"
		default:
			state = "已完成"
		}
	default:
		n := int32(0)
		if it.Spec.Replicas != nil {
			n = *it.Spec.Replicas
		}
		desired = itoa32(n)
		ready = itoa32(it.Status.ReadyReplicas)
		extra = itoa32(it.Status.AvailableReplicas)
		switch {
		case n == 0:
			state = "已停止"
		case it.Status.ReadyReplicas == n:
			state = "就绪"
		default:
			state = "副本不足"
		}
	}
	return desired, ready, extra, state
}

// QueryPods 列 Pod 的状态、重启次数与所在节点。
func (c *K8sCollector) QueryPods(ctx context.Context, cluster, namespace string) (*ContainerQueryResult, error) {
	cfg, conn, err := c.queryConn(cluster)
	if err != nil {
		return nil, err
	}
	defer conn.client.CloseIdleConnections()

	res := newContainerResult(model.OpsKindContainerPods, cfg.Name, namespace,
		[]string{"命名空间", "名称", "状态", "就绪", "重启", "节点", "Pod IP"})

	path := "/api/v1/pods"
	if namespace != "" {
		path = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(containerMaxPods))
	var list cqPodList
	if err := c.getJSON(ctx, conn, path+"?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	for _, it := range list.Items {
		res.Total++
		if len(res.Rows) >= containerMaxPods {
			res.Truncated = true
			continue
		}
		ready, restarts := 0, int32(0)
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}
		// 状态列用**有效状态**（对齐 kubectl 的 STATUS），而不是裸 phase：
		// 只报 phase 会把 ImagePullBackOff 显示成 Pending、把 CrashLoopBackOff
		// 显示成 Running——后者看起来是健康的，正好把要排的故障藏起来。
		addRow(res, it.Metadata.Namespace, it.Metadata.Name,
			fallback(podStatus(it.Status.Phase, it.Status.ContainerStatuses), "Unknown"),
			fmt.Sprintf("%d/%d", ready, len(it.Status.ContainerStatuses)),
			itoa32(restarts),
			fallback(it.Spec.NodeName, "—"),
			fallback(it.Status.PodIP, "—"))
	}
	if list.Metadata.Continue != "" {
		res.Truncated = true
	}
	return res, nil
}

// QueryEvents 列事件，按时间倒序取最近若干条。
func (c *K8sCollector) QueryEvents(ctx context.Context, cluster, namespace, name string) (*ContainerQueryResult, error) {
	cfg, conn, err := c.queryConn(cluster)
	if err != nil {
		return nil, err
	}
	defer conn.client.CloseIdleConnections()

	res := newContainerResult(model.OpsKindContainerEvents, cfg.Name, namespace,
		[]string{"时间", "类型", "对象", "原因", "次数", "内容"})

	path := "/api/v1/events"
	if namespace != "" {
		path = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/events"
	}
	// fieldSelector 在服务端过滤，比拉回来再筛省一个数量级——事件在一个吵闹的集群里非常多。
	q := url.Values{}
	q.Set("limit", strconv.Itoa(containerMaxEvents))
	if name != "" {
		q.Set("fieldSelector", "involvedObject.name="+name)
	}
	var list cqEventList
	if err := c.getJSON(ctx, conn, path+"?"+q.Encode(), &list); err != nil {
		return nil, err
	}

	items := list.Items
	sort.SliceStable(items, func(i, j int) bool {
		return eventTime(items[i]) > eventTime(items[j])
	})
	for _, it := range items {
		res.Total++
		if len(res.Rows) >= containerMaxEvents {
			res.Truncated = true
			continue
		}
		obj := it.InvolvedObject.Kind + "/" + it.InvolvedObject.Name
		addRow(res, formatK8sTime(eventTime(it)), fallback(it.Type, "—"), obj,
			fallback(it.Reason, "—"), itoa32(it.Count), truncateLine(it.Message, 300))
	}
	if list.Metadata.Continue != "" {
		res.Truncated = true
	}
	return res, nil
}

func eventTime(e cqEvent) string {
	for _, t := range []string{e.LastTimestamp, e.EventTime, e.FirstTimestamp} {
		if t != "" {
			return t
		}
	}
	return ""
}

// ---- 单对象详情 ----

// QueryObject 返回单个对象的详情（白名单投影）。
//
// 刻意**不做**"把对象 JSON 脱敏后原样透出"：那样只要漏一个字段就是一次凭据泄露，
// 而 Pod 的 `env[].value`、ConfigMap 的 `data`、注解里的 `last-applied-configuration`
// 都可能含明文密钥。这里改成按资源类型逐项投影——只取排障真正需要的字段，
// 敏感内容压根不进返回体。
func (c *K8sCollector) QueryObject(ctx context.Context, cluster, namespace, resource, name string) (*ContainerQueryResult, error) {
	cfg, conn, err := c.queryConn(cluster)
	if err != nil {
		return nil, err
	}
	defer conn.client.CloseIdleConnections()

	res := newContainerResult(model.OpsKindContainerDescribe, cfg.Name, namespace,
		[]string{"字段", "值"})
	addRow(res, "资源", resource+"/"+name)

	path, ok := objectPath(resource, namespace, name)
	if !ok {
		return nil, fmt.Errorf("不支持的资源类型 %q", resource)
	}
	var raw map[string]any
	if err := c.getJSON(ctx, conn, path, &raw); err != nil {
		return nil, err
	}

	switch resource {
	case "pods":
		projectPod(raw, res)
	case "deployments", "statefulsets", "daemonsets":
		projectWorkload(raw, res)
	case "jobs":
		projectJob(raw, res)
	case "services":
		projectService(raw, res)
	case "configmaps":
		projectConfigMap(raw, res)
	}
	return res, nil
}

// objectPath 把「资源类型 + 命名空间 + 名字」映射成 apiserver 路径。
// 返回 ok=false 表示不在白名单里——**路径是拼出来的，所以白名单必须在拼之前生效**。
func objectPath(resource, namespace, name string) (string, bool) {
	esc := url.PathEscape(namespace)
	escName := url.PathEscape(name)
	switch resource {
	case "pods":
		return "/api/v1/namespaces/" + esc + "/pods/" + escName, true
	case "services":
		return "/api/v1/namespaces/" + esc + "/services/" + escName, true
	case "configmaps":
		return "/api/v1/namespaces/" + esc + "/configmaps/" + escName, true
	case "deployments":
		return "/apis/apps/v1/namespaces/" + esc + "/deployments/" + escName, true
	case "statefulsets":
		return "/apis/apps/v1/namespaces/" + esc + "/statefulsets/" + escName, true
	case "daemonsets":
		return "/apis/apps/v1/namespaces/" + esc + "/daemonsets/" + escName, true
	case "jobs":
		return "/apis/batch/v1/namespaces/" + esc + "/jobs/" + escName, true
	}
	return "", false
}

func projectPod(raw map[string]any, res *ContainerQueryResult) {
	meta, _ := raw["metadata"].(map[string]any)
	spec, _ := raw["spec"].(map[string]any)
	status, _ := raw["status"].(map[string]any)

	addRow(res, "命名空间", str(meta["namespace"]))
	addRow(res, "创建时间", formatK8sTime(str(meta["creationTimestamp"])))
	addRow(res, "标签", joinMap(meta["labels"]))
	addRow(res, "节点", str(spec["nodeName"]))
	cstatus := rawContainerStatuses(status["containerStatuses"])
	addRow(res, "Pod IP", str(status["podIP"]))
	// 状态给**有效状态**（对齐 kubectl 的 STATUS）：只给 phase 时，
	// CrashLoopBackOff 的 Pod 会显示成 Running —— 看起来是健康的。
	addRow(res, "状态", fallback(podStatus(str(status["phase"]), cstatus), "—"))
	addRow(res, "QoS", str(status["qosClass"]))

	// 只列容器名与镜像：env 的**值**可能含明文密钥，一律不下发。
	for _, field := range []string{"containers", "initContainers"} {
		list, _ := spec[field].([]any)
		for _, it := range list {
			c, _ := it.(map[string]any)
			addRow(res, field, str(c["name"])+" → "+str(c["image"]))
		}
	}
	// 容器级原因：这才是"这个 Pod 为什么起不来"的答案（镜像拉不动 / 反复崩溃 / OOM）。
	// 光有容器名和镜像不够——排查时第一眼要看的就是这里。
	for _, c := range cstatus {
		addRow(res, "容器状态", containerStatusText(c))
	}
	appendEnvNames(res, spec, "containers", "容器环境变量名")
	appendConditions(res, status)
	res.Notice = appendNotice(res.Notice, "环境变量只返回变量名，不返回值；密钥类字段不下发")
}

// rawContainerStatuses 把原始对象里的 containerStatuses 转成共用类型。
// 这里走一次 JSON 往返，而不是手写字段提取：目的是让"有效状态"始终只有
// podStatus（k8s.go）一处实现。手写提取等于把判定逻辑复制一遍，
// 采集页与查询页迟早会给出互相矛盾的结论。
func rawContainerStatuses(v any) []k8sContainerStatus {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []k8sContainerStatus
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// containerStatusText 一行说清一个容器的处境：状态（含原因）与重启次数。
func containerStatusText(c k8sContainerStatus) string {
	state := "Running"
	if r := strings.TrimSpace(c.State.Waiting.Reason); r != "" {
		state = r
	} else if t := c.State.Terminated; t != nil {
		switch {
		case strings.TrimSpace(t.Reason) != "":
			state = t.Reason
		case t.ExitCode != 0:
			state = "Error"
		default:
			state = "Completed"
		}
	}
	s := c.Name + ": " + state
	if c.RestartCount > 0 {
		s += fmt.Sprintf("（重启 %d 次）", c.RestartCount)
	}
	return s
}

func projectWorkload(raw map[string]any, res *ContainerQueryResult) {
	meta, _ := raw["metadata"].(map[string]any)
	spec, _ := raw["spec"].(map[string]any)
	status, _ := raw["status"].(map[string]any)

	addRow(res, "命名空间", str(meta["namespace"]))
	addRow(res, "创建时间", formatK8sTime(str(meta["creationTimestamp"])))
	addRow(res, "标签", joinMap(meta["labels"]))
	addRow(res, "期望副本", str(spec["replicas"]))
	addRow(res, "就绪副本", str(status["readyReplicas"]))
	addRow(res, "可用副本", str(status["availableReplicas"]))
	addRow(res, "更新副本", str(status["updatedReplicas"]))
	tmpl, _ := spec["template"].(map[string]any)
	tmplSpec, _ := tmpl["spec"].(map[string]any)
	for _, field := range []string{"containers", "initContainers"} {
		list, _ := tmplSpec[field].([]any)
		for _, it := range list {
			c, _ := it.(map[string]any)
			addRow(res, field, str(c["name"])+" → "+str(c["image"]))
		}
	}
	appendConditions(res, status)
}

func projectJob(raw map[string]any, res *ContainerQueryResult) {
	meta, _ := raw["metadata"].(map[string]any)
	status, _ := raw["status"].(map[string]any)
	addRow(res, "命名空间", str(meta["namespace"]))
	addRow(res, "创建时间", formatK8sTime(str(meta["creationTimestamp"])))
	addRow(res, "活跃", str(status["active"]))
	addRow(res, "成功", str(status["succeeded"]))
	addRow(res, "失败", str(status["failed"]))
	addRow(res, "开始时间", formatK8sTime(str(status["startTime"])))
	addRow(res, "完成时间", formatK8sTime(str(status["completionTime"])))
	appendConditions(res, status)
}

func projectService(raw map[string]any, res *ContainerQueryResult) {
	meta, _ := raw["metadata"].(map[string]any)
	spec, _ := raw["spec"].(map[string]any)
	addRow(res, "命名空间", str(meta["namespace"]))
	addRow(res, "类型", str(spec["type"]))
	addRow(res, "ClusterIP", str(spec["clusterIP"]))
	addRow(res, "选择器", joinMap(spec["selector"]))
	ports, _ := spec["ports"].([]any)
	for _, it := range ports {
		p, _ := it.(map[string]any)
		addRow(res, "端口", fmt.Sprintf("%s:%s/%s", str(p["name"]), str(p["port"]), fallback(str(p["protocol"]), "TCP")))
	}
}

func projectConfigMap(raw map[string]any, res *ContainerQueryResult) {
	meta, _ := raw["metadata"].(map[string]any)
	data, _ := raw["data"].(map[string]any)
	binary, _ := raw["binaryData"].(map[string]any)
	addRow(res, "命名空间", str(meta["namespace"]))
	// 只给键名：ConfigMap 的 value 在生产里经常被拿来放配置口令。
	for k := range data {
		addRow(res, "配置键", k)
	}
	for k := range binary {
		addRow(res, "二进制键", k)
	}
	res.Notice = appendNotice(res.Notice, "ConfigMap 只返回键名，不返回值（内容可能含凭据）")
}

func appendEnvNames(res *ContainerQueryResult, spec map[string]any, field, label string) {
	list, _ := spec[field].([]any)
	for _, it := range list {
		c, _ := it.(map[string]any)
		envs, _ := c["env"].([]any)
		names := make([]string, 0, len(envs))
		for _, e := range envs {
			em, _ := e.(map[string]any)
			if n := str(em["name"]); n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			addRow(res, label, str(c["name"])+": "+strings.Join(names, ", "))
		}
	}
}

func appendConditions(res *ContainerQueryResult, status map[string]any) {
	conds, _ := status["conditions"].([]any)
	for _, it := range conds {
		c, _ := it.(map[string]any)
		addRow(res, "条件", fmt.Sprintf("%s=%s %s", str(c["type"]), str(c["status"]), truncateLine(str(c["message"]), 160)))
	}
}

// ---- 小工具 ----

func itoa32(v int32) string { return strconv.FormatInt(int64(v), 10) }

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// JSON 数字统一解成 float64；整数值不要显示成 3.000000。
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func joinMap(v any) string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k+"="+str(m[k]))
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// truncateLine 按字符（不是字节）截断，避免把多字节汉字切成乱码。
func truncateLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// formatK8sTime 把 RFC3339 压成 "2026-10-02 08:30:00"；解析不了就原样返回。
func formatK8sTime(s string) string {
	if s == "" || s == "<nil>" {
		return "—"
	}
	if len(s) >= 19 && s[4] == '-' && s[10] == 'T' {
		return s[:10] + " " + s[11:19]
	}
	return s
}

func appendNotice(cur, add string) string {
	if cur == "" {
		return add
	}
	return cur + "；" + add
}
