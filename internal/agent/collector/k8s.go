package collector

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
)

// K8sCollector 采集 Kubernetes 集群指标，支持直连 apiserver（kubeconfig/token）与
// kube-state-metrics exporter 双模式。每个 cfg 对应一个 K8s 集群。
type K8sCollector struct {
	node      string
	instances []model.K8sInstanceConfig
}

// NewK8sCollector 创建 K8sCollector。
func NewK8sCollector(node string, instances []model.K8sInstanceConfig) *K8sCollector {
	return &K8sCollector{node: node, instances: instances}
}

// Collect 采集所有 K8s 集群指标（等价于 CollectCtx(context.Background())）。
func (c *K8sCollector) Collect() ([]model.Metric, []model.K8sInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 K8s 集群指标；ctx 取消或超时后停止采集剩余集群。
func (c *K8sCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.K8sInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.K8sInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("K8s 采集被中断，跳过剩余集群", "err", err)
			break
		}
		m, ki := c.collectCluster(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ki)
	}
	return metrics, instances
}

// collectCluster 采集单个 K8s 集群。
func (c *K8sCollector) collectCluster(ctx context.Context, cfg model.K8sInstanceConfig, now int64) ([]model.Metric, model.K8sInstance) {
	// 解析连接信息（apiserver 地址、token、TLS）
	conn, err := buildK8sConn(cfg)
	inst := model.K8sInstance{
		Instance: cfg.APIServer,
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	if conn != nil {
		inst.Instance = conn.apiServer
	}
	if conn != nil {
		// conn 按采集周期创建；采集结束后关闭自定义 Transport 的空闲连接，
		// 避免 apiserver keep-alive 连接随周期累积。
		defer conn.client.CloseIdleConnections()
	}
	if err != nil {
		slog.Warn("K8s 连接配置解析失败", "name", cfg.Name, "err", err)
		return []model.Metric{c.mk("k8s_cluster_up", 0, cfg, conn, nil, now)}, inst
	}

	// exporter 模式：抓取 kube-state-metrics /metrics 文本
	if cfg.ExporterURL != "" {
		m, up, version := c.collectExporter(ctx, cfg, conn, now)
		inst.Up = up
		inst.Version = version
		return m, inst
	}

	// 直连模式
	var out []model.Metric

	// 1. /version 探测存活与版本
	version, err := c.getVersion(ctx, conn)
	up := 0.0
	if err == nil {
		up = 1
		inst.Up = true
		inst.Version = version
	} else {
		slog.Warn("K8s apiserver 不可达", "name", cfg.Name, "apiServer", conn.apiServer, "err", err)
	}
	out = append(out, c.mk("k8s_cluster_up", up, cfg, conn, map[string]string{"version": version}, now))
	if up == 0 {
		return out, inst
	}

	// 2. 节点
	out = append(out, c.collectNodes(ctx, cfg, conn, now)...)
	// 3. 工作负载（Deployment / StatefulSet / DaemonSet）
	out = append(out, c.collectWorkloads(ctx, cfg, conn, now)...)
	// 4. Pod
	out = append(out, c.collectPods(ctx, cfg, conn, now)...)
	// 5. metrics-server（可选）
	if cfg.MetricsServer {
		out = append(out, c.collectNodeMetrics(ctx, cfg, conn, now)...)
	}

	return out, inst
}

// ---- 指标构造 ----

// mk 构造一个带集群级标签（instance/group）的指标，extra 追加下钻维度标签。
func (c *K8sCollector) mk(name string, val float64, cfg model.K8sInstanceConfig, conn *k8sConn, extra map[string]string, now int64) model.Metric {
	instance := cfg.APIServer
	if conn != nil {
		instance = conn.apiServer
	}
	l := map[string]string{
		"group":    cfg.Name,
		"instance": instance,
		"name":     cfg.Name,
	}
	for k, v := range extra {
		if v != "" {
			l[k] = v
		}
	}
	return model.Metric{Node: c.node, Name: name, Value: val, Labels: l, Timestamp: now}
}

// ---- 直连采集 ----

func (c *K8sCollector) collectNodes(ctx context.Context, cfg model.K8sInstanceConfig, conn *k8sConn, now int64) []model.Metric {
	var list k8sNodeList
	if err := c.getJSON(ctx, conn, "/api/v1/nodes", &list); err != nil {
		slog.Warn("K8s 获取节点列表失败", "name", cfg.Name, "err", err)
		return nil
	}
	var out []model.Metric
	total := len(list.Items)
	ready := 0
	for _, n := range list.Items {
		isReady := 0.0
		for _, cond := range n.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				isReady = 1
				ready++
				break
			}
		}
		role := nodeRole(n.Metadata.Labels)
		ip := ""
		for _, addr := range n.Status.Addresses {
			if addr.Type == "InternalIP" {
				ip = addr.Address
				break
			}
		}
		out = append(out, c.mk("k8s_node_ready", isReady, cfg, conn, map[string]string{
			"node_name":   n.Metadata.Name,
			"role":        role,
			"internal_ip": ip,
		}, now))
	}
	out = append(out, c.mk("k8s_nodes_total", float64(total), cfg, conn, nil, now))
	out = append(out, c.mk("k8s_nodes_ready", float64(ready), cfg, conn, nil, now))
	return out
}

