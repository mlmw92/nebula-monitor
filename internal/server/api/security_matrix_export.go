package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
)

// 合规矩阵单次导出的规模上限。
//
// 矩阵是"节点 × 检查项"的笛卡尔积，必须有上限；但**超限要明确拒绝，不能给半张表**：
// 半张合规矩阵拿去交审计，比一句报错危险得多（这也是本项目对"静默截断"的一贯立场）。
// 上限取 2000 台 / 20 万格：对当前体量（线上 4 台 × 5 项）没有约束力，
// 但它保证"将来某天几千台机器时不会悄悄少一半"。
const (
	maxMatrixHosts = 2000
	maxMatrixCells = 200000
)

// handleComplianceMatrixExport 导出"节点 × 安全基线检查项"的合规矩阵 CSV。
//
// GET /api/v1/security/baselines/export，权限 security:export。
//
// 为什么单设权限点：一次把**全部可见节点**的合规结论（哪些项没过）落盘，
// 与"在界面上逐台翻看"不是一个量级的动作（与 assets:export / audit:export 同一约定）。
//
// 三条硬口径（见设计件 §4）：
//  ① 行集合与 GET /security/baselines **完全一致**（共用 securityBaselinesInScope），
//     否则会出现"看得见却导不出"或"导出越权"；
//  ② 单元格三态 通过 / 未通过 / 空，**空 ≠ 未通过**；
//  ③ 规模超限或没有数据时**明确拒绝**并说清原因，不给只有表头的空文件。
func (a *API) handleComplianceMatrixExport(w http.ResponseWriter, r *http.Request) {
	baselines, enabled := a.securityBaselinesInScope(r)
	if !enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "安全基线未启用：无法导出合规矩阵",
		})
		return
	}
	if len(baselines) == 0 {
		// 刻意不给"只有表头的空 CSV"：那种文件看起来像"全部合规"，是危险的误导。
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "还没有安全基线数据可导出：请确认 Agent 已上报安全基线",
		})
		return
	}

	keys, names := matrixColumns(baselines)
	if msg := matrixLimitError(len(baselines), len(keys)); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	header := append([]string{"节点", "显示名", "IP", "评分", "检查时间"}, matrixHeader(keys, names)...)
	csvDownload(w, csvFilename("compliance-matrix"), header, func(write func([]string)) {
		for _, b := range baselines {
			row := []string{
				b.Node, b.DisplayName, b.NodeIP,
				strconv.FormatFloat(b.Score, 'f', -1, 64), formatCSVTime(b.CheckedAt),
			}
			for _, k := range keys {
				row = append(row, matrixCell(b, k))
			}
			write(row)
		}
	})

	// 留痕：与资产导出一致（导出是"把整份结论拿走"的动作，事后要能回答"谁在什么时候导了什么"）。
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: AuthenticatedUser(r), Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: audit.ClientIP(r), Succeeded: true,
			RequestID: RequestID(r),
			Category:  "security", Action: "export",
			Detail: fmt.Sprintf("导出合规矩阵 %d 台 × %d 项", len(baselines), len(keys)),
		})
	}
	slog.Info("已导出合规矩阵", "hosts", len(baselines), "checks", len(keys), "operator", AuthenticatedUser(r))
}

// matrixLimitError 判定矩阵规模是否超限，返回给用户看的报文（空串 = 通过）。
//
// 抽成纯函数是为了让**边界**能被穷尽地测：真造 2001 台机器的夹具要写 2001 次存储文件
// （每次 Ingest 都会整份落盘），测试会慢到没人愿意跑——而边界恰恰是上限最该被测到的地方
// （差一台机器就该拒绝）。HTTP 那条路径用的是同一个函数，不存在两套判定。
func matrixLimitError(hosts, checks int) string {
	cells := hosts * checks
	if hosts <= maxMatrixHosts && cells <= maxMatrixCells {
		return ""
	}
	return fmt.Sprintf("矩阵规模 %d 台 × %d 项 = %d 格，超过单次导出上限（%d 台 / %d 格）：请先按分组缩小范围",
		hosts, checks, cells, maxMatrixHosts, maxMatrixCells)
}

// matrixColumns 取所有节点检查项的**并集**与中文名，并按键排序。
//
// 为什么是并集而不是某个节点的清单：不同 Agent 版本上报的检查项可能不同，
// 用固定清单会漏掉新项、用"第一台的清单"会把别的机器的新项丢掉。
// 排序是刻意的：同一份数据两次导出必须能 diff（否则"这两份哪里不同"没法回答）。
func matrixColumns(baselines []model.SecurityBaseline) ([]string, map[string]string) {
	names := map[string]string{}
	seen := map[string]bool{}
	keys := make([]string, 0, 16)
	for _, b := range baselines {
		for _, it := range b.Items {
			if !seen[it.Key] {
				seen[it.Key] = true
				keys = append(keys, it.Key)
			}
			if names[it.Key] == "" {
				names[it.Key] = it.Name
			}
		}
	}
	sort.Strings(keys)
	return keys, names
}

// matrixHeader 把检查项拼成列头：`中文名(key)`。
// 这样"这一列是什么检查"不必再去翻别处，导出的文件自己就是完整的。
func matrixHeader(keys []string, names map[string]string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if n := strings.TrimSpace(names[k]); n != "" {
			out = append(out, fmt.Sprintf("%s(%s)", n, k))
		} else {
			out = append(out, k)
		}
	}
	return out
}

// matrixCell 取某节点在某检查项上的三态：通过 / 未通过 / 空。
//
// **空 ≠ 未通过**：该节点没有上报这一项（Agent 版本旧、或这一项对它不适用）时留空。
// 把"没采到"记成"不合规"，是这类矩阵最典型的错——按它开整改单就是错单；
// 反过来把"没采到"记成"通过"则会漏掉真实风险。两者都必须与"未通过"区分开。
func matrixCell(b model.SecurityBaseline, key string) string {
	for _, it := range b.Items {
		if it.Key == key {
			if it.Pass {
				return "通过"
			}
			return "未通过"
		}
	}
	return ""
}
