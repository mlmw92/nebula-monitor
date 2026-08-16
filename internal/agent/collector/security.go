package collector

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// 安全采集相关常量。
const (
	// securityMaxEvents 单周期最多上报的安全事件数，防止洪峰拖垮上报链路。
	securityMaxEvents = 200
	// securityMaxBytes 单日志文件单周期最多解析的字节数。
	securityMaxBytes = 4 << 20
	// securityFIMDefaultBaseline 默认 FIM 基线文件名（部署目录下）。
	securityFIMDefaultBaseline = "fim_baseline.json"
)

// SSH 失败登录日志正则（兼容 syslog 风格 auth.log / secure）。
// 示例：
//   Failed password for root from 1.2.3.4 port 5678 ssh2
//   Failed password for invalid user admin from 1.2.3.4 port 5678 ssh2
//   Connection closed by authenticating user root 1.2.3.4 port 5678 [preauth]
//   Invalid user admin from 1.2.3.4 port 5678
var (
	// reSSHFailed 匹配 SSH 登录失败（含无效用户），捕获用户名与来源 IP。
	reSSHFailed      = regexp.MustCompile(`Failed password for (?:invalid user )?(\S+) from (\S+) port (\d+)`)
	// reSSHInvalidUser 匹配 "Invalid user" 探测，捕获被猜解的用户名与来源 IP。
	reSSHInvalidUser = regexp.MustCompile(`Invalid user (\S+) from (\S+)`)
	// reSSHClosedBy 匹配认证阶段被关闭的连接，捕获用户名与来源 IP。
	reSSHClosedBy    = regexp.MustCompile(`Connection closed by authenticating user (\S+) (\S+) port (\d+)`)
	// reSSHAccepted 匹配 SSH 登录成功，捕获用户名与来源 IP。
	reSSHAccepted    = regexp.MustCompile(`Accepted password for (\S+) from (\S+) port (\d+)`)
	// reSSHRootLogin 匹配 root 登录被拒绝/接受（不区分大小写）。
	reSSHRootLogin   = regexp.MustCompile(`(?i)root login (?:refused|accepted)`)
	// sudo 审计：记录提权用户、执行的命令与来源 IP（如 sudo -u）。
	// 示例：sudo:   ops : TTY=... ; PWD=... ; USER=root ; COMMAND=/bin/ls /etc
	reSudo = regexp.MustCompile(`sudo:\s+(\S+)\s+:\s+.*USER=(\S+)\s+;\s+COMMAND=(.*)$`)
)

// SecurityCollector 采集主机安全事件与基线检查结果。
// 设计原则：敏感文件（/etc/shadow）仅计算 SHA256 哈希，绝不上传文件内容或口令；
// SSH 日志增量解析（记录文件偏移），避免重复解析全量日志。
type SecurityCollector struct {
	node   string
	nodeIP string
	cfg    config.SecurityConfig
	mu     sync.Mutex
	fim    *fimState            // FIM 基线状态（含本地偏移/哈希）
	sshOff map[string]int64    // 各 SSH 日志文件的读取偏移
}

// fimState 文件完整性监测的本地状态，含基线哈希与文件路径。
type fimState struct {
	BaselinePath string             `json:"baselinePath"`
	Hashes       map[string]string  `json:"hashes"`    // path -> sha256
	LastChecked  int64              `json:"lastChecked"`
}

// NewSecurityCollector 创建安全采集器并加载本地 FIM 基线（若存在）。
func NewSecurityCollector(node, nodeIP string, cfg config.SecurityConfig) *SecurityCollector {
	if cfg.BruteForceThreshold <= 0 {
		cfg.BruteForceThreshold = 5
	}
	if cfg.BruteForceWindowSec <= 0 {
		cfg.BruteForceWindowSec = 300
	}
	c := &SecurityCollector{
		node:    node,
		nodeIP:  nodeIP,
		cfg:     cfg,
		sshOff:  map[string]int64{},
	}
	c.loadFIMBaseline()
	return c
}