func (c *K8sCollector) collectWorkloads(ctx context.Context, cfg model.K8sInstanceConfig, conn *k8sConn, now int64) []model.Metric {
	var out []model.Metric

	// Deployment
	var deps k8sWorkloadList
	if err := c.getJSON(ctx, conn, "/apis/apps/v1/deployments", &deps); err == nil {
		unhealthy := 0
		for _, d := range deps.Items {
			desired := float64(d.Spec.Replicas)
			readyR := float64(d.Status.ReadyReplicas)
			if readyR < desired {
				unhealthy++
			}
			out = append(out,
				c.mk("k8s_deployment_replicas_desired", desired, cfg, conn, map[string]string{"namespace": d.Metadata.Namespace, "workload": d.Metadata.Name}, now),
				c.mk("k8s_deployment_replicas_ready", readyR, cfg, conn, map[string]string{"namespace": d.Metadata.Namespace, "workload": d.Metadata.Name}, now),
			)
		}
		out = append(out, c.mk("k8s_deployments_total", float64(len(deps.Items)), cfg, conn, nil, now))
		out = append(out, c.mk("k8s_deployments_unhealthy", float64(unhealthy), cfg, conn, nil, now))
	}

	// StatefulSet
	var sts k8sWorkloadList
	if err := c.getJSON(ctx, conn, "/apis/apps/v1/statefulsets", &sts); err == nil {
		unhealthy := 0
		for _, s := range sts.Items {
			desired := float64(s.Spec.Replicas)
			readyR := float64(s.Status.ReadyReplicas)
			if readyR < desired {
				unhealthy++
			}
			out = append(out,
				c.mk("k8s_statefulset_replicas_desired", desired, cfg, conn, map[string]string{"namespace": s.Metadata.Namespace, "workload": s.Metadata.Name}, now),
				c.mk("k8s_statefulset_replicas_ready", readyR, cfg, conn, map[string]string{"namespace": s.Metadata.Namespace, "workload": s.Metadata.Name}, now),
			)
		}
		out = append(out, c.mk("k8s_statefulsets_total", float64(len(sts.Items)), cfg, conn, nil, now))
		out = append(out, c.mk("k8s_statefulsets_unhealthy", float64(unhealthy), cfg, conn, nil, now))
	}

	// DaemonSet
	var ds k8sDaemonSetList
	if err := c.getJSON(ctx, conn, "/apis/apps/v1/daemonsets", &ds); err == nil {
		unhealthy := 0
		for _, d := range ds.Items {
			desired := float64(d.Status.DesiredNumberScheduled)
			readyR := float64(d.Status.NumberReady)
			if readyR < desired {
				unhealthy++
			}
			out = append(out,
				c.mk("k8s_daemonset_desired", desired, cfg, conn, map[string]string{"namespace": d.Metadata.Namespace, "workload": d.Metadata.Name}, now),
				c.mk("k8s_daemonset_ready", readyR, cfg, conn, map[string]string{"namespace": d.Metadata.Namespace, "workload": d.Metadata.Name}, now),
			)
		}
		out = append(out, c.mk("k8s_daemonsets_total", float64(len(ds.Items)), cfg, conn, nil, now))
		out = append(out, c.mk("k8s_daemonsets_unhealthy", float64(unhealthy), cfg, conn, nil, now))
	}

	return out
}

