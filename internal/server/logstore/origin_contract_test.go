package logstore

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 容器过滤（「只看某个 Pod」）：两个后端语义一致——**没有身份的行不命中任何容器条件**
// （它不属于任何 Pod），且同名 Pod 在别的命名空间里不算命中。
func TestBackendContract_PodFilter(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			now := time.Now().UnixMilli()
			seed := func(origin *model.LogOrigin, ts int64, text string) {
				t.Helper()
				if _, _, _, err := store.Append(model.LogBatch{
					Source: "podlog", Node: "web-01", Origin: origin,
					Lines: []model.LogLine{{Ts: ts, Text: text}},
				}); err != nil {
					t.Fatalf("写入失败: %v", err)
				}
			}
			seed(&model.LogOrigin{Namespace: "nebula-demo", Pod: "web-1", Container: "nginx"}, now, "pod web-1")
			seed(&model.LogOrigin{Namespace: "nebula-demo", Pod: "web-2", Container: "nginx"}, now-1000, "pod web-2")
			// 同名但别的命名空间：容器过滤必须带上命名空间，否则会串台
			seed(&model.LogOrigin{Namespace: "kube-system", Pod: "web-1", Container: "coredns"}, now-2000, "same name other ns")
			// 普通文件日志（没有身份）
			seedLines(t, store, "applog", "web-01", model.LogLine{Ts: now - 3000, Text: "plain line"})

			single := mustQuery(t, store, model.LogQuery{
				From: now - 10_000, To: now + 10_000,
				Pods: []model.LogPodFilter{{Namespace: "nebula-demo", Pod: "web-1"}},
			})
			if len(single.Lines) != 1 || single.Lines[0].Text != "pod web-1" {
				t.Fatalf("单个容器过滤应只命中它（同名不同命名空间不算）：%+v", single.Lines)
			}

			// 多个容器之间是「或」
			multi := mustQuery(t, store, model.LogQuery{
				From: now - 10_000, To: now + 10_000,
				Pods: []model.LogPodFilter{
					{Namespace: "nebula-demo", Pod: "web-1"},
					{Namespace: "nebula-demo", Pod: "web-2"},
				},
			})
			if len(multi.Lines) != 2 {
				t.Fatalf("多个容器应「或」命中 2 条：%+v", multi.Lines)
			}

			// 没有身份的行不命中任何容器条件
			for _, hit := range multi.Lines {
				if hit.Text == "plain line" {
					t.Fatalf("没有身份的行不该被容器过滤命中：%+v", multi.Lines)
				}
			}

			// 不存在的容器：空结果（而不是"退化成不限"）
			none := mustQuery(t, store, model.LogQuery{
				From: now - 10_000, To: now + 10_000,
				Pods: []model.LogPodFilter{{Namespace: "nebula-demo", Pod: "no-such-pod"}},
			})
			if len(none.Lines) != 0 {
				t.Fatalf("不存在的容器应无命中：%+v", none.Lines)
			}
		})
	}
}

// 容器身份（Pod 日志）在两个后端上必须表现一致：落得进去、读得回来、非法即拒。
//
// 为什么也要跑契约（而不是只测一个后端）：身份决定这些行被标到哪个 Pod 资产上，
// 换后端后"身份丢了"或"身份被正文覆盖"都不会报错，只会让日志页上的资产标签
// 悄悄消失或指错——正是契约用例要挡住的那类问题。
func TestBackendContract_LogOrigin(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			now := time.Now().UnixMilli()
			origin := &model.LogOrigin{Namespace: "nebula-demo", Pod: "web-1", Container: "nginx"}

			if _, dropped, reason, err := store.Append(model.LogBatch{
				Source: "podlog", Node: "web-01", Origin: origin,
				Lines: []model.LogLine{{Ts: now, Text: "error: boom"}},
			}); err != nil || dropped != 0 {
				t.Fatalf("带容器身份的写入应被接受：dropped=%d reason=%s err=%v", dropped, reason, err)
			}
			// 正文里**伪造**身份：绝不能变成行上的身份（否则一条日志就能把自己标到别人的 Pod 上）
			seedLines(t, store, "applog", "web-01",
				model.LogLine{Ts: now - 1000, Text: `k8s_pod=evil k8s_namespace=other k8s_container=x`})

			res := mustQuery(t, store, model.LogQuery{From: now - 10_000, To: now + 10_000})
			if len(res.Lines) != 2 {
				t.Fatalf("应命中 2 条，实际 %d：%+v", len(res.Lines), res.Lines)
			}
			for _, hit := range res.Lines {
				switch hit.Source {
				case "podlog":
					if hit.Origin == nil || *hit.Origin != *origin {
						t.Fatalf("容器身份应随行返回：%+v", hit.Origin)
					}
				case "applog":
					if hit.Origin != nil {
						t.Fatalf("正文里的 k8s_pod 不得变成行上的身份：%+v", hit.Origin)
					}
				}
			}

			// 非法身份：两个后端都必须**拒绝**（而不是"丢掉身份照收"）——
			// 身份不可信比没有身份更危险，而合法 Agent 永远不会发出非法身份。
			if _, _, _, err := store.Append(model.LogBatch{
				Source: "podlog", Node: "web-01",
				Origin: &model.LogOrigin{Namespace: "nebula-demo", Pod: "web_1", Container: "nginx"},
				Lines:  []model.LogLine{{Ts: now, Text: "x"}},
			}); err == nil {
				t.Fatal("非法容器身份应被拒绝")
			}
			// 只给一半身份同样非法（定位不到资产可接受，**定位错**不可接受）
			if _, _, _, err := store.Append(model.LogBatch{
				Source: "podlog", Node: "web-01",
				Origin: &model.LogOrigin{Pod: "web-1", Container: "nginx"},
				Lines:  []model.LogLine{{Ts: now, Text: "x"}},
			}); err == nil {
				t.Fatal("缺 namespace 的身份应被拒绝")
			}
		})
	}
}