// loadFIMBaseline 加载本地 FIM 基线；不存在则等待首次采集建立。
func (c *SecurityCollector) loadFIMBaseline() {
	path := c.cfg.FIMBaselinePath
	if path == "" {
		path = securityFIMDefaultBaseline
	}
	st := &fimState{BaselinePath: path, Hashes: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, st)
		if st.Hashes == nil {
			st.Hashes = map[string]string{}
		}
		slog.Info("已加载 FIM 基线", "path", path, "files", len(st.Hashes))
	} else {
		slog.Info("FIM 基线未找到，首次采集将建立基线（不告警）", "path", path)
	}
	c.fim = st
}

// saveFIMBaseline 持久化 FIM 基线到本地文件。
func (c *SecurityCollector) saveFIMBaseline() {
	if c.fim == nil {
		return
	}
	c.fim.LastChecked = time.Now().UnixMilli()
	data, err := json.MarshalIndent(c.fim, "", "  ")
	if err != nil {
		slog.Warn("FIM 基线序列化失败", "err", err)
		return
	}
	if err := os.WriteFile(c.fim.BaselinePath, data, 0600); err != nil {
		slog.Warn("FIM 基线写入失败", "path", c.fim.BaselinePath, "err", err)
	}
}

// Collect 采集安全事件与基线检查结果。
func (c *SecurityCollector) Collect() ([]model.SecurityEvent, *model.SecurityBaseline) {
	var events []model.SecurityEvent

	// 1) SSH 登录审计 + 暴力破解
	events = append(events, c.collectSSH()...)

	// 2) FIM 文件完整性监测
	events = append(events, c.collectFIM()...)

	// 3) sudo 审计
	events = append(events, c.collectSudo()...)

	// 4) 异常进程 / 反弹 shell
	events = append(events, c.collectProcessAnomalies()...)

	// 5) 安全基线检查
	baseline := c.collectBaseline()

	// 限流：单周期最多上报 securityMaxEvents 条事件，避免洪峰。
	if len(events) > securityMaxEvents {
		slog.Warn("安全事件超过单周期上限，截断上报", "total", len(events), "cap", securityMaxEvents)
		events = events[:securityMaxEvents]
	}
	return events, baseline
}

// collectSSH 增量解析 SSH 日志，统计失败来源 IP，检测暴力破解。
func (c *SecurityCollector) collectSSH() []model.SecurityEvent {
	paths := c.sshLogPaths()
	var events []model.SecurityEvent
	now := time.Now()

	// 窗口内失败尝试按来源 IP 聚合：ip -> []失败时间戳
	failByIP := map[string][]int64{}

	for _, path := range paths {
		offset, _ := c.sshOff[path]
		f, err := os.Open(path)
		if err != nil {
			// 该日志文件可能不存在（非对应发行版），仅调试级别，不刷屏
			slog.Debug("SSH 日志打开失败，跳过", "path", path, "err", err)
			continue
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		if st.Size() < offset {
			offset = 0 // 日志被轮转，从头解析
		}
		if _, err := f.Seek(offset, 0); err != nil {
			_ = f.Close()
			continue
		}
		r := bufio.NewReader(f)
		var readBytes int64
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				readBytes += int64(len(line))
				if readBytes > securityMaxBytes {
					break
				}
				line = strings.TrimRight(line, "\r\n")
				ts := parseSyslogTime(line, now)
				if m := reSSHFailed.FindStringSubmatch(line); m != nil {
					ip := m[2]
					failByIP[ip] = append(failByIP[ip], ts)
					// 失败登录审计事件（信息级别，便于追溯攻击来源）
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSSHAudit, model.SeverityInfo,
						fmt.Sprintf("SSH 登录失败：用户 %s 来自 %s", m[1], ip),
						map[string]string{"user": m[1], "result": "failed"}, ip, "", ts))
				} else if m := reSSHInvalidUser.FindStringSubmatch(line); m != nil {
					failByIP[m[2]] = append(failByIP[m[2]], ts)
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSSHAudit, model.SeverityInfo,
						fmt.Sprintf("SSH 登录失败：无效用户 %s 来自 %s", m[1], m[2]),
						map[string]string{"user": m[1], "result": "invalid"}, m[2], "", ts))
				} else if m := reSSHClosedBy.FindStringSubmatch(line); m != nil {
					failByIP[m[2]] = append(failByIP[m[2]], ts)
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSSHAudit, model.SeverityInfo,
						fmt.Sprintf("SSH 认证中断：用户 %s 来自 %s", m[1], m[2]),
						map[string]string{"user": m[1], "result": "closed"}, m[2], "", ts))
				} else if m := reSSHAccepted.FindStringSubmatch(line); m != nil {
					// 成功登录也记录审计（不告警），便于排查暴力破解后的入侵
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSSHAudit, model.SeverityInfo,
						fmt.Sprintf("SSH 登录成功：用户 %s 来自 %s", m[1], m[2]),
						map[string]string{"user": m[1], "result": "success"}, m[2], m[1], ts))
				}
			}
			if err != nil {
				break
			}
		}
		offset += readBytes
		c.mu.Lock()
		c.sshOff[path] = offset
		c.mu.Unlock()
		_ = f.Close()
	}

	// 暴力破解检测：窗口内失败次数超阈值
	for ip, tsList := range failByIP {
		// 仅统计窗口内的失败次数
		window := time.Duration(c.cfg.BruteForceWindowSec) * time.Second
		cutoff := now.Add(-window).UnixMilli()
		cnt := 0
		for _, t := range tsList {
			if t >= cutoff {
				cnt++
			}
		}
		if cnt >= c.cfg.BruteForceThreshold {
			events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSSHBruteforce, model.SeverityCritical,
				fmt.Sprintf("检测到 SSH 暴力破解：来源 %s 在 %d 秒内失败 %d 次", ip, c.cfg.BruteForceWindowSec, cnt),
				map[string]string{"failCount": strconv.Itoa(cnt), "windowSec": strconv.Itoa(c.cfg.BruteForceWindowSec)},
				ip, "", now.UnixMilli()))
		}
	}
	return events
}