func (c *K8sCollector) collectPods(ctx context.Context, cfg model.K8sInstanceConfig, conn *k8sConn, now int64) []model.Metric {
	var list k8sPodList
	if err := c.getJSON(ctx, conn, "/api/v1/pods", &list); err != nil {
		slog.Warn("K8s 获取 Pod 列表失败", "name", cfg.Name, "err", err)
		return nil
	}
	var out []model.Metric
	total := len(list.Items)
	// running 按**有效状态**计数，其余三个按 phase 计数——两者刻意不同，见下方注释。
	running, pending, failed, succeeded, abnormal := 0, 0, 0, 0, 0
	for _, p := range list.Items {
		status := podStatus(p.Status.Phase, p.Status.ContainerStatuses)

		// "运行中"用有效状态判定，不用 phase：phase 是 Pod 的生命周期阶段，
		// 容器在 CrashLoopBackOff / RunContainerError 时 kubelet 仍把 phase 留在 Running，
		// 按 phase 数出来的"运行 Pod"会把崩溃中的 Pod 一起算进去——监控报假健康比不报更糟。
		if status == "Running" {
			running++
		}
		// 这三个保留 phase 口径：它们表达的是生命周期阶段分布，
		// 与"是否健康"是两件事（Pending 可能是排队，也可能是镜像拉不动）。
		switch p.Status.Phase {
		case "Pending":
			pending++
		case "Failed":
			failed++
		case "Succeeded":
			succeeded++
		}
		if !podHealthy(status) {
			abnormal++
			// detail 系列沿用 k8s_pod_phase 这个名字（服务端按名取），
			// 但把有效状态放进 status 标签，phase 一并保留：老服务端读 phase 依然可用。
			out = append(out, c.mk("k8s_pod_phase", 1, cfg, conn, map[string]string{
				"namespace": p.Metadata.Namespace,
				"pod":       p.Metadata.Name,
				"phase":     p.Status.Phase,
				"status":    status,
			}, now))
		}
	}
	out = append(out,
		c.mk("k8s_pods_total", float64(total), cfg, conn, nil, now),
		c.mk("k8s_pods_running", float64(running), cfg, conn, nil, now),
		c.mk("k8s_pods_pending", float64(pending), cfg, conn, nil, now),
		c.mk("k8s_pods_failed", float64(failed), cfg, conn, nil, now),
		c.mk("k8s_pods_succeeded", float64(succeeded), cfg, conn, nil, now),
		c.mk("k8s_pods_abnormal", float64(abnormal), cfg, conn, nil, now),
	)
	return out
}

// podStatus 推断 Pod 的**有效状态**，语义对齐 `kubectl get pods` 的 STATUS 列：
// 容器等待原因优先（ImagePullBackOff / CrashLoopBackOff / RunContainerError ...），
// 其次是容器终止原因（Error / OOMKilled / Completed），最后才回退到 phase。
//
// 为什么必须这样：phase 只表达 Pod 的生命周期阶段，**不反映容器起不起得来**。
// 镜像拉不动时 phase 是 Pending、容器崩溃重启时 phase 是 Running，
// 只报 phase 会让崩溃中的 Pod 在界面上显示成"运行中"。
func podStatus(phase string, cs []k8sContainerStatus) string {
	for _, c := range cs {
		if r := strings.TrimSpace(c.State.Waiting.Reason); r != "" {
			return r
		}
	}
	for _, c := range cs {
		t := c.State.Terminated
		if t == nil {
			continue
		}
		if r := strings.TrimSpace(t.Reason); r != "" {
			return r
		}
		if t.ExitCode != 0 {
			return "Error"
		}
		return "Completed"
	}
	return phase
}

// podHealthy 判断有效状态是否属于"正常"。Completed 是正常结束（Job 跑完），
// 与 Succeeded 等价，不能算异常。
func podHealthy(status string) bool {
	switch status {
	case "Running", "Succeeded", "Completed":
		return true
	}
	return false
}

