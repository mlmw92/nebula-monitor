package logstore

import (
	"os"
	"path/filepath"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 保留清理与容量统计（C2 子批次 E）。
//
// 清理沿用的是**同一套分片布局**：日期目录名是 YYYY-MM-DD，字典序即时间序，
// 因此「删掉比 cutoff 更早的日期目录」既简单又不会误删当天的数据。
// 这也是分片布局除了「有界扫描」之外的第二个好处——按时间删除不需要逐文件判断。

// Stats 是集中日志的占用情况（供保留策略页展示，回答「日志到底占了多少盘」）。
type Stats struct {
	Sources int   `json:"sources"`
	Days    int   `json:"days"` // 日期分片目录总数
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
}

// Stats 统计当前占用。
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	var out Stats
	sources, err := os.ReadDir(s.root)
	if err != nil {
		return out
	}
	for _, src := range sources {
		if !src.IsDir() || !model.IsValidLogSourceName(src.Name()) {
			continue
		}
		out.Sources++
		days, err := os.ReadDir(filepath.Join(s.root, src.Name()))
		if err != nil {
			continue
		}
		for _, day := range days {
			if !day.IsDir() {
				continue
			}
			out.Days++
			entries, err := os.ReadDir(filepath.Join(s.root, src.Name(), day.Name()))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if info, err := e.Info(); err == nil {
					out.Files++
					out.Bytes += info.Size()
				}
			}
		}
	}
	return out
}

// PruneResult 是一次清理的结果。
type PruneResult struct {
	DirsRemoved  int   `json:"dirsRemoved"`
	FilesRemoved int   `json:"filesRemoved"`
	Bytes        int64 `json:"bytes"`
}

// PruneBefore 删除 cutoff 之前（按日期）的日志分片，返回删除量。
//
// 只删**整天**：日志的时间戳精度在小时/分钟级，而存储按天分片——
// 为了「精确到分钟」去逐行判断会把清理变成一次全量读，而收益只是多留几小时。
// 因此保留期实际是「保留 N 天后、再按整天对齐」，与「报告保留」的处理方式一致。
func (s *Store) PruneBefore(cutoff time.Time) PruneResult {
	var res PruneResult
	if s == nil {
		return res
	}
	cutoffDate := cutoff.Format("2006-01-02")
	today := time.Now().Format("2006-01-02")

	sources, err := os.ReadDir(s.root)
	if err != nil {
		return res
	}
	for _, src := range sources {
		if !src.IsDir() || !model.IsValidLogSourceName(src.Name()) {
			continue
		}
		srcDir := filepath.Join(s.root, src.Name())
		days, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}
		for _, day := range days {
			if !day.IsDir() {
				continue
			}
			date := day.Name()
			// 字典序比较即时间序；当天目录永不删除（防止 cutoff 配得过大时把正在写的目录删掉）
			if date >= cutoffDate || date == today {
				continue
			}
			dir := filepath.Join(srcDir, date)
			dirs, files, bytes := dirUsage(dir)
			if err := os.RemoveAll(dir); err != nil {
				// 单个目录删不掉（权限/被占用）不该中断整体清理
				continue
			}
			res.DirsRemoved += dirs
			res.FilesRemoved += files
			res.Bytes += bytes
			s.mu.Lock()
			delete(s.written, src.Name()+"|"+date) // 配额记录一并清掉，避免长期运行后 map 无界增长
			s.mu.Unlock()
		}
		// 来源目录空了就一并删掉，避免磁盘上留一堆空目录
		if entries, err := os.ReadDir(srcDir); err == nil && len(entries) == 0 {
			_ = os.Remove(srcDir)
		}
	}
	return res
}

// dirUsage 统计一个日期分片的文件数与字节数（删除前调用）。
func dirUsage(dir string) (dirs, files int, bytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, 0
	}
	dirs = 1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files++
		if info, err := e.Info(); err == nil {
			bytes += info.Size()
		}
	}
	return dirs, files, bytes
}
