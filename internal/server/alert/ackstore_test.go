package alert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAckStore_MarkThenHandled 认领：状态为 ack、处理人为本人、计入「已处理」。
func TestAckStore_MarkThenHandled(t *testing.T) {
	s := NewAckStore(filepath.Join(t.TempDir(), "acks.json"))
	info := s.Mark("r1", "web-01", "cpu_usage", 1000, "ops1")

	if info.Status != StatusAck || info.Assignee != "ops1" || info.User != "ops1" {
		t.Fatalf("认领结果不符：%+v", info)
	}
	if info.AckTime == 0 || info.Time == 0 {
		t.Fatalf("应记录认领与操作时间：%+v", info)
	}
	if !s.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("认领后应视为已处理")
	}
	if s.IsHandled("r1", "other", "cpu_usage", 1000) {
		t.Fatal("不同节点不应互相影响")
	}
	got, ok := s.Get("r1", "web-01", "cpu_usage", 1000)
	if !ok || got.Status != StatusAck {
		t.Fatalf("Get 应返回认领记录：%+v ok=%v", got, ok)
	}
}

// TestAckStore_LegacyRecordTreatedAsAck 升级兼容：
// 旧文件里的记录没有 status 字段，必须按「已认领」处理，否则升级后既有确认会重新冒出来。
func TestAckStore_LegacyRecordTreatedAsAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acks.json")
	legacy := `[{"rule":"r1","host":"web-01","instance":"cpu_usage","startsAt":1000,"user":"ops","time":123}]`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("写入旧记录失败: %v", err)
	}

	s := NewAckStore(path)
	info, ok := s.Get("r1", "web-01", "cpu_usage", 1000)
	if !ok {
		t.Fatal("应加载旧记录")
	}
	if info.EffectiveStatus() != StatusAck {
		t.Fatalf("旧记录应视为已认领，got %q", info.EffectiveStatus())
	}
	if !info.Handled() || !s.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("旧记录必须计入已处理")
	}
}

// TestAckStore_CloseAndReopen 关闭与重新打开：状态流转、关闭信息清理、认领时间与评论保留。
func TestAckStore_CloseAndReopen(t *testing.T) {
	s := NewAckStore(filepath.Join(t.TempDir(), "acks.json"))
	acked := s.Mark("r1", "web-01", "cpu_usage", 1000, "ops1")
	if _, err := s.Comment("r1", "web-01", "cpu_usage", 1000, "ops1", "正在扩容"); err != nil {
		t.Fatalf("评论失败: %v", err)
	}

	closed := s.Close("r1", "web-01", "cpu_usage", 1000, "ops2", " 误报，已调整阈值 ")
	if closed.Status != StatusClosed || closed.CloseReason != "误报，已调整阈值" {
		t.Fatalf("关闭结果不符：%+v", closed)
	}
	if closed.CloseTime == 0 || closed.User != "ops2" {
		t.Fatalf("应记录关闭时间与操作人：%+v", closed)
	}
	if !s.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("已关闭应视为已处理")
	}

	reopened := s.Reopen("r1", "web-01", "cpu_usage", 1000, "ops1")
	if reopened.Status != StatusPending {
		t.Fatalf("重新打开后应为待处理，got %q", reopened.Status)
	}
	if reopened.Assignee != "" || reopened.CloseReason != "" || reopened.CloseTime != 0 {
		t.Fatalf("重新打开应清空处理人与关闭信息：%+v", reopened)
	}
	if reopened.AckTime != acked.AckTime {
		t.Fatal("重新打开应保留认领时间")
	}
	if len(reopened.Comments) != 1 {
		t.Fatalf("重新打开应保留评论历史：%+v", reopened.Comments)
	}
	if s.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("重新打开后应重新回到待处理（不计入已处理）")
	}
}

// TestAckStore_Assign 指派：可指派他人；指派为空表示指派给当前操作者。
func TestAckStore_Assign(t *testing.T) {
	s := NewAckStore(filepath.Join(t.TempDir(), "acks.json"))

	info := s.Assign("r1", "web-01", "cpu_usage", 1000, "ops1", "ops2")
	if info.Status != StatusAck || info.Assignee != "ops2" || info.User != "ops1" {
		t.Fatalf("指派结果不符：%+v", info)
	}
	firstAck := info.AckTime

	// 再次指派不覆盖首次认领时间
	info = s.Assign("r1", "web-01", "cpu_usage", 1000, "ops2", "   ")
	if info.Assignee != "ops2" {
		t.Fatalf("空指派应回落到操作者本人，got %q", info.Assignee)
	}
	if info.AckTime != firstAck {
		t.Fatal("二次指派不应改写首次认领时间")
	}
}