func (c *K8sCollector) collectNodeMetrics(ctx context.Context, cfg model.K8sInstanceConfig, conn *k8sConn, now int64) []model.Metric {
	var list k8sNodeMetricsList
	if err := c.getJSON(ctx, conn, "/apis/metrics.k8s.io/v1beta1/nodes", &list); err != nil {
		slog.Warn("K8s metrics-server 查询失败", "name", cfg.Name, "err", err)
		return nil
	}
	var out []model.Metric
	for _, n := range list.Items {
		cpuCores := parseK8sCPU(n.Usage.CPU)
		memBytes := parseK8sMem(n.Usage.Memory)
		out = append(out,
			c.mk("k8s_node_cpu_usage_cores", cpuCores, cfg, conn, map[string]string{"node_name": n.Metadata.Name}, now),
			c.mk("k8s_node_mem_usage_bytes", memBytes, cfg, conn, map[string]string{"node_name": n.Metadata.Name}, now),
		)
	}
	return out
}

// getVersion 请求 /version 返回 gitVersion。
func (c *K8sCollector) getVersion(ctx context.Context, conn *k8sConn) (string, error) {
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := c.getJSON(ctx, conn, "/version", &v); err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// getJSON 向 apiserver 发起 GET 请求并解码 JSON。
func (c *K8sCollector) getJSON(ctx context.Context, conn *k8sConn, path string, out interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, conn.apiServer+path, nil)
	if err != nil {
		return err
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := conn.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("apiserver 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- exporter 模式 ----

// collectExporter 抓取 kube-state-metrics /metrics 文本并映射到统一指标。
// 返回 (metrics, up, version)。KSM 不暴露版本，version 恒为空。
func (c *K8sCollector) collectExporter(ctx context.Context, cfg model.K8sInstanceConfig, conn *k8sConn, now int64) ([]model.Metric, bool, string) {
	client := &http.Client{Timeout: 10 * time.Second}
	body, err := fetchMetrics(ctx, client, cfg.ExporterURL)
	if err != nil {
		slog.Warn("K8s 抓取 kube-state-metrics 失败", "name", cfg.Name, "url", cfg.ExporterURL, "err", err)
		return []model.Metric{c.mk("k8s_cluster_up", 0, cfg, conn, nil, now)}, false, ""
	}
	text := string(body)

	// KSM 指标名 → 本项目统一指标名的计数聚合
	nodesTotal := countKSMSeries(text, "kube_node_info")
	nodesReady := sumKSMValue(text, "kube_node_status_condition", `condition="Ready"`, `status="true"`)
	podsTotal := countKSMSeries(text, "kube_pod_info")
	podsRunning := sumKSMValue(text, "kube_pod_status_phase", `phase="Running"`)
	podsPending := sumKSMValue(text, "kube_pod_status_phase", `phase="Pending"`)
	podsFailed := sumKSMValue(text, "kube_pod_status_phase", `phase="Failed"`)
	depsTotal := countKSMSeries(text, "kube_deployment_created")

	out := []model.Metric{
		c.mk("k8s_cluster_up", 1, cfg, conn, nil, now),
		c.mk("k8s_nodes_total", nodesTotal, cfg, conn, nil, now),
		c.mk("k8s_nodes_ready", nodesReady, cfg, conn, nil, now),
		c.mk("k8s_pods_total", podsTotal, cfg, conn, nil, now),
		c.mk("k8s_pods_running", podsRunning, cfg, conn, nil, now),
		c.mk("k8s_pods_pending", podsPending, cfg, conn, nil, now),
		c.mk("k8s_pods_failed", podsFailed, cfg, conn, nil, now),
		c.mk("k8s_deployments_total", depsTotal, cfg, conn, nil, now),
	}
	return out, true, ""
}

// countKSMSeries 统计包含指定指标名的样本行数。
func countKSMSeries(text, metric string) float64 {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, metric+"{") || strings.HasPrefix(line, metric+" ") {
			n++
		}
	}
	return float64(n)
}

// sumKSMValue 对含指定指标名且标签包含所有 filters 子串的样本行求值之和。
func sumKSMValue(text, metric string, filters ...string) float64 {
	sum := 0.0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		ok := true
		for _, f := range filters {
			if !strings.Contains(line, f) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			sum += v
		}
	}
	return sum
}

// ---- 连接构建 ----

// k8sConn 是一次采集用的 apiserver 连接上下文。
type k8sConn struct {
	apiServer string
	token     string
	client    *http.Client
}

