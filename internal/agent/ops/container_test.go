package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// 容器只读查询的四道护栏里，Agent 侧占两道（本机开关、参数复校），外加"实现是否就位"。
// 这些用例钉死三件事：不声明就不执行、护栏关掉就真的关掉、参数不合法时**根本不去碰 apiserver**。

type fakeQuerier struct {
	calls  int
	last   string
	result *model.ContainerQueryResult
	err    error
}

func (f *fakeQuerier) record(what string) (*model.ContainerQueryResult, error) {
	f.calls++
	f.last = what
	return f.result, f.err
}

func (f *fakeQuerier) QueryWorkloads(_ context.Context, cluster, ns string) (*model.ContainerQueryResult, error) {
	return f.record("workloads|" + cluster + "|" + ns)
}

func (f *fakeQuerier) QueryPods(_ context.Context, cluster, ns string) (*model.ContainerQueryResult, error) {
	return f.record("pods|" + cluster + "|" + ns)
}

func (f *fakeQuerier) QueryEvents(_ context.Context, cluster, ns, name string) (*model.ContainerQueryResult, error) {
	return f.record("events|" + cluster + "|" + ns + "|" + name)
}

func (f *fakeQuerier) QueryObject(_ context.Context, cluster, ns, resource, name string) (*model.ContainerQueryResult, error) {
	return f.record("object|" + cluster + "|" + ns + "|" + resource + "|" + name)
}

func (f *fakeQuerier) QueryLogs(_ context.Context, cluster, ns, pod, container string, tailLines, sinceSeconds int) (*model.ContainerQueryResult, error) {
	return f.record("logs|" + cluster + "|" + ns + "|" + pod + "|" + container +
		"|" + strconv.Itoa(tailLines) + "|" + strconv.Itoa(sinceSeconds))
}

func sampleResult(kind string) *model.ContainerQueryResult {
	return &model.ContainerQueryResult{
		Kind: kind, Cluster: "prod-k8s", Namespace: "default",
		Columns: []string{"名称", "状态"},
		Rows:    [][]string{{"web-1", "Running"}},
		Total:   1,
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// 没有注入查询实现（本机没配集群）时，容器动作既不该被声明，也不该被执行。
func TestContainer_NotDeclaredWithoutQuerier(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	if contains(e.Supported(), model.OpsKindContainerPods) {
		t.Fatalf("未注入查询实现时不应声明容器动作，实际 %v", e.Supported())
	}
	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerPods, Params: map[string]string{"cluster": "c"}})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "未启用容器查询") {
		t.Fatalf("未注入实现时应明确失败，实际 %+v", res)
	}
}

// guards.ops.container=false 是"显式关闭必须真生效"：既不声明，也拒绝执行。
func TestContainer_GuardOffMeansOff(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{Container: boolPtr(false)})
	e.SetK8sQuerier(&fakeQuerier{result: sampleResult(model.OpsKindContainerPods)})

	if contains(e.Supported(), model.OpsKindContainerPods) {
		t.Fatalf("container=false 时不应声明容器动作，实际 %v", e.Supported())
	}
	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerPods, Params: map[string]string{"cluster": "c"}})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "container") {
		t.Fatalf("container=false 时应被本机护栏拒绝，实际 %+v", res)
	}
}

// 只读总开关关掉时，容器查询同样被拒——它是只读动作的一个子集，不能绕过总开关。
func TestContainer_ReadOnlySwitchAlsoCoversIt(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{ReadOnly: boolPtr(false)})
	e.SetK8sQuerier(&fakeQuerier{result: sampleResult(model.OpsKindContainerWorkloads)})
	if contains(e.Supported(), model.OpsKindContainerWorkloads) {
		t.Fatalf("readOnly=false 时不应声明容器动作，实际 %v", e.Supported())
	}
	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerWorkloads, Params: map[string]string{"cluster": "c"}})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "readOnly") {
		t.Fatalf("readOnly=false 时应被拒绝，实际 %+v", res)
	}
}

// 参数本地复校：不合法时**一次都不能调用查询实现**（否则等于拿拼出来的路径去请求 apiserver）。
func TestContainer_LocalParamValidationRunsBeforeQuery(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		params map[string]string
		wantIn string
	}{
		{"集群标识含空格", model.OpsKindContainerPods, map[string]string{"cluster": "prod k8s"}, "集群标识不合法"},
		{"集群标识为选项", model.OpsKindContainerWorkloads, map[string]string{"cluster": "--server"}, "集群标识不合法"},
		{"命名空间路径穿越", model.OpsKindContainerPods, map[string]string{"cluster": "c", "namespace": "../../etc"}, "命名空间不合法"},
		{"命名空间带查询串", model.OpsKindContainerPods, map[string]string{"cluster": "c", "namespace": "a?watch=1"}, "命名空间不合法"},
		{"资源类型不在白名单", model.OpsKindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "secrets", "name": "db"}, "资源类型不合法"},
		{"对象名路径穿越", model.OpsKindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "pods", "name": "../secrets"}, "对象名不合法"},
		// 日志：命名空间必填（Pod 名只在命名空间内唯一）、行数与时间窗有硬上限。
		{"日志缺命名空间", model.OpsKindContainerLogs, map[string]string{"cluster": "c", "name": "web-1"}, "必须指定命名空间"},
		{"日志缺 Pod 名", model.OpsKindContainerLogs, map[string]string{"cluster": "c", "namespace": "n"}, "Pod 名不合法"},
		{"日志 Pod 名路径穿越", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "../secrets"}, "Pod 名不合法"},
		{"日志容器名不合法", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "container": "a b"}, "容器名不合法"},
		{"日志行数超上限", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "tailLines": "9999"}, "行数不合法"},
		{"日志行数非数字", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "tailLines": "all"}, "行数不合法"},
		{"日志行数为零", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "tailLines": "0"}, "行数不合法"},
		{"日志时间窗超上限", model.OpsKindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "sinceSeconds": "999999"}, "时间窗不合法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeQuerier{result: sampleResult(tc.kind)}
			e := newTestExecutor(config.OpsGuards{})
			e.SetK8sQuerier(f)

			res := e.Execute(model.OpsCommand{ID: "t1", Kind: tc.kind, Params: tc.params})
			if res.State != model.OpsStateFailed || !strings.Contains(res.Message, tc.wantIn) {
				t.Fatalf("应本地拒绝，实际 %+v", res)
			}
			if f.calls != 0 {
				t.Fatalf("参数不合法时不应调用查询实现，实际调用 %d 次", f.calls)
			}
		})
	}
}