// collectFIM 计算关键文件 SHA256，与基线比对，产出新增/修改/删除事件，并更新基线。
func (c *SecurityCollector) collectFIM() []model.SecurityEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fim == nil {
		return nil
	}
	var events []model.SecurityEvent
	now := time.Now().UnixMilli()
	prev := c.fim.Hashes
	newHashes := map[string]string{}

	for _, path := range c.cfg.FIMPaths {
		sum, err := sha256File(path)
		if err != nil {
			if os.IsNotExist(err) {
				// 文件被删除
				if _, existed := prev[path]; existed {
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatFIM, model.SeverityWarning,
						fmt.Sprintf("受监测文件被删除：%s", path),
						map[string]string{"action": "deleted", "path": path}, "", "", now))
				}
			}
			// 无读权限等：记录但继续
			continue
		}
		newHashes[path] = sum
		if old, ok := prev[path]; ok {
			if old != sum {
				events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatFIM, model.SeverityWarning,
					fmt.Sprintf("受监测文件被修改：%s（哈希 %s → %s）", path, shortHash(old), shortHash(sum)),
					map[string]string{"action": "modified", "path": path, "oldHash": old, "newHash": sum}, "", "", now))
			}
		} else {
			// 新文件（基线中不存在）
			if len(prev) > 0 {
				events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatFIM, model.SeverityWarning,
					fmt.Sprintf("发现新增受监测文件：%s", path),
					map[string]string{"action": "added", "path": path, "hash": sum}, "", "", now))
			}
		}
	}

	// 更新基线（仅当已建立过基线，避免首次运行产生基线建立噪声）
	if len(prev) > 0 {
		c.fim.Hashes = newHashes
		c.saveFIMBaseline()
	} else {
		// 首次建立基线，不告警
		c.fim.Hashes = newHashes
		c.saveFIMBaseline()
	}
	return events
}

// collectSudo 解析 sudo 审计记录，记录提权用户、目标用户与命令（不含凭据）。
func (c *SecurityCollector) collectSudo() []model.SecurityEvent {
	paths := c.sshLogPaths()
	var events []model.SecurityEvent
	now := time.Now()
	for _, path := range paths {
		offset, _ := c.sshOff[path]
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		if st.Size() < offset {
			offset = 0
		}
		if _, err := f.Seek(offset, 0); err != nil {
			_ = f.Close()
			continue
		}
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				line = strings.TrimRight(line, "\r\n")
				if m := reSudo.FindStringSubmatch(line); m != nil {
					// m[1]=执行者 m[2]=目标用户 m[3]=命令
					events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatSudoAudit, model.SeverityInfo,
						fmt.Sprintf("sudo 提权：%s 以 %s 身份执行命令", m[1], m[2]),
						map[string]string{"targetUser": m[2], "command": m[3]}, "", m[1], parseSyslogTime(line, now)))
				}
			}
			if err != nil {
				break
			}
		}
		_ = f.Close()
	}
	return events
}

