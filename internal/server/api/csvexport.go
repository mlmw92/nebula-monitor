package api

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// csvCell 消毒单个 CSV 单元格，防"公式注入"。
//
// 为什么需要：Excel / WPS 会把以 `=` `+` `-` `@` 开头的单元格当**公式**执行
// （`=cmd|'/C calc'!A0` 这类历史上有过实际利用），Tab 与 CR 还能用来从单元格里逃逸。
// 而 `encoding/csv` 只管转义逗号与引号，对公式前缀一无所知——它是 CSV 语法层面的转义，
// 不是**电子表格语义**层面的消毒。
//
// 做法：命中前缀时在前面加一个单引号（Excel 视为"这是文本"），其它解析器也只会看到
// 一个普通的单引号字符，不会改变数据含义。
func csvCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// csvCells 对整行逐格消毒。
func csvCells(row []string) []string {
	out := make([]string, len(row))
	for i, cell := range row {
		out[i] = csvCell(cell)
	}
	return out
}

// csvDownload 是导出类接口的统一出口：设置响应头 + 写 BOM + 逐行写出。
//
// 为什么统一：此前三处导出各写一遍，差异是真实存在的——审计那份漏了 BOM
// （Excel 打开中文列名乱码），而三处**都没有**公式注入消毒。统一到这里，
// 顺带把两处修掉，避免下次新增导出时再漏一遍。
//
// rows 用回调逐行产出，而不是收一个 [][]string：导出可能有数万行，
// 先拼齐再写会白白多一份内存（且拼的过程中出错更晚才被发现）。
func csvDownload(w http.ResponseWriter, filename string, header []string, rows func(write func([]string))) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.WriteHeader(http.StatusOK)
	// BOM：Excel 打开中文列名不乱码（少了这三个字节，中文表头就是乱码）。
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))

	cw := csv.NewWriter(w)
	_ = cw.Write(csvCells(header))
	rows(func(row []string) { _ = cw.Write(csvCells(row)) })
	cw.Flush()
	if err := cw.Error(); err != nil {
		// 响应头已经发出，改不了状态码；如实记日志，避免"导出到一半失败却没人知道"。
		slog.Error("写 CSV 导出失败", "file", filename, "err", err)
	}
}

// csvFilename 生成带时间戳的导出文件名：同一天导出多次不会互相覆盖，
// 也便于把两份文件按时间对齐比较。
func csvFilename(prefix string) string {
	return fmt.Sprintf("%s-%s.csv", prefix, time.Now().Format("20060102-150405"))
}
