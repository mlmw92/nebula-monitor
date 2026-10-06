package logstore

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

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