// 成功路径：Data 给一行摘要、JSON 给结构化载荷，且 JSON 能被反序列化回协议类型。
func TestContainer_SuccessCarriesStructuredPayload(t *testing.T) {
	f := &fakeQuerier{result: sampleResult(model.OpsKindContainerPods)}
	f.result.Truncated = true
	f.result.Total = 812

	e := newTestExecutor(config.OpsGuards{})
	e.SetK8sQuerier(f)

	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerPods,
		Params: map[string]string{"cluster": "prod-k8s", "namespace": "default"}})

	if res.State != model.OpsStateSucceeded {
		t.Fatalf("应成功，实际 %+v", res)
	}
	if f.last != "pods|prod-k8s|default" {
		t.Fatalf("应把参数原样传给查询实现，实际 %q", f.last)
	}
	if !strings.Contains(res.Message, "已截断") {
		t.Fatalf("截断必须说出来，实际 %q", res.Message)
	}
	if res.Data["概览"] == "" {
		t.Fatalf("Data 应给一行摘要，实际 %+v", res.Data)
	}
	var got model.ContainerQueryResult
	if err := json.Unmarshal([]byte(res.JSON), &got); err != nil {
		t.Fatalf("JSON 载荷应可反序列化: %v / %s", err, res.JSON)
	}
	if got.Total != 812 || !got.Truncated || len(got.Rows) != 1 || got.Columns[0] != "名称" {
		t.Fatalf("结构化载荷内容不符，实际 %+v", got)
	}
}

// Pod 日志：默认行数必须落到模型常量上，且显式参数原样传到查询实现。
// 这是"静默夹值"的反面用例——超上限一律拒绝，不偷偷改小。
func TestContainer_LogsAppliesDefaultsAndPassesArgs(t *testing.T) {
	f := &fakeQuerier{result: sampleResult(model.OpsKindContainerLogs)}
	e := newTestExecutor(config.OpsGuards{})
	e.SetK8sQuerier(f)
	if !contains(e.Supported(), model.OpsKindContainerLogs) {
		t.Fatalf("容器动作应包含日志查询，实际 %v", e.Supported())
	}

	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerLogs,
		Params: map[string]string{"cluster": "prod-k8s", "namespace": "default", "name": "web-1"}})
	if res.State != model.OpsStateSucceeded {
		t.Fatalf("应成功，实际 %+v", res)
	}
	want := "logs|prod-k8s|default|web-1||" + strconv.Itoa(model.OpsLogDefaultTailLines) + "|0"
	if f.last != want {
		t.Fatalf("默认参数不符：got %q want %q", f.last, want)
	}

	res = e.Execute(model.OpsCommand{ID: "t2", Kind: model.OpsKindContainerLogs,
		Params: map[string]string{"cluster": "c", "namespace": "n", "name": "web-1", "container": "app",
			"tailLines": strconv.Itoa(model.OpsLogMaxTailLines), "sinceSeconds": strconv.Itoa(model.OpsLogMaxSinceSeconds)}})
	if res.State != model.OpsStateSucceeded {
		t.Fatalf("边界值应被接受，实际 %+v", res)
	}
	want = "logs|c|n|web-1|app|" + strconv.Itoa(model.OpsLogMaxTailLines) + "|" + strconv.Itoa(model.OpsLogMaxSinceSeconds)
	if f.last != want {
		t.Fatalf("显式参数不符：got %q want %q", f.last, want)
	}
}

// 查询失败要以 failed 回执回到中心，而不是把错误吞掉（吞掉的任务会永远停在 running）。
func TestContainer_QueryErrorBecomesFailedResult(t *testing.T) {
	f := &fakeQuerier{err: errors.New("apiserver 返回 403")}
	e := newTestExecutor(config.OpsGuards{})
	e.SetK8sQuerier(f)

	res := e.Execute(model.OpsCommand{ID: "t1", Kind: model.OpsKindContainerPods, Params: map[string]string{"cluster": "c"}})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "403") {
		t.Fatalf("查询失败应回 failed 并带上原因，实际 %+v", res)
	}
}

// 注入实现后能力清单要包含四个容器动作（全部只读）。
func TestContainer_SupportedListsAllFourKinds(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	e.SetK8sQuerier(&fakeQuerier{result: sampleResult(model.OpsKindContainerPods)})
	got := e.Supported()
	for _, k := range containerKinds {
		if !contains(got, k) {
			t.Fatalf("能力清单应含 %s，实际 %v", k, got)
		}
	}
}
