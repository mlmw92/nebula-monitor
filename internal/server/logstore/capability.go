package logstore

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 能力与现状探测（批次 23）。
//
// 为什么要有：两个后端的检索能力完全不同（本地是"有界扫描"、VictoriaLogs 是索引检索），
// 但**部署方与使用者在界面上看不到自己在哪一种下**——于是"我要按字段筛百万行"这个需求，
// 既不知道当前部署做不到，也不知道改个配置就能做到。这里如实回答两件事：
// 该后端**能做什么**（能力），以及**存了什么**（探测）。

// probeMaxEntries 是本地后端探测时最多列举的日志文件数。
// 触顶即停并标记 Truncated：报"至少这么多"，而不是为了一个提示把磁盘扫一遍。
const probeMaxEntries = 20000

// dayLayout 是分片目录名的日期格式（见包注释里的存储布局）。
const dayLayout = "2006-01-02"

// localCapability 回答本地后端的能力与现状。
func (s *Store) Capability() Capability {
	cap := Capability{
		Backend: BackendLocal,
		Notes: []string{
			"检索是「按时间/来源/节点挑文件 + 反向逐行扫描」，**没有倒排索引**——这是刻意的：" +
				"自建索引要与分片保持一致，半套索引比没有索引更危险",
			"要百万行级的关键词/字段筛选，请把日志后端切到 VictoriaLogs（平台已支持），而不是加大扫描预算",
		},
	}
	if s == nil {
		cap.Storage.Err = "本地后端未初始化（未配置日志目录）"
		return cap
	}
	cap.ScanBudget = &ScanBudget{Bytes: s.scanBudgetBytes, Lines: int(s.scanBudgetLines)}
	cap.Storage = s.probeStorage(probeMaxEntries)
	return cap
}

// probeStorage 走一遍「来源 / 日期」两级目录做统计，**只 stat 与列举、不读文件内容**：
// 这是"看一眼存了什么"，不是统计任务。limit 是列举文件数的上限（触顶即停并标记 Truncated），
// 做成参数只为用例能注入小值——默认值见 probeMaxEntries。
func (s *Store) probeStorage(limit int) StorageStats {
	var st StorageStats
	if strings.TrimSpace(s.root) == "" {
		return st
	}
	sources, err := os.ReadDir(s.root)
	if err != nil {
		if !os.IsNotExist(err) {
			// 目录不存在 = 还没写过日志，是正常状态（0 条）；读不了才是问题，如实报出来
			st.Err = "读取日志目录失败: " + err.Error()
		}
		return st
	}
	nodes := map[string]struct{}{}
	entries := 0
	for _, srcEntry := range sources {
		if !srcEntry.IsDir() {
			continue // 字段目录等文件不是来源（见 fieldCatalogFile）
		}
		st.Sources++
		days, err := os.ReadDir(filepath.Join(s.root, srcEntry.Name()))
		if err != nil {
			continue
		}
		for _, dayEntry := range days {
			if !dayEntry.IsDir() {
				continue
			}
			// 只认日期形态的目录名：异常目录名不该污染"能查到哪一天"这个结论
			if _, err := time.Parse(dayLayout, dayEntry.Name()); err != nil {
				continue
			}
			day := dayEntry.Name()
			if st.OldestDay == "" || day < st.OldestDay {
				st.OldestDay = day
			}
			if day > st.NewestDay {
				st.NewestDay = day
			}
			files, err := os.ReadDir(filepath.Join(s.root, srcEntry.Name(), day))
			if err != nil {
				continue
			}
			for _, f := range files {
				if entries >= limit {
					st.Truncated = true
					st.Nodes = len(nodes)
					return st
				}
				entries++
				nodes[strings.TrimSuffix(f.Name(), ".log")] = struct{}{}
				if info, err := f.Info(); err == nil {
					st.Bytes += info.Size()
				}
			}
		}
	}
	st.Nodes = len(nodes)
	return st
}

// vlCapability 回答外部后端的能力与现状。
func (v *VictoriaLogs) Capability() Capability {
	cap := Capability{
		Backend:       BackendVictoriaLogs,
		FullTextIndex: true,
		FieldIndex:    true,
		Notes: []string{
			"关键词、正则、字段、节点与容器身份**全部下推**给 VictoriaLogs，由它自己的倒排索引检索",
			"容量与保留期由 VictoriaLogs 自己管（-retentionPeriod），平台不掌握，因此不报存储字节与可查时间跨度",
			"只检索切换之后的日志：历史本地分片**不会自动迁入**（换后端是换存储，不是迁移）",
		},
	}
	if v == nil {
		cap.Storage.Err = "外部后端未初始化"
		return cap
	}
	// 探测用一个**带时间窗的只读查询**（字段目录）：它不扫全量（见 vlCatalogWindow），
	// 拿得到说明后端可达；拿不到就如实报"探测失败"——把探测失败显示成 0 条，
	// 用户会以为日志丢了，那正是这条能力端点要避免的误导。
	form := url.Values{}
	form.Set("query", "_time:"+vlCatalogWindow)
	if _, err := v.valuesOf("field_names", form); err != nil {
		cap.Storage.Err = "探测外部后端失败: " + err.Error()
		return cap
	}
	cap.Storage.Sources = len(v.Sources())
	return cap
}