// collectProcessAnomalies 枚举进程命令行，匹配挖矿/反弹 shell 等异常特征。
func (c *SecurityCollector) collectProcessAnomalies() []model.SecurityEvent {
	var events []model.SecurityEvent
	now := time.Now().UnixMilli()
	// 反弹 shell / 可疑外联特征
	reversePatterns := []string{"/dev/tcp/", "nc -e", "ncat -e", "bash -i", "socat ", "python -c", "perl -e", "mkfifo"}
	// 常见矿池/挖矿关键字（命令或进程名）
	minerPatterns := []string{"minerd", "xmrig", "cgminer", "cpuminer", "stratum+tcp", "ethminer", "claymore"}

	procs := enumProcesses()
	for _, p := range procs {
		cmd := p.cmdline
		lcmd := strings.ToLower(cmd)
		// 反弹 shell 特征（需多个关键字组合，避免误报常见脚本）
		for _, pat := range reversePatterns {
			if strings.Contains(lcmd, pat) {
				events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatProcessAnomaly, model.SeverityCritical,
					fmt.Sprintf("检测到可疑反弹 shell 特征：进程 %s (PID %d) 命令行含 %q", p.name, p.pid, pat),
					map[string]string{"pid": strconv.Itoa(p.pid), "name": p.name, "cmdline": cmd, "feature": pat}, "", "", now))
				break
			}
		}
		for _, pat := range minerPatterns {
			if strings.Contains(lcmd, pat) {
				events = append(events, mkEvent(c.node, c.nodeIP, model.SecurityCatProcessAnomaly, model.SeverityCritical,
					fmt.Sprintf("检测到疑似挖矿进程：%s (PID %d)", p.name, p.pid),
					map[string]string{"pid": strconv.Itoa(p.pid), "name": p.name, "cmdline": cmd, "feature": pat}, "", "", now))
				break
			}
		}
	}
	return events
}

// collectBaseline 执行安全基线检查，输出 0-100 合规评分与逐项结果。
func (c *SecurityCollector) collectBaseline() *model.SecurityBaseline {
	now := time.Now().UnixMilli()
	var items []model.SecurityBaselineItem

	// 1) SSH root 登录
	passRoot, detailRoot, sevRoot := c.checkSSHRootLogin()
	items = append(items, mkBaselineItem("ssh_root_login", "SSH root 登录已禁用", passRoot, sevRoot, detailRoot))

	// 2) SSH 密码认证
	passPwd, detailPwd, sevPwd := c.checkSSHPasswordAuth()
	items = append(items, mkBaselineItem("ssh_password_auth", "SSH 密码认证已禁用（建议密钥）", passPwd, sevPwd, detailPwd))

	// 3) 防火墙启用
	passFw, detailFw, sevFw := c.checkFirewall()
	items = append(items, mkBaselineItem("firewall_enabled", "防火墙已启用", passFw, sevFw, detailFw))

	// 4) fail2ban 运行
	passF2b, detailF2b, sevF2b := c.checkFail2ban()
	items = append(items, mkBaselineItem("fail2ban_running", "fail2ban 入侵防御运行中", passF2b, sevF2b, detailF2b))

	// 5) 空口令账户（需 root 权限，默认开启）
	if c.cfg.WeakPasswordCheck {
		passWeak, detailWeak, sevWeak := c.checkEmptyPassword()
		items = append(items, mkBaselineItem("no_empty_password", "无空口令账户", passWeak, sevWeak, detailWeak))
	}

	// 计算加权评分（通过项得权重分，未通过得 0）
	var totalWeight, gotScore float64
	for _, it := range items {
		totalWeight += it.Weight
		gotScore += it.Score
	}
	score := 100.0
	if totalWeight > 0 {
		score = gotScore / totalWeight * 100
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return &model.SecurityBaseline{
		Node:      c.node,
		NodeIP:    c.nodeIP,
		Score:     score,
		Items:     items,
		CheckedAt: now,
	}
}

// ---- 基线检查辅助函数 ----

// readSSHDConfig 读取并解析 sshd_config 的键值对（忽略注释与空白）。
func readSSHDConfig() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile("/etc/ssh/sshd_config")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			out[strings.ToLower(parts[0])] = strings.Join(parts[1:], " ")
		}
	}
	return out
}

