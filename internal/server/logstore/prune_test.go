package logstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 保留清理的要点：
//  - **只删整天**、且**当天目录永不删**（否则正在写的目录会被清掉）；
//  - 只删早于 cutoff 的日期，边界当天（cutoff 那天）保留；
//  - 统计能回答「日志占了多少盘」（这是运维开日志前最想知道的事）；
//  - 配额记录随目录一起清掉（否则长期运行后 map 无界增长）。

func date(t time.Time) string { return t.Format("2006-01-02") }

func TestPruneBefore_RemovesOnlyOlderDays(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old1 := now.AddDate(0, 0, -10)
	old2 := now.AddDate(0, 0, -8)
	edge := now.AddDate(0, 0, -7) // cutoff 当天：必须保留
	recent := now.AddDate(0, 0, -1)
	for _, d := range []time.Time{old1, old2, edge, recent, now} {
		shard(t, root, "applog", date(d), "n1", model.LogHit{Ts: d.UnixMilli(), Text: "line-" + date(d)})
	}
	s := New(root, 0)

	before := s.Stats()
	if before.Sources != 1 || before.Days != 5 || before.Files != 5 {
		t.Fatalf("清理前统计不符：%+v", before)
	}

	res := s.PruneBefore(now.AddDate(0, 0, -7))
	if res.DirsRemoved != 2 || res.FilesRemoved != 2 {
		t.Fatalf("应删掉 2 个更早的日期分片，got %+v", res)
	}
	if res.Bytes <= 0 {
		t.Fatalf("应报告释放字节数（供界面展示），got %+v", res)
	}

	after := s.Stats()
	if after.Days != 3 || after.Files != 3 {
		t.Fatalf("清理后应剩 3 天（cutoff 当天 + 最近两天），got %+v", after)
	}
	// cutoff 当天必须还在
	if _, err := os.Stat(filepath.Join(root, "applog", date(edge))); err != nil {
		t.Fatalf("cutoff 当天不应被删除：%v", err)
	}
	// 更早的确实被删了
	if _, err := os.Stat(filepath.Join(root, "applog", date(old1))); !os.IsNotExist(err) {
		t.Fatalf("更早的日期分片应被删除，err=%v", err)
	}
}

// TestPruneBefore_NeverRemovesTodayEvenWithHugeCutoff 防御性：即使 cutoff 被配成未来
// （例如 LogsDays 被填了一个奇怪的负值后被 normalize 成 0 之前的中间态），当天目录也不能被删。
func TestPruneBefore_NeverRemovesTodayEvenWithHugeCutoff(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	shard(t, root, "applog", date(now), "n1", model.LogHit{Ts: now.UnixMilli(), Text: "today"})
	s := New(root, 0)

	s.PruneBefore(now.AddDate(0, 0, 30)) // 荒谬的 cutoff：等效于「全部过期」
	if _, err := os.Stat(filepath.Join(root, "applog", date(now))); err != nil {
		t.Fatalf("当天目录必须保留（它正在被写）：%v", err)
	}
}

// TestPruneBefore_CleansEmptySourceDir 分片删空后连来源目录一起清掉，不留空目录。
func TestPruneBefore_CleansEmptySourceDir(t *testing.T) {
	root := t.TempDir()
	old := time.Now().AddDate(0, 0, -30)
	shard(t, root, "applog", date(old), "n1", model.LogHit{Ts: old.UnixMilli(), Text: "x"})

	s := New(root, 0)
	s.PruneBefore(time.Now().AddDate(0, 0, -7))
	if entries, err := os.ReadDir(root); err == nil && len(entries) != 0 {
		t.Fatalf("空的来源目录应被清掉，got %v", entries)
	}
}

// TestPruneBefore_DropsQuotaState 清理掉的分片，其当日配额记录也要一并删除。
func TestPruneBefore_DropsQuotaState(t *testing.T) {
	root := t.TempDir()
	old := time.Now().AddDate(0, 0, -30)
	shard(t, root, "applog", date(old), "n1", model.LogHit{Ts: old.UnixMilli(), Text: "x"})
	s := New(root, 0)
	key := "applog|" + date(old)
	s.written[key] = 123

	s.PruneBefore(time.Now().AddDate(0, 0, -7))
	s.mu.Lock()
	_, still := s.written[key]
	s.mu.Unlock()
	if still {
		t.Fatal("被清理分片的配额记录应一并删除，避免长期运行后无界增长")
	}
}

// TestStats_EmptyAndMissingRoot 统计对「空目录/不存在的目录」都要安全。
func TestStats_EmptyAndMissingRoot(t *testing.T) {
	if st := New(t.TempDir(), 0).Stats(); st != (Stats{}) {
		t.Fatalf("空目录统计应为零值，got %+v", st)
	}
	if st := New(filepath.Join(t.TempDir(), "nope"), 0).Stats(); st != (Stats{}) {
		t.Fatalf("目录不存在应返回零值而不是报错，got %+v", st)
	}
	if st := (*Store)(nil).Stats(); st != (Stats{}) {
		t.Fatalf("nil 存储应返回零值，got %+v", st)
	}
}
