// Package node 实现节点注册、分组管理与心跳维护。
// 分组关系持久化到 meta JSON 文件，启动加载，不依赖 VictoriaMetrics retention。
package node

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// metaFile 持久化结构。
type metaFile struct {
	Nodes  []model.Node  `json:"nodes"`
	Groups []model.Group `json:"groups"`
}

// upgradeTask 记录一个节点的升级任务（持续追踪直到 agent 版本匹配或超时）。
type upgradeTask struct {
	targetVersion string    // 期望 agent 升级到的版本
	createdAt     time.Time // 任务创建时间（用于超时清理）
	retries       int       // 已下发次数（用于限流与诊断）
}

// Manager 管理被监控节点与分组。
type Manager struct {
	mu             sync.RWMutex
	nodes          map[string]*model.Node // key: hostname
	groups         map[string]*model.Group
	metaPath       string
	offlineTimeout time.Duration
	upgradeQueue   map[string]*upgradeTask // 待升级节点集合（内存态，重启丢失）
}

// New 创建 Manager 并从 meta 文件加载已有节点/分组。
func New(metaPath string, offlineTimeout time.Duration) *Manager {
	m := &Manager{
		nodes:          map[string]*model.Node{},
		groups:         map[string]*model.Group{},
		metaPath:       metaPath,
		offlineTimeout: offlineTimeout,
		upgradeQueue:   map[string]*upgradeTask{},
	}
	m.load()
	// 默认分组
	if _, ok := m.groups["default"]; !ok {
		m.groups["default"] = &model.Group{Name: "default", Description: "默认分组", CreatedAt: model.NowMillis()}
		m.persist()
	}
	return m
}

// Register 处理一次上报：注册/更新节点并刷新心跳。
func (m *Manager) Register(p *model.ReportPayload) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := model.NowMillis()
	n, ok := m.nodes[p.Node]
	if !ok {
		// 新节点：Agent 上报的 group 仅作为默认值；若为空则归入默认分组
		n = &model.Node{
			Hostname:  p.Node,
			Group:     p.Group,
			Labels:    p.Labels,
			CreatedAt: now,
		}
		m.nodes[p.Node] = n
	}
	n.IP = p.IP
	n.OS = p.OS
	n.Arch = p.Arch
	// 已存在节点：分组以 Server 端（nodes.json）为准，避免 Agent 默认 group 覆盖用户自定义分组。
	// 仅当该节点尚无有效分组时，才用 Agent 上报的 group 作为兜底。
	if !ok || n.Group == "" {
		if p.Group != "" {
			n.Group = p.Group
		} else {
			n.Group = "default"
		}
	}
	if p.Labels != nil {
		n.Labels = p.Labels
	}
	if p.Version != "" {
		n.Version = p.Version
	}
	if hasHostInfo(p.HostInfo) {
		n.HostInfo = p.HostInfo
	}
	n.Status = "online"
	n.LastSeen = now
	m.persistLocked()
}

func hasHostInfo(info model.HostInfo) bool {
	return info.CPUModel != "" || info.CPUCores > 0 || info.MemoryTotal > 0 || info.DiskTotal > 0 || len(info.Disks) > 0
}

// OfflineStale 将超过离线阈值的节点标记为 offline。
func (m *Manager) OfflineStale() {
	m.mu.Lock()
	defer m.mu.Unlock()
	threshold := m.offlineTimeout.Milliseconds()
	now := model.NowMillis()
	for _, n := range m.nodes {
		if n.Status == "online" && now-n.LastSeen > threshold {
			n.Status = "offline"
		}
	}
}

// ListNodes 返回节点快照列表。
func (m *Manager) ListNodes() []model.Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, *n)
	}
	return out
}

// GetNode 返回单个节点。
func (m *Manager) GetNode(name string) (model.Node, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[name]
	if !ok {
		return model.Node{}, false
	}
	return *n, true
}

// SetNodeGroup 修改节点所属分组。
func (m *Manager) SetNodeGroup(name, group string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[name]
	if !ok {
		return os.ErrNotExist
	}
	n.Group = group
	m.persistLocked()
	return nil
}

