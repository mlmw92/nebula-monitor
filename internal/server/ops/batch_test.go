package ops

import (
	"errors"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// 批量下发最常见的形态是**部分成功**（几台离线、几台没放行），
// 因此这里把"逐节点给结论"钉死：只说"失败"等于让人自己去比对是哪几台。
func TestCreateBatch_PartialSuccess(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store)
	store.SaveCaps("web-01", []string{KindNodeDiagnostics, KindSvcStatus})
	store.SaveCaps("db-01", []string{KindNodeDiagnostics, KindSvcStatus, KindSvcRestart})
	// web-02 刻意不声明任何能力

	res, err := svc.CreateBatch([]string{"web-01", "web-02", "db-01", "web-01"}, KindNodeDiagnostics, nil,
		"alice", "10.0.0.9", "批量排障")
	if err != nil {
		t.Fatalf("批量下发不应整批失败: %v", err)
	}
	if res.Total != 3 {
		t.Fatalf("重复节点应被去重：total 应为 3，实际 %d", res.Total)
	}
	if res.Created != 2 || res.Failed != 1 {
		t.Fatalf("应为 2 成功 1 失败，实际 created=%d failed=%d", res.Created, res.Failed)
	}
	if res.BatchID == "" || !strings.HasPrefix(res.BatchID, "ob-") {
		t.Fatalf("应生成批次号，实际 %q", res.BatchID)
	}
	if len(res.Items) != 3 || res.Items[0].Node != "web-01" || res.Items[1].Node != "web-02" || res.Items[2].Node != "db-01" {
		t.Fatalf("逐节点结果应保持输入顺序：%+v", res.Items)
	}
	if !res.Items[0].OK || res.Items[1].OK || !res.Items[2].OK {
		t.Fatalf("失败项应明确标出（web-02 未声明能力）：%+v", res.Items)
	}
	if !strings.Contains(res.Items[1].Error, "尚未声明") {
		t.Fatalf("失败原因应可操作（指向本机护栏/版本），实际 %q", res.Items[1].Error)
	}
	// 成功的两项必须真的落库、同批次、可查询
	for _, idx := range []int{0, 2} {
		task, ok := store.Get(res.Items[idx].TaskID)
		if !ok {
			t.Fatalf("任务 %s 未落库", res.Items[idx].TaskID)
		}
		if task.BatchID != res.BatchID || task.State != model.OpsStateQueued {
			t.Fatalf("任务应带同一批次号且为 queued：%+v", task)
		}
		if task.Operator != "alice" || task.Reason != "批量排障" {
			t.Fatalf("操作者与原因应逐条落库：%+v", task)
		}
	}
	// 按批次过滤应查到 2 条
	if got := store.List(ListFilter{BatchID: res.BatchID}); len(got) != 2 {
		t.Fatalf("按批次应查到 2 条，实际 %d", len(got))
	}
}

// 写动作的批量下发：只有放行了该动作的节点成功，其余按"未放行写操作"记账。
func TestCreateBatch_WriteActionGatingPerNode(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store)
	store.SaveCaps("web-01", []string{KindNodeDiagnostics, KindSvcStatus})
	store.SaveCaps("db-01", []string{KindNodeDiagnostics, KindSvcStatus, KindSvcRestart})

	res, err := svc.CreateBatch([]string{"web-01", "db-01"}, KindSvcRestart, map[string]string{"unit": "nginx.service"},
		"alice", "", "")
	if err != nil {
		t.Fatalf("应部分成功: %v", err)
	}
	if res.Created != 1 || res.Failed != 1 {
		t.Fatalf("应 1 成功 1 失败，实际 %+v", res)
	}
	if !strings.Contains(res.Items[0].Error, "guards.ops") {
		t.Fatalf("未放行的节点应提示去改 guards.ops，实际 %q", res.Items[0].Error)
	}
}

// 整批失败（参数不合法、没有节点、超上限）必须返回 error，而不是"创建 0 条"的成功。
func TestCreateBatch_RejectsWholeBatch(t *testing.T) {
	svc := NewService(newTestStore(t))

	if _, err := svc.CreateBatch(nil, KindNodeDiagnostics, nil, "alice", "", ""); err == nil {
		t.Fatal("没有目标节点应整批失败")
	}
	if _, err := svc.CreateBatch([]string{"web-01"}, KindSvcStatus, map[string]string{"unit": "--now"}, "alice", "", ""); err == nil {
		t.Fatal("参数不合法应整批失败")
	} else if errors.Is(err, ErrUnsupported) {
		t.Fatalf("参数错误不该报成「节点不支持」：%v", err)
	}

	nodes := make([]string, maxBatchNodes+1)
	for i := range nodes {
		nodes[i] = "n" + itoa(int64(i))
	}
	if _, err := svc.CreateBatch(nodes, KindNodeDiagnostics, nil, "alice", "", ""); err == nil {
		t.Fatal("超过上限应显式报错")
	} else if !strings.Contains(err.Error(), "分批") {
		t.Fatalf("超限提示应告诉用户怎么办（分批下发），实际 %q", err.Error())
	}
}

