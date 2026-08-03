// Package storage 实现时序存储适配层。
// 写入走 Prometheus remote_write（精简 prompb struct + snappy）；查询走 PromQL。
// 兼容 VictoriaMetrics / Grafana Mimir / Cortex / Thanos Receive / 任意 PromQL 时序库。
package storage

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
)

// isValidMetricName 校验 PromQL 指标名（含 __name__ 等保留名同样适用）。
// 规则：首字符为字母/_:，后续可为字母/数字/_:。
func isValidMetricName(n string) bool {
	if n == "" {
		return false
	}
	for i, r := range n {
		switch {
		case r == '_' || r == ':':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// isValidLabelName 校验 PromQL 标签名（label 名不允许以数字开头，且不含 :）。
func isValidLabelName(n string) bool {
	if n == "" {
		return false
	}
	for i, r := range n {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// isValidLabelValue 仅做长度与不可打印字符约束；具体转义由 quotePromQLValue 完成。
func isValidLabelValue(v string) bool {
	if v == "" || len(v) > 4096 {
		return false
	}
	for _, r := range v {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}

// quotePromQLValue 将任意字符串转义为 PromQL 双引号字符串字面量，
// 转义反斜杠与双引号，防止通过标签值注入额外的选择器/闭合花括号。
func quotePromQLValue(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// buildExpr 安全拼接 PromQL 表达式：对指标名、标签名做白名单校验，
// 对标签值做转义，杜绝 PromQL 注入（如通过 node 传入 `"} or up{__name__=~".*"}}`）。
// node 为空时表示跨节点查询（不限定 node 标签）。
func buildExpr(node, name string, labels map[string]string) (string, error) {
	if !isValidMetricName(name) {
		return "", fmt.Errorf("invalid metric name: %q", name)
	}
	selectors := make([]string, 0, len(labels)+1)
	if node != "" {
		if !isValidLabelValue(node) {
			return "", fmt.Errorf("invalid node label value: %q", node)
		}
		selectors = append(selectors, "node="+quotePromQLValue(node))
	}
	for k, v := range labels {
		if !isValidLabelName(k) {
			return "", fmt.Errorf("invalid label name: %q", k)
		}
		if !isValidLabelValue(v) {
			return "", fmt.Errorf("invalid label value for %s: %q", k, v)
		}
		selectors = append(selectors, k+"="+quotePromQLValue(v))
	}
	expr := name
	if len(selectors) > 0 {
		expr += "{" + strings.Join(selectors, ",") + "}"
	}
	return expr, nil
}

// Storage 是时序存储抽象。实现方为 PromQL 生态时序库适配层。
type Storage interface {
	// Write 批量写入指标样本。
	Write(metrics []model.Metric) error
	// QueryRange 按时间范围查询某节点某指标，step 为毫秒，内部转换为秒交给后端降采样。
	QueryRange(node, name string, labels map[string]string, start, end, step int64) ([]model.Series, error)
	// QueryLatest 查询最近一个样本点（用于 WebSocket 回退）。
	QueryLatest(node, name string, labels map[string]string) (*model.Point, error)
	// QueryInstant 即时查询，返回所有匹配序列（向量结果），用于进程 TOP 等多序列场景。
	QueryInstant(node, name string, labels map[string]string) ([]model.Series, error)
	// QueryAllLatest 对单一指标跨所有节点做即时查询，返回按节点标签分组的全部序列（用于主机列表聚合）。
	QueryAllLatest(name string, labels map[string]string) ([]model.Series, error)
	// Close 释放资源。
	Close() error
	// Backend 返回后端类型名（用于日志/诊断）。
	Backend() string
}

// PromStorage 是基于 Prometheus remote_write + PromQL 的时序存储实现。
// 由于 VictoriaMetrics / Mimir / Cortex / Thanos 等共享 PromQL 读取协议，
// 仅写入路径（remote_write 接收端点）存在差异，因此统一用本实现，按 backend 映射写路径。
type PromStorage struct {
	backend       string
	addr          string
	writeURL      string
	queryURL      string
	queryRangeURL string
	httpClient    *http.Client
}

// defaultWritePath 返回各后端默认的 remote_write 写入路径。
func defaultWritePath(backend string) (string, error) {
	switch backend {
	case "victoriametrics", "":
		return "/api/v1/write", nil
	case "mimir", "cortex":
		return "/api/v1/push", nil
	case "thanos":
		return "/api/v1/receive", nil
	case "prometheus":
		// Prometheus 本身不直接接收 remote_write，通常经 Receiver/Adapter 转发；
		// 这里默认走 Thanos Receive 风格路径，建议显式使用 custom 指定真实写入端点。
		return "/api/v1/receive", nil
	case "custom":
		return "", fmt.Errorf("backend=custom 必须在配置中指定 writePath")
	default:
		return "", fmt.Errorf("不支持的时序库后端: %s（可选: victoriametrics|mimir|cortex|thanos|prometheus|custom）", backend)
	}
}

// NewStorage 依据配置创建存储实现。
func NewStorage(cfg config.TSDBConfig) (Storage, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "victoriametrics"
	}
	wt := time.Duration(cfg.WriteTimeout) * time.Second
	if wt <= 0 {
		wt = 5 * time.Second
	}
	qt := time.Duration(cfg.QueryTimeout) * time.Second
	if qt <= 0 {
		qt = 10 * time.Second
	}

	writePath := cfg.WritePath
	if writePath == "" {
		p, err := defaultWritePath(backend)
		if err != nil {
			return nil, err
		}
		writePath = p
	}
	queryPath := cfg.QueryPath
	if queryPath == "" {
		queryPath = "/api/v1/query"
	}
	queryRangePath := cfg.QueryRangePath
	if queryRangePath == "" {
		queryRangePath = "/api/v1/query_range"
	}

	addr := cfg.Addr
	if addr == "" {
		return nil, fmt.Errorf("时序库地址(addr)不能为空")
	}
	// 查询基址：默认与写入基址一致；Thanos/Cortex 等查询与写入端口不同时用 queryAddr 覆盖
	queryBase := addr
	if cfg.QueryAddr != "" {
		queryBase = cfg.QueryAddr
	}
	return &PromStorage{
		backend:       backend,
		addr:          addr,
		writeURL:      addr + writePath,
		queryURL:      queryBase + queryPath,
		queryRangeURL: queryBase + queryRangePath,
		httpClient:    &http.Client{Timeout: maxDuration(wt, qt)},
	}, nil
}

// New 兼容旧调用（默认 VictoriaMetrics 单地址）。
func New(addr string, writeTimeout, queryTimeout int) Storage {
	s, err := NewStorage(config.TSDBConfig{Addr: addr, WriteTimeout: writeTimeout, QueryTimeout: queryTimeout})
	if err != nil {
		// 旧签名无 error，降级为默认 VM 写路径，保证向后兼容
		return &PromStorage{
			backend:       "victoriametrics",
			addr:          addr,
			writeURL:      addr + "/api/v1/write",
			queryURL:      addr + "/api/v1/query",
			queryRangeURL: addr + "/api/v1/query_range",
			httpClient:    &http.Client{Timeout: maxDuration(time.Duration(writeTimeout)*time.Second, time.Duration(queryTimeout)*time.Second)},
		}
	}
	return s
}

// Backend 返回后端类型名。
func (s *PromStorage) Backend() string { return s.backend }

// maxDuration 返回较大的 duration。
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
