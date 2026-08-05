// Package instancereg 保存各 Agent 节点最近一次上报的中间件实例配置清单。
//
// 背景：中间件实例清单原本完全依赖时序库的 *_instance_up 即时查询来枚举。
// 但 VictoriaMetrics / Prometheus 的即时查询对超过 lookback-delta（默认约 5 分钟）
// 的旧样本视为 stale 并返回空，一旦 Agent 所在主机宕机、停止上报，实例清单就
// 会瞬间消失，前端因此误判为"尚未配置 XX 监控"。
//
// 本注册表在每次上报时记录"最后已知的实例配置"，使 Agent 离线期间 Web 仍能从
// 这里枚举到已配置（但当前离线）的实例，正确展示为"离线"而非"未配置"。
//
// 注意：注册表为内存态；若 Server 重启且恰逢 Agent 离线，注册表会暂时为空，
// 待 Agent 重新上报后自动重建——这是可接受的有限窗口。
package instancereg

import (
	"sync"

	"github.com/nebula/monitor/internal/model"
)

// Registry 按节点保存最近一次上报的中间件实例清单。
type Registry struct {
	mu    sync.RWMutex
	mysql map[string][]model.MySQLInstance
}

// Default 是进程内共享的注册表单例，receiver 写入、API 读取。
var Default = NewRegistry()

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{mysql: make(map[string][]model.MySQLInstance)}
}

// SetMySQL 用某节点最新上报的 MySQL 实例清单覆盖该节点记录。
// 传入空切片表示当前未配置任何 MySQL 实例，对应节点记录被清除。
func (r *Registry) SetMySQL(node string, list []model.MySQLInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(list) == 0 {
		delete(r.mysql, node)
		return
	}
	cp := make([]model.MySQLInstance, len(list))
	for i, m := range list {
		m.Node = node // 规范化节点字段，确保与枚举键一致
		cp[i] = m
	}
	r.mysql[node] = cp
}

// MySQLInstances 返回所有节点已知的 MySQL 实例清单（含离线实例）。
func (r *Registry) MySQLInstances() []model.MySQLInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]model.MySQLInstance, 0, 8)
	for _, list := range r.mysql {
		out = append(out, list...)
	}
	return out
}