// 取消：只有 queued 能撤；已领取的必须逐条报"撤不回来"，而不是假装成功。
func TestCancel_OnlyQueuedCanBeCancelled(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store)
	store.SaveCaps("web-01", []string{KindNodeDiagnostics})
	store.SaveCaps("db-01", []string{KindNodeDiagnostics})
	store.SaveCaps("web-02", []string{KindNodeDiagnostics})

	res, err := svc.CreateBatch([]string{"web-01", "db-01", "web-02"}, KindNodeDiagnostics, nil, "alice", "", "")
	if err != nil {
		t.Fatalf("批量下发失败: %v", err)
	}
	// 让 db-01 的任务被"领取"，模拟已经发出去
	if cmd := store.Take("db-01", []string{KindNodeDiagnostics}); cmd == nil {
		t.Fatal("准备环境失败：应能领取到任务")
	}

	out := svc.Cancel(nil, res.BatchID, "alice")
	if out.Cancelled != 2 || out.Failed != 1 {
		t.Fatalf("整批取消应成功 2 条、失败 1 条（已下发），实际 %+v", out)
	}
	for _, it := range out.Items {
		if it.Node == "db-01" {
			if it.OK || !strings.Contains(it.Error, "无法取消") {
				t.Fatalf("已被领取的任务应明确报「无法取消」：%+v", it)
			}
			continue
		}
		if !it.OK || it.State != model.OpsStateCancelled {
			t.Fatalf("排队中的任务应被取消：%+v", it)
		}
	}
	// 取消后不留下"还在排队"的假象：状态确实写成 cancelled
	if t2, _ := store.Get(res.Items[0].TaskID); t2.State != model.OpsStateCancelled {
		t.Fatalf("取消后的状态应为 cancelled，实际 %s", t2.State)
	}
	// 已领取的那条必须保持 delivered（不能因为"整批取消"被改成已取消）
	if t2, _ := store.Get(res.Items[1].TaskID); t2.State != model.OpsStateDelivered {
		t.Fatalf("已下发的任务状态不应被改写，实际 %s", t2.State)
	}
	// 再取消一次（已终态）应失败
	again := svc.Cancel([]string{res.Items[0].TaskID}, "", "alice")
	if again.Cancelled != 0 || again.Failed != 1 {
		t.Fatalf("终态任务不应被再次取消：%+v", again)
	}
	// 未知 ID 也走"失败项"而不是 panic
	unknown := svc.Cancel([]string{"ops-999"}, "", "alice")
	if unknown.Failed != 1 || !strings.Contains(unknown.Items[0].Error, "不存在") {
		t.Fatalf("未知任务应给出「任务不存在」：%+v", unknown)
	}
}

// 删除只对终态开放：删掉排队中/执行中的任务会让"这条指令去哪了"无从回答。
func TestDelete_OnlyTerminal(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(store)
	store.SaveCaps("web-01", []string{KindNodeDiagnostics})
	task, err := svc.Create("web-01", KindNodeDiagnostics, nil, "alice", "", "")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := svc.Delete(task.ID, "alice"); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("排队中的任务不应可删，实际 %v", err)
	}
	if res := svc.Cancel([]string{task.ID}, "", "alice"); res.Cancelled != 1 {
		t.Fatalf("取消应成功：%+v", res)
	}
	if err := svc.Delete(task.ID, "alice"); err != nil {
		t.Fatalf("终态任务应可删：%v", err)
	}
	if _, ok := store.Get(task.ID); ok {
		t.Fatal("删除后不应还能查到")
	}
}

// 批次号必须唯一：两批共号会让"整批取消"误伤另一批。
func TestNextBatchID_Unique(t *testing.T) {
	store := newTestStore(t)
	a, b := store.NextBatchID(), store.NextBatchID()
	if a == b {
		t.Fatalf("两次批次号不应相同：%s / %s", a, b)
	}
}