// checkSSHRootLogin 检查 PermitRootLogin 是否为 prohibit-password/no。
func (c *SecurityCollector) checkSSHRootLogin() (pass bool, detail string, sev model.Severity) {
	cfg := readSSHDConfig()
	v, ok := cfg["permitrootlogin"]
	if !ok {
		return true, "未显式配置 PermitRootLogin（默认允许），建议设为 prohibit-password 或 no", model.SeverityWarning
	}
	switch strings.ToLower(v) {
	case "no", "prohibit-password", "forced-commands-only":
		return true, "PermitRootLogin=" + v, ""
	default:
		return false, "PermitRootLogin=" + v + "（允许 root 直接登录，存在风险）", model.SeverityWarning
	}
}

// checkSSHPasswordAuth 检查 PasswordAuthentication 是否为 no。
func (c *SecurityCollector) checkSSHPasswordAuth() (pass bool, detail string, sev model.Severity) {
	cfg := readSSHDConfig()
	v, ok := cfg["passwordauthentication"]
	if !ok {
		return false, "未显式配置 PasswordAuthentication（默认允许密码登录），建议设为 no", model.SeverityWarning
	}
	if strings.EqualFold(v, "no") {
		return true, "PasswordAuthentication=no", ""
	}
	return false, "PasswordAuthentication=" + v + "（允许密码登录，建议改为密钥登录）", model.SeverityWarning
}

// checkFirewall 探测防火墙是否启用（ufw/firewalld/iptables 任一活跃即可）。
func (c *SecurityCollector) checkFirewall() (pass bool, detail string, sev model.Severity) {
	if cmdExists("ufw") {
		if out, err := exec.Command("ufw", "status").Output(); err == nil {
			s := string(out)
			if strings.Contains(s, "Status: active") {
				return true, "ufw 已启用", ""
			}
			return false, "ufw 已安装但未启用", model.SeverityWarning
		}
	}
	if cmdExists("firewall-cmd") {
		if out, err := exec.Command("firewall-cmd", "--state").Output(); err == nil && strings.TrimSpace(string(out)) == "running" {
			return true, "firewalld 运行中", ""
		}
		return false, "firewalld 未运行", model.SeverityWarning
	}
	if cmdExists("iptables") {
		if out, err := exec.Command("iptables", "-L", "-n").Output(); err == nil {
			s := string(out)
			// 存在非空的默认策略或规则（非全 ACCEPT 且无规则）视为已配置
			if !strings.Contains(s, "Chain INPUT (policy ACCEPT)") || strings.Count(s, "ACCEPT") != strings.Count(s, "Chain") {
				return true, "iptables 已配置规则", ""
			}
			return false, "iptables 无有效防护规则（INPUT 全 ACCEPT）", model.SeverityWarning
		}
	}
	return false, "未检测到 ufw/firewalld/iptables 防火墙", model.SeverityWarning
}

// checkFail2ban 探测 fail2ban 服务是否运行。
func (c *SecurityCollector) checkFail2ban() (pass bool, detail string, sev model.Severity) {
	if cmdExists("fail2ban-client") {
		if out, err := exec.Command("fail2ban-client", "status").Output(); err == nil {
			if strings.Contains(string(out), "Status") {
				return true, "fail2ban 运行中", ""
			}
		}
		return false, "fail2ban 已安装但未运行", model.SeverityWarning
	}
	return false, "未安装 fail2ban（建议安装以防御暴力破解）", model.SeverityInfo
}