// SetNodeDisplayName 设置节点的自定义显示名（别名）。空字符串表示清除别名，恢复使用真实主机名。
// 该值仅由用户在 UI 设置，Agent 上报时不会被覆盖（Register 不会更新此字段）。
func (m *Manager) SetNodeDisplayName(name, displayName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[name]
	if !ok {
		return os.ErrNotExist
	}
	n.DisplayName = displayName
	m.persistLocked()
	return nil
}

// RemoveNode 移除节点。
func (m *Manager) RemoveNode(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, name)
	m.persistLocked()
}

// RequestUpgrade 标记某节点待升级（由前端 API 触发）。
// targetVersion 为期望 agent 升级到的版本（server 当前版本）。
func (m *Manager) RequestUpgrade(name, targetVersion string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upgradeQueue[name] = &upgradeTask{
		targetVersion: targetVersion,
		createdAt:     time.Now(),
	}
}

// ConsumeUpgrade 检查节点是否需要升级指令。
// 持续下发逻辑：
//   - 节点在升级队列中且 agent 上报版本 != 目标版本 -> 下发 upgrade，保留任务
//   - agent 上报版本 == 目标版本 -> 升级成功，移除任务
//   - 任务超时（10 分钟）-> 放弃，移除任务
//   - 节点不在队列中 -> 不下发
func (m *Manager) ConsumeUpgrade(name, agentVersion string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.upgradeQueue[name]
	if !ok {
		return false
	}
	// 超时清理（10 分钟）
	if time.Since(task.createdAt) > 10*time.Minute {
		slog.Warn("升级任务超时，移除", "node", name, "target", task.targetVersion, "agent", agentVersion)
		delete(m.upgradeQueue, name)
		return false
	}
	// 版本匹配 -> 升级成功
	if agentVersion != "" && agentVersion == task.targetVersion {
		slog.Info("agent 升级成功", "node", name, "version", agentVersion)
		delete(m.upgradeQueue, name)
		return false
	}
	// 仍需升级，累加计数后下发指令
	task.retries++
	return true
}

// ListGroups 返回分组列表。
func (m *Manager) ListGroups() []model.Group {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Group, 0, len(m.groups))
	for _, g := range m.groups {
		out = append(out, *g)
	}
	return out
}

// AddGroup 新增分组。
func (m *Manager) AddGroup(name, desc string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.groups[name]; ok {
		return
	}
	m.groups[name] = &model.Group{Name: name, Description: desc, CreatedAt: model.NowMillis()}
	m.persistLocked()
}

// RemoveGroup 删除分组。
func (m *Manager) RemoveGroup(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, name)
	m.persistLocked()
}

// load 从 meta 文件加载。
func (m *Manager) load() {
	data, err := os.ReadFile(m.metaPath)
	if err != nil {
		return
	}
	var mf metaFile
	if err := json.Unmarshal(data, &mf); err != nil {
		slog.Warn("解析节点 meta 文件失败，忽略", "path", m.metaPath, "err", err)
		return
	}
	for i := range mf.Nodes {
		n := mf.Nodes[i]
		m.nodes[n.Hostname] = &n
	}
	for i := range mf.Groups {
		g := mf.Groups[i]
		m.groups[g.Name] = &g
	}
}

// persist 持久化到 meta 文件。
func (m *Manager) persist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistLocked()
}

// persistLocked 必须在持有写锁时调用。
func (m *Manager) persistLocked() {
	mf := metaFile{}
	for _, n := range m.nodes {
		mf.Nodes = append(mf.Nodes, *n)
	}
	for _, g := range m.groups {
		mf.Groups = append(mf.Groups, *g)
	}
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		slog.Warn("序列化节点 meta 失败", "err", err)
		return
	}
	if err := os.MkdirAll(dirOf(m.metaPath), 0o755); err != nil {
		slog.Warn("创建 meta 目录失败", "err", err)
		return
	}
	if err := os.WriteFile(m.metaPath, data, 0o644); err != nil {
		slog.Warn("写入节点 meta 文件失败", "err", err, "path", m.metaPath)
	}
}

// dirOf 返回文件路径的目录部分。
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
