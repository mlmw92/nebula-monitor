package model

// 安全事件与基线检查相关模型，随 ReportPayload 上报，由 Server 端结构化存储与告警。
// 注意：敏感文件（如 /etc/shadow）仅上传 SHA256 哈希，绝不上传文件内容或口令。

// 安全事件类别常量。
const (
	// SecurityCatSSHBruteforce SSH 暴力破解检测。
	SecurityCatSSHBruteforce = "ssh_bruteforce"
	// SecurityCatSSHAudit SSH 登录审计（成功/失败来源 IP）。
	SecurityCatSSHAudit = "ssh_audit"
	// SecurityCatFIM 文件完整性监测（新增/修改/删除）。
	SecurityCatFIM = "fim"
	// SecurityCatProcessAnomaly 异常进程 / 反弹 shell 检测。
	SecurityCatProcessAnomaly = "process_anomaly"
	// SecurityCatSudoAudit sudo 提权与命令审计。
	SecurityCatSudoAudit = "sudo_audit"
)

// SecurityEvent 单条安全事件，由 Agent 采集并随上报体提交，Server 端持久化与告警。
type SecurityEvent struct {
	ID        string            `json:"id"`                  // 事件唯一 ID（node|category|hash）
	Node      string            `json:"node"`                // 节点名
	NodeIP    string            `json:"nodeIp,omitempty"`    // 节点 IP（服务器 IP）
	Category  string            `json:"category"`            // 类别：见 SecurityCat* 常量
	Severity  Severity          `json:"severity"`            // 严重级别
	Message   string            `json:"message"`             // 人类可读描述
	Detail    map[string]string `json:"detail,omitempty"`    // 结构化细节（不含敏感内容）
	SourceIP  string            `json:"sourceIp,omitempty"`  // 来源 IP（SSH/sudo 场景）
	User      string            `json:"user,omitempty"`      // 关联账户（不含口令）
	Timestamp int64             `json:"timestamp"`           // 事件时间（毫秒）
}

// SecurityBaselineItem 单项基线检查结果。
type SecurityBaselineItem struct {
	Key      string  `json:"key"`             // 检查项标识（如 ssh_root_login）
	Name     string  `json:"name"`            // 检查项名称（中文）
	Pass     bool    `json:"pass"`            // 是否通过
	Severity Severity `json:"severity"`       // 未通过时的严重级别
	Weight   float64 `json:"weight"`          // 该项在总评分中的权重
	Score    float64 `json:"score"`           // 该项得分（0 或 weight）
	Detail   string  `json:"detail,omitempty"` // 检查说明（可选）
}

// SecurityBaseline 主机安全基线检查结果，含 0-100 合规评分与逐项明细。
type SecurityBaseline struct {
	Node      string                 `json:"node"`      // 节点名
	NodeIP    string                 `json:"nodeIp,omitempty"` // 节点 IP（服务器 IP）
	Score     float64                `json:"score"`     // 合规评分 0-100
	Items     []SecurityBaselineItem `json:"items"`     // 各项检查结果
	CheckedAt int64                  `json:"checkedAt"` // 检查时间（毫秒）
}