// checkEmptyPassword 检查 /etc/shadow 中是否存在空口令账户（仅 root 可读）。
// 重要：仅统计是否存在空口令，不上传任何口令或哈希内容。
func (c *SecurityCollector) checkEmptyPassword() (pass bool, detail string, sev model.Severity) {
	data, err := os.ReadFile("/etc/shadow")
	if err != nil {
		// 无权限（非 root）时跳过检查，不误报
		return true, "无 /etc/shadow 读取权限，跳过空口令检查", model.SeverityInfo
	}
	empty := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 {
			continue
		}
		// 第二个字段为空或 "!" / "*" 表示锁定/无密码
		pw := fields[1]
		if pw == "" {
			empty++
		}
	}
	if empty > 0 {
		return false, fmt.Sprintf("发现 %d 个空口令账户", empty), model.SeverityCritical
	}
	return true, "未发现空口令账户", ""
}

// ---- 工具函数 ----

// sshLogPaths 返回需监控的 SSH 日志路径：优先使用配置，否则自动探测。
func (c *SecurityCollector) sshLogPaths() []string {
	if len(c.cfg.SSHLogPaths) > 0 {
		return c.cfg.SSHLogPaths
	}
	var paths []string
	for _, p := range []string{"/var/log/auth.log", "/var/log/secure"} {
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

// mkEvent 构造一条安全事件并生成稳定 ID。
func mkEvent(node, nodeIP, category string, sev model.Severity, msg string, detail map[string]string, srcIP, user string, ts int64) model.SecurityEvent {
	id := fmt.Sprintf("%s|%s|%s", node, category, hashString(node+category+msg+srcIP))
	if sev == "" {
		sev = model.SeverityInfo
	}
	return model.SecurityEvent{
		ID:        id,
		Node:      node,
		NodeIP:    nodeIP,
		Category:  category,
		Severity:  sev,
		Message:   msg,
		Detail:    detail,
		SourceIP:  srcIP,
		User:      user,
		Timestamp: ts,
	}
}

// mkBaselineItem 构造基线检查项，权重默认 1.0，通过得分=权重。
func mkBaselineItem(key, name string, pass bool, sev model.Severity, detail string) model.SecurityBaselineItem {
	weight := 1.0
	score := 0.0
	if pass {
		score = weight
	}
	if sev == "" {
		sev = model.SeverityInfo
	}
	return model.SecurityBaselineItem{
		Key:      key,
		Name:     name,
		Pass:     pass,
		Severity: sev,
		Weight:   weight,
		Score:    score,
		Detail:   detail,
	}
}

// sha256File 计算文件 SHA256（仅哈希，不上传内容）。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// shortHash 截断哈希用于可读展示。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// hashString 对字符串做 SHA256 并返回短前缀（用于事件 ID 去重）。
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// cmdExists 判断命令是否在 PATH 中。
func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// parseSyslogTime 尝试从日志行解析时间戳；失败回退到 now。
func parseSyslogTime(line string, now time.Time) int64 {
	// 常见格式：Mon DD HH:MM:SS 或 2024-01-02T15:04:05
	formats := []string{
		"Jan _2 15:04:05",
		"Jan 2 15:04:05",
		"2006-01-02T15:04:05.999999-07:00",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, extractSyslogPrefix(line)); err == nil {
			year := t.Year()
			if year == 0 {
				year = now.Year()
			}
			return time.Date(year, t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local).UnixMilli()
		}
	}
	return now.UnixMilli()
}

// extractSyslogPrefix 截取日志行开头的时间部分（去除前导进程字段后尝试）。
func extractSyslogPrefix(line string) string {
	// 典型： "Jan  5 10:20:30 host sshd[123]: ..."
	fields := strings.Fields(line)
	if len(fields) >= 3 {
		return fields[0] + " " + fields[1] + " " + fields[2]
	}
	return line
}

// procInfo 进程基本信息。
type procInfo struct {
	pid    int
	name   string
	cmdline string
}

// enumProcesses 枚举当前系统进程（Linux /proc）。
// 非 Linux 平台返回空列表，避免采集器崩溃。
func enumProcesses() []procInfo {
	var out []procInfo
	if runtime.GOOS != "linux" {
		return out
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		name := ""
		if comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")); err == nil {
			name = strings.TrimSpace(string(comm))
		}
		// cmdline 以 \0 分隔，转为空格
		cl := strings.ReplaceAll(string(cmdline), "\x00", " ")
		cl = strings.TrimSpace(cl)
		if cl == "" {
			cl = name
		}
		out = append(out, procInfo{pid: pid, name: name, cmdline: cl})
	}
	return out
}