// buildK8sConn 根据配置构建连接：优先使用显式 APIServer+Token，否则解析 kubeconfig。
func buildK8sConn(cfg model.K8sInstanceConfig) (*k8sConn, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureTLS}

	// 显式指定 apiServer：使用 token 认证
	if cfg.APIServer != "" && cfg.Kubeconfig == "" {
		return &k8sConn{
			apiServer: strings.TrimRight(cfg.APIServer, "/"),
			token:     cfg.Token,
			client:    &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
		}, nil
	}

	// 解析 kubeconfig
	if cfg.Kubeconfig == "" {
		return nil, fmt.Errorf("apiServer 与 kubeconfig 均为空")
	}
	kc, err := parseKubeconfig(cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}

	apiServer := cfg.APIServer
	if apiServer == "" {
		apiServer = kc.server
	}
	if apiServer == "" {
		return nil, fmt.Errorf("kubeconfig 未包含 server 地址")
	}

	// CA 证书
	if !cfg.InsecureTLS && len(kc.caData) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(kc.caData) {
			tlsCfg.RootCAs = pool
		}
	}
	// 客户端证书认证
	if len(kc.clientCertData) > 0 && len(kc.clientKeyData) > 0 {
		cert, err := tls.X509KeyPair(kc.clientCertData, kc.clientKeyData)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书失败: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	token := cfg.Token
	if token == "" {
		token = kc.token
	}

	return &k8sConn{
		apiServer: strings.TrimRight(apiServer, "/"),
		token:     token,
		client:    &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
	}, nil
}

// kubeconfigData 是从 kubeconfig 解析出的当前上下文连接信息。
type kubeconfigData struct {
	server         string
	caData         []byte
	token          string
	clientCertData []byte
	clientKeyData  []byte
}

// parseKubeconfig 解析 kubeconfig 文件，取 current-context 对应的 cluster 与 user。
func parseKubeconfig(path string) (*kubeconfigData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 kubeconfig 失败: %w", err)
	}
	var kc kubeconfigFile
	if err := yaml.Unmarshal(raw, &kc); err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}

	// 定位 current-context
	var ctxCluster, ctxUser string
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext {
			ctxCluster = c.Context.Cluster
			ctxUser = c.Context.User
			break
		}
	}
	if ctxCluster == "" && len(kc.Contexts) > 0 {
		ctxCluster = kc.Contexts[0].Context.Cluster
		ctxUser = kc.Contexts[0].Context.User
	}

	out := &kubeconfigData{}
	for _, cl := range kc.Clusters {
		if cl.Name == ctxCluster {
			out.server = cl.Cluster.Server
			if cl.Cluster.CAData != "" {
				out.caData, _ = base64.StdEncoding.DecodeString(cl.Cluster.CAData)
			} else if cl.Cluster.CA != "" {
				out.caData, _ = os.ReadFile(cl.Cluster.CA)
			}
			break
		}
	}
	for _, u := range kc.Users {
		if u.Name == ctxUser {
			out.token = u.User.Token
			if u.User.ClientCertData != "" {
				out.clientCertData, _ = base64.StdEncoding.DecodeString(u.User.ClientCertData)
			} else if u.User.ClientCert != "" {
				out.clientCertData, _ = os.ReadFile(u.User.ClientCert)
			}
			if u.User.ClientKeyData != "" {
				out.clientKeyData, _ = base64.StdEncoding.DecodeString(u.User.ClientKeyData)
			} else if u.User.ClientKey != "" {
				out.clientKeyData, _ = os.ReadFile(u.User.ClientKey)
			}
			break
		}
	}
	return out, nil
}

// ---- kubeconfig / apiserver JSON 结构 ----

type kubeconfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
			CAData string `yaml:"certificate-authority-data"`
			CA     string `yaml:"certificate-authority"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token          string `yaml:"token"`
			ClientCertData string `yaml:"client-certificate-data"`
			ClientCert     string `yaml:"client-certificate"`
			ClientKeyData  string `yaml:"client-key-data"`
			ClientKey      string `yaml:"client-key"`
		} `yaml:"user"`
	} `yaml:"users"`
}

type k8sObjectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type k8sNodeList struct {
	Items []struct {
		Metadata k8sObjectMeta `json:"metadata"`
		Status   struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
		} `json:"status"`
	} `json:"items"`
}

type k8sWorkloadList struct {
	Items []struct {
		Metadata k8sObjectMeta `json:"metadata"`
		Spec     struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	} `json:"items"`
}

type k8sDaemonSetList struct {
	Items []struct {
		Metadata k8sObjectMeta `json:"metadata"`
		Status   struct {
			DesiredNumberScheduled int `json:"desiredNumberScheduled"`
			NumberReady            int `json:"numberReady"`
		} `json:"status"`
	} `json:"items"`
}

// k8sContainerStatus 是容器级状态。**状态的真相在这里，不在 pod 的 phase 里**：
// 镜像拉不动时 phase 仍是 Pending，容器反复崩溃重启时 phase 仍是 Running。
//
// 这个类型被 K8s 采集（本文件）与容器只读查询（k8s_query.go）**共用**：
// 两个接口返回的 containerStatuses 形状一致，共用一个类型才能让"有效状态"的判定
// 只有 podStatus 一处实现——否则两个页面迟早给出互相矛盾的结论。
type k8sContainerStatus struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
	State        struct {
		Waiting    k8sContainerWaiting     `json:"waiting"`
		Terminated *k8sContainerTerminated `json:"terminated"`
	} `json:"state"`
}

// k8sContainerWaiting 是容器等待状态。reason 是镜像拉不动/崩溃退避这类
// "容器根本没起来"的答案（ImagePullBackOff / CrashLoopBackOff / RunContainerError）。
type k8sContainerWaiting struct {
	Reason string `json:"reason"`
}

// k8sContainerTerminated 是容器终止状态。
type k8sContainerTerminated struct {
	Reason   string `json:"reason"`
	ExitCode int    `json:"exitCode"`
}

type k8sPodList struct {
	Items []struct {
		Metadata k8sObjectMeta `json:"metadata"`
		Status   struct {
			Phase             string               `json:"phase"`
			ContainerStatuses []k8sContainerStatus `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type k8sNodeMetricsList struct {
	Items []struct {
		Metadata k8sObjectMeta `json:"metadata"`
		Usage    struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"usage"`
	} `json:"items"`
}

// ---- 辅助解析 ----

// nodeRole 从 node label 推断角色；多角色按字典序拼接（如 control-plane,master）。
//
// 两处都是踩过的坑：
//  1. **必须排序**。labels 是 map，遍历顺序随机；此前"取第一个命中的"会让多角色节点
//     （标准 K8s 控制面同时带 control-plane 与 master）的角色在采集周期之间随机跳变，
//     界面上看起来就是角色在闪。
//  2. **全部返回而不是只取一个**。与 `kubectl get nodes` 的 ROLES 列口径一致，
//     否则多角色节点的角色"显示不全"。
func nodeRole(labels map[string]string) string {
	var roles []string
	for k := range labels {
		if strings.HasPrefix(k, "node-role.kubernetes.io/") {
			if role := strings.TrimPrefix(k, "node-role.kubernetes.io/"); role != "" {
				roles = append(roles, role)
			}
		}
	}
	if len(roles) == 0 {
		return "worker"
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

// parseK8sCPU 解析 metrics-server 的 CPU 用量（如 "123456789n" 纳核）为核数。
func parseK8sCPU(s string) float64 {
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "n") { // 纳核
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "n"), 64)
		return round2(v / 1e9)
	}
	if strings.HasSuffix(s, "u") { // 微核
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "u"), 64)
		return round2(v / 1e6)
	}
	if strings.HasSuffix(s, "m") { // 毫核
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		return round2(v / 1e3)
	}
	v, _ := strconv.ParseFloat(s, 64)
	return round2(v)
}

// parseK8sMem 解析 metrics-server 的内存用量（如 "1024Ki"/"512Mi"）为字节数。
func parseK8sMem(s string) float64 {
	if s == "" {
		return 0
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"Ki", 1024}, {"Mi", 1024 * 1024}, {"Gi", 1024 * 1024 * 1024}, {"Ti", 1024 * 1024 * 1024 * 1024},
		{"K", 1000}, {"M", 1000 * 1000}, {"G", 1000 * 1000 * 1000},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			return v * u.mult
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
