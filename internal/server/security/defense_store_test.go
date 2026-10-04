package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 防护任务曾经是**纯追加**的：只标 expired、从不删除，而每次状态变化都要全量重写整个文件。
// 于是封禁次数越多、文件越大、每次写越慢、启动读得越久，且没有任何地方会提示这件事。
// 这组用例钉住修复后的两条规矩：**超量的已结束任务要裁掉**、**超过 TTL 的未结束任务要回收**，
// 而**还在等回执的任务任何情况下都不能丢**（丢了操作者就永远不知道结果）。

func newTestDefenseStore(t *testing.T) *DefenseStore {
	t.Helper()
	return NewDefenseStore(filepath.Join(t.TempDir(), "defense_tasks.json"))
}

// putTask 直接往存储里塞一条任务。
//
// 白盒是有意的：用例要构造"存量已有几百条"的形态，而走 Create → Take → UpdateResult
// 那条真实链路每条都会落一次盘（几百次全量写），慢到不可接受；那条链路本身由其它用例覆盖。
func putTask(s *DefenseStore, id, state string, createdAt, expiresAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = &DefenseTask{
		DefenseCommand: model.DefenseCommand{
			ID: id, Node: "web-01", CreatedAt: createdAt, ExpiresAt: expiresAt,
		},
		State: state,
	}
}

func storedCount(s *DefenseStore) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tasks)
}

func storedState(s *DefenseStore, id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t := s.tasks[id]; t != nil {
		return t.State
	}
	return ""
}

// 超量时丢最老的**已结束**任务，且未结束的任务一条都不能丢。
func TestDefenseStorePrunesOldestFinishedBeyondCap(t *testing.T) {
	s := newTestDefenseStore(t)
	// 时间基准取"现在"：这两条未结束的任务必须是**未过期**的，
	// 否则它们会先被回收成 expired、再被当作已结束任务裁掉——那是另一条路径（见回收用例）。
	base := time.Now().UnixMilli()

	// 两条未结束（且未过期）的任务，创建时间比所有已结束的都早：
	// 如果裁剪是"按时间丢最老的"，它们会第一个被丢——那是绝不允许的。
	putTask(s, "pending-oldest", model.DefenseStateQueued, base-2000, base+3600_000)
	putTask(s, "pending-2", model.DefenseStateDelivered, base-1000, base+3600_000)
	for i := 0; i < maxDefenseTasks+5; i++ {
		putTask(s, fmt.Sprintf("done-%03d", i), model.DefenseStateSucceeded, base+int64(i), 0)
	}

	s.save()

	if got := storedCount(s); got != maxDefenseTasks {
		t.Fatalf("应裁到上限 %d 条，实际 %d", maxDefenseTasks, got)
	}
	if storedState(s, "pending-oldest") != model.DefenseStateQueued || storedState(s, "pending-2") != model.DefenseStateDelivered {
		t.Fatal("未结束的任务不该被丢弃：它们还有回执要等，丢掉等于让操作者永远不知道结果")
	}
	if storedState(s, "done-000") != "" {
		t.Fatal("应丢弃最老的已结束任务")
	}
	// 最新的那条必须还在（回执要被看到）
	if storedState(s, fmt.Sprintf("done-%03d", maxDefenseTasks+4)) != model.DefenseStateSucceeded {
		t.Fatal("最新的已结束任务不该被丢弃")
	}
}

// 超过 TTL 仍未结束的任务要被回收为 expired——否则一条没人来领的 queued 任务
// 会永远停在 queued：既不结束、也不可裁，白占一条量。
func TestDefenseStoreExpiresOverdueOnSave(t *testing.T) {
	s := newTestDefenseStore(t)
	now := time.Now().UnixMilli()
	putTask(s, "stale", model.DefenseStateQueued, now-3600_000, now-60_000)
	putTask(s, "fresh", model.DefenseStateQueued, now, now+3600_000)

	s.save()

	if got := storedState(s, "stale"); got != model.DefenseStateExpired {
		t.Fatalf("超过 TTL 的任务应被回收为 expired，实际 %q", got)
	}
	if got := storedState(s, "fresh"); got != model.DefenseStateQueued {
		t.Fatalf("未过期的任务不该被改动，实际 %q", got)
	}
	s.mu.RLock()
	msg := s.tasks["stale"].Message
	s.mu.RUnlock()
	if msg == "" {
		t.Fatal("回收要带一句可读原因，否则界面上只会看到一个莫名的 expired")
	}
}

// 存量文件已经很大时，**加载时**就要自愈（老版本没有上限）：
// 这份文件每次都要整个读进内存，等到"下一次有人点封禁"才缩回去太迟。
func TestDefenseStoreLoadHealsBloatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defense_tasks.json")
	base := int64(1_700_000_000_000)
	tasks := make([]*DefenseTask, 0, maxDefenseTasks+20)
	for i := 0; i < maxDefenseTasks+20; i++ {
		tasks = append(tasks, &DefenseTask{
			DefenseCommand: model.DefenseCommand{ID: fmt.Sprintf("t-%03d", i), Node: "web-01", CreatedAt: base + int64(i)},
			State:          model.DefenseStateSucceeded,
		})
	}
	blob, err := json.Marshal(map[string]any{"tasks": tasks, "caps": map[string]bool{"web-01": true}})
	if err != nil {
		t.Fatalf("构造存量文件失败: %v", err)
	}
	if err := os.WriteFile(path, blob, 0600); err != nil {
		t.Fatalf("写入存量文件失败: %v", err)
	}

	s := NewDefenseStore(path)

	if got := storedCount(s); got != maxDefenseTasks {
		t.Fatalf("加载后应裁到 %d 条，实际 %d", maxDefenseTasks, got)
	}
	// 能力清单不能被裁掉：它记录"哪个节点支持结构化防护指令"，与任务量无关
	s.mu.RLock()
	caps := len(s.caps)
	s.mu.RUnlock()
	if caps != 1 {
		t.Fatalf("能力清单应原样保留，实际 %d 项", caps)
	}

	// 文件也要被就地重写：否则下次启动还得再读一遍那份巨大的旧文件
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取自愈后的文件失败: %v", err)
	}
	var reloaded struct {
		Tasks []*DefenseTask `json:"tasks"`
	}
	if err := json.Unmarshal(after, &reloaded); err != nil {
		t.Fatalf("解析自愈后的文件失败: %v", err)
	}
	if len(reloaded.Tasks) != maxDefenseTasks {
		t.Fatalf("文件应被重写为 %d 条，实际 %d", maxDefenseTasks, len(reloaded.Tasks))
	}
}

// 裁剪是**尽力而为**：未结束的任务本身就超上限时，绝不为了凑数去丢还在等回执的任务。
func TestDefenseStoreDoesNotDropPendingToSatisfyCap(t *testing.T) {
	s := newTestDefenseStore(t)
	now := time.Now().UnixMilli()
	for i := 0; i < maxDefenseTasks+10; i++ {
		putTask(s, fmt.Sprintf("q-%03d", i), model.DefenseStateQueued, now+int64(i), now+3600_000)
	}

	s.save()

	if got := storedCount(s); got != maxDefenseTasks+10 {
		t.Fatalf("等待回执的任务不该被丢弃，实际剩下 %d", got)
	}
}
