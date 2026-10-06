package asset

import "testing"

// 自然键的拼与解必须互为逆运算。集群是 apiserver 地址（含 `://` 与可能的端口），
// 键里因此带斜杠——从右往左取三段是唯一不会解错的读法。
func TestParseContainerKeyRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		typeKey  string
		cluster  string
		ns       string
		kind     string
		objName  string
	}{
		{"带协议的集群地址", TypePod, "https://10.0.0.9:6443", "default", "pod", "web-7d9f-abc"},
		{"无协议无端口的集群", TypePod, "10.0.0.9", "kube-system", "pod", "coredns-xyz"},
		{"带路径的集群地址", TypePod, "https://api.k8s.example.com/k8s", "nebula-demo", "pod", "web-abc"},
		{"工作负载", TypeWorkload, "https://10.0.0.9:6443", "default", "deployment", "web"},
		{"名字带点的 StatefulSet", TypeWorkload, "https://10.0.0.9:6443", "db", "statefulset", "mysql-0.db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var key string
			if tc.typeKey == TypePod {
				key = PodNaturalKey(tc.cluster, tc.ns, tc.objName)
			} else {
				key = WorkloadNaturalKey(tc.cluster, tc.ns, tc.kind, tc.objName)
			}
			got, ok := ParseContainerKey(tc.typeKey, key)
			if !ok {
				t.Fatalf("应能解出身份：%s", key)
			}
			if got.Cluster != tc.cluster || got.Namespace != tc.ns || got.Kind != tc.kind || got.Name != tc.objName {
				t.Fatalf("解出的身份不符：got %+v want cluster=%q ns=%q kind=%q name=%q",
					got, tc.cluster, tc.ns, tc.kind, tc.objName)
			}
		})
	}
}

// 非容器类型与残缺键一律返回 false：宁可让联动方说"没有这个入口"，
// 也不要解出一个看似合理、实际指向别的对象的身份。
func TestParseContainerKeyRejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		typeKey string
		key     string
	}{
		{"主机类型", TypeHost, "web-01"},
		{"中间件实例类型", TypeMiddlewareInst, "redis:10.0.0.1:6379"},
		{"段数不足", TypePod, "default/pod/web-1"},
		{"空串", TypePod, ""},
		{"缺名字", TypePod, "https://10.0.0.9:6443/default/pod/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := ParseContainerKey(tc.typeKey, tc.key); ok {
				t.Fatalf("不应解出身份：type=%s key=%q", tc.typeKey, tc.key)
			}
		})
	}
}
