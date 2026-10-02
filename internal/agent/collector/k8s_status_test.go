package collector

import (
	"testing"
)

func waitingCS(reason string) k8sContainerStatus {
	var c k8sContainerStatus
	c.State.Waiting.Reason = reason
	return c
}

func terminatedCS(reason string, code int) k8sContainerStatus {
	var c k8sContainerStatus
	c.State.Terminated = &k8sContainerTerminated{Reason: reason, ExitCode: code}
	return c
}

// TestPodStatus 覆盖"有效状态"的判定：容器级原因优先，phase 只在没有任何容器级
// 信息时兜底。前两条直接对应实机验证踩到的现象——只报 phase 时，
// ImagePullBackOff 显示成 Pending、RunContainerError 显示成 Running（看起来是健康的）。
func TestPodStatus(t *testing.T) {
	cases := []struct {
		name  string
		phase string
		cs    []k8sContainerStatus
		want  string
	}{
		{"镜像拉不动：phase 仍是 Pending，必须报出真实原因", "Pending",
			[]k8sContainerStatus{waitingCS("ImagePullBackOff")}, "ImagePullBackOff"},
		{"容器起不来：phase 是 Running，不能报成正常", "Running",
			[]k8sContainerStatus{waitingCS("RunContainerError")}, "RunContainerError"},
		{"崩溃退避", "Running",
			[]k8sContainerStatus{waitingCS("CrashLoopBackOff")}, "CrashLoopBackOff"},
		{"正常运行的 Pod 回退到 phase", "Running", nil, "Running"},
		{"终止原因 OOMKilled", "Failed",
			[]k8sContainerStatus{terminatedCS("OOMKilled", 137)}, "OOMKilled"},
		{"终止但没有 reason 且退出码非 0 → Error", "Failed",
			[]k8sContainerStatus{terminatedCS("", 1)}, "Error"},
		{"正常结束 → Completed", "Succeeded",
			[]k8sContainerStatus{terminatedCS("Completed", 0)}, "Completed"},
		{"Succeeded 且无容器状态 → 仍是 Succeeded", "Succeeded", nil, "Succeeded"},
		{"等待原因优先于终止原因", "Failed",
			[]k8sContainerStatus{terminatedCS("Error", 1), waitingCS("ImagePullBackOff")}, "ImagePullBackOff"},
		{"多容器：只要有一个在等就报它", "Running",
			[]k8sContainerStatus{{}, waitingCS("ImagePullBackOff")}, "ImagePullBackOff"},
		{"原因带空白要 trim 掉", "Pending",
			[]k8sContainerStatus{waitingCS("  ImagePullBackOff  ")}, "ImagePullBackOff"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := podStatus(c.phase, c.cs); got != c.want {
				t.Fatalf("podStatus(%q, ...) = %q，期望 %q", c.phase, got, c.want)
			}
		})
	}
}

// TestPodHealthy 是"异常 Pod"计数的判据。它必须把 Completed 算作正常，
// 否则跑完的 Job 会被一直报成异常。
func TestPodHealthy(t *testing.T) {
	healthy := []string{"Running", "Succeeded", "Completed"}
	abnormal := []string{"Pending", "ImagePullBackOff", "CrashLoopBackOff",
		"RunContainerError", "Error", "OOMKilled", "Unknown", ""}
	for _, s := range healthy {
		if !podHealthy(s) {
			t.Errorf("podHealthy(%q) = false，期望 true", s)
		}
	}
	for _, s := range abnormal {
		if podHealthy(s) {
			t.Errorf("podHealthy(%q) = true，期望 false", s)
		}
	}
}

// TestNodeRole 断言角色取值确定且完整。
// 关键在确定性：labels 是 map，遍历顺序随机；此前"取第一个命中的"会让多角色节点
// 的角色在采集周期之间随机跳变，界面上看起来像在闪。
func TestNodeRole(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"无角色标签 → worker", map[string]string{"kubernetes.io/hostname": "n1"}, "worker"},
		{"单角色", map[string]string{"node-role.kubernetes.io/control-plane": "true"}, "control-plane"},
		{"多角色按字典序拼接（与 kubectl 的 ROLES 列一致）",
			map[string]string{
				"node-role.kubernetes.io/master":        "true",
				"node-role.kubernetes.io/control-plane": "true",
			}, "control-plane,master"},
		{"空后缀的标签忽略", map[string]string{"node-role.kubernetes.io/": "true"}, "worker"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first := nodeRole(c.labels)
			if first != c.want {
				t.Fatalf("nodeRole = %q，期望 %q", first, c.want)
			}
			// 重复调用必须稳定——map 遍历顺序是随机的，取第一个就会在这里露馅。
			for i := 0; i < 100; i++ {
				if got := nodeRole(c.labels); got != first {
					t.Fatalf("第 %d 次调用得到 %q，与首次的 %q 不一致（角色在跳变）", i, got, first)
				}
			}
		})
	}
}