// TestAckStore_Comment 评论：不改状态、去空白、校验空内容、截断超长、只保留最新 N 条。
func TestAckStore_Comment(t *testing.T) {
	s := NewAckStore(filepath.Join(t.TempDir(), "acks.json"))

	// 未处置的告警评论后应建立「待处理」记录
	info, err := s.Comment("r1", "web-01", "cpu_usage", 1000, "ops1", "  已收到  ")
	if err != nil {
		t.Fatalf("评论失败: %v", err)
	}
	if info.Status != StatusPending {
		t.Fatalf("新记录应为待处理，got %q", info.Status)
	}
	if len(info.Comments) != 1 || info.Comments[0].Text != "已收到" {
		t.Fatalf("评论内容应去空白：%+v", info.Comments)
	}
	if s.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("仅评论不应视为已处理")
	}

	// 已关闭的告警评论不得改变状态
	s.Close("r2", "web-01", "cpu_usage", 2000, "ops1", "误报")
	info, _ = s.Comment("r2", "web-01", "cpu_usage", 2000, "ops2", "补充说明")
	if info.Status != StatusClosed {
		t.Fatalf("评论不应改变处置状态，got %q", info.Status)
	}

	// 空内容拒绝
	if _, err := s.Comment("r1", "web-01", "cpu_usage", 1000, "ops1", "   "); err != ErrEmptyComment {
		t.Fatalf("空评论应被拒绝，got %v", err)
	}

	// 超长截断（按字符而非字节，避免切断多字节汉字）
	long := strings.Repeat("告", maxCommentLength+10)
	info, _ = s.Comment("r3", "web-01", "cpu_usage", 3000, "ops1", long)
	if got := len([]rune(info.Comments[0].Text)); got != maxCommentLength {
		t.Fatalf("超长评论应截断到 %d 字符，got %d", maxCommentLength, got)
	}

	// 只保留最新 N 条
	s2 := NewAckStore(filepath.Join(t.TempDir(), "acks.json"))
	for i := 0; i < maxAckComments+5; i++ {
		if _, err := s2.Comment("r9", "web-01", "cpu_usage", 9000, "ops1", "第"+string(rune('A'+i%26))+"条"); err != nil {
			t.Fatalf("评论失败: %v", err)
		}
	}
	got, _ := s2.Get("r9", "web-01", "cpu_usage", 9000)
	if len(got.Comments) != maxAckComments {
		t.Fatalf("评论应只保留最新 %d 条，got %d", maxAckComments, len(got.Comments))
	}
}

// TestAckStore_PersistAndReload 落盘与重载：处置状态、指派、关闭原因与评论都应持久化。
func TestAckStore_PersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "acks.json")
	s := NewAckStore(path)
	s.Mark("r1", "web-01", "cpu_usage", 1000, "ops1")
	s.Close("r2", "db-01", "disk_used_percent", 2000, "ops2", "已扩容")
	if _, err := s.Comment("r2", "db-01", "disk_used_percent", 2000, "ops2", "扩容完成"); err != nil {
		t.Fatalf("评论失败: %v", err)
	}

	reloaded := NewAckStore(path)
	if !reloaded.IsHandled("r1", "web-01", "cpu_usage", 1000) {
		t.Fatal("认领状态应持久化")
	}
	closed, ok := reloaded.Get("r2", "db-01", "disk_used_percent", 2000)
	if !ok {
		t.Fatal("关闭记录应持久化")
	}
	if closed.Status != StatusClosed || closed.CloseReason != "已扩容" {
		t.Fatalf("关闭原因应持久化：%+v", closed)
	}
	if len(closed.Comments) != 1 || closed.Comments[0].Text != "扩容完成" {
		t.Fatalf("评论应持久化：%+v", closed.Comments)
	}
	if len(reloaded.Map()) != 2 {
		t.Fatalf("应加载 2 条记录，got %d", len(reloaded.Map()))
	}
}

// TestAckStore_NilSafe 空接收者不 panic（引擎与接口都可能拿到 nil 存储）。
func TestAckStore_NilSafe(t *testing.T) {
	var s *AckStore
	if s.IsHandled("r", "h", "i", 1) {
		t.Fatal("nil 存储应返回未处理")
	}
	if _, ok := s.Get("r", "h", "i", 1); ok {
		t.Fatal("nil 存储不应返回记录")
	}
}
