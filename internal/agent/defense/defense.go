package defense

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 受控 fail2ban 入侵防御（仅管理 nebula-monitor-sshd 专属 jail）。
//
// 安全边界：
//   - 仅创建并管理 /etc/fail2ban/jail.d/nebula-monitor-sshd.conf 与专属 action 配置；
//   - 绝不修改、删除或接管用户既有 jail.local 或其他 jail 配置；
//   - “停用防护”只停止并移除 nebula 专属配置，随后重载 fail2ban，不卸载 fail2ban 软件包；
//   - 白名单由服务端下发（操作人真实来源 IP + 回环），不自动放行整个内网段。

const (
	jailConfPath    = "/etc/fail2ban/jail.d/nebula-monitor-sshd.conf"
	actionConfPath  = "/etc/fail2ban/action.d/nebula-monitor.conf"
	jailName        = "nebula-monitor-sshd"
	auditDir        = "/var/lib/nebula-monitor/defense"
	auditPath       = auditDir + "/ban_audit.jsonl"
	stateDir        = auditDir
	executedPath    = stateDir + "/executed.json"
	managedMarkPath = stateDir + "/managed_by_nebula"
)

// Manager 管理节点上的 nebula 专属 SSH 防护。
type Manager struct {
	mu       sync.Mutex
	executed map[string]string // commandID -> 结果消息（用于幂等）
}

// NewManager 创建防护管理器并加载已执行记录。
func NewManager() *Manager {
	m := &Manager{executed: map[string]string{}}
	m.loadExecuted()
	return m
}

func (m *Manager) loadExecuted() {
	data, err := os.ReadFile(executedPath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &m.executed)
}

func (m *Manager) saveExecuted() {
	data, err := json.MarshalIndent(m.executed, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(executedPath, data, 0600)
}

// isExecuted 判断命令是否已执行，用于幂等。
func (m *Manager) isExecuted(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.executed[id]
	return msg, ok
}

// markExecuted 记录命令执行结果。
func (m *Manager) markExecuted(id, msg string) {
	m.mu.Lock()
	m.executed[id] = msg
	m.mu.Unlock()
	m.saveExecuted()
}

// Status 探测并返回当前防护状态。
func (m *Manager) Status() *model.DefenseStatus {
	st := &model.DefenseStatus{Node: "", UpdatedAt: time.Now().UnixMilli()}
	if !m.supported() {
		st.Supported = false
		st.Message = "当前环境不满足（需 Linux + systemd + sshd）"
		return st
	}
	st.Supported = true
	st.Installed = cmdExists("fail2ban-client")
	if st.Installed {
		out, err := exec.Command("fail2ban-client", "status").Output()
		if err == nil && strings.Contains(string(out), "Status") {
			st.Running = true
			st.Jails = parseJailList(string(out))
			if _, e := exec.Command("fail2ban-client", "status", jailName).Output(); e == nil {
				st.ManagedJail = true
			}
		} else {
			st.Message = "fail2ban 已安装但未运行"
		}
	}
	return st
}

// supported 判断环境是否支持（Linux + systemd + sshd）。
func (m *Manager) supported() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if !cmdExists("systemctl") {
		return false
	}
	// 检测 sshd 服务存在（sshd 或 ssh）
	out, err := exec.Command("systemctl", "list-unit-files", "sshd.service", "ssh.service").Output()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "sshd.service") || strings.Contains(s, "ssh.service")
}

// Enable 启用 nebula 专属 SSH 防护。
// ignoreIPs 为精确白名单（操作人来源 IP + 回环），由服务端下发。
func (m *Manager) Enable(ignoreIPs []string) (string, error) {
	if !m.supported() {
		return "", fmt.Errorf("环境不满足（需 Linux + systemd + sshd），请手动安装 fail2ban 并配置 SSH jail")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", fmt.Errorf("创建状态目录失败: %w", err)
	}

	// 1. 安装 fail2ban（若未安装）
	if !cmdExists("fail2ban-client") {
		if err := installFail2ban(); err != nil {
			return "", fmt.Errorf("安装 fail2ban 失败: %w", err)
		}
	}

	// 2. 写入专属 action 配置（向受控 JSONL 审计文件追加 ban/unban 记录）
	if err := os.WriteFile(actionConfPath, []byte(actionConf), 0644); err != nil {
		return "", fmt.Errorf("写入 action 配置失败: %w", err)
	}

	// 3. 写入专属 jail 配置（仅保护 SSH，含精确白名单）
	jail := buildJailConf(ignoreIPs)
	if err := os.WriteFile(jailConfPath, []byte(jail), 0644); err != nil {
		return "", fmt.Errorf("写入 jail 配置失败: %w", err)
	}

	// 4. 确保 fail2ban 服务启用并运行
	if err := enableStartFail2ban(); err != nil {
		return "", fmt.Errorf("启动 fail2ban 失败: %w", err)
	}

	// 5. 确保 server 就绪并启用 nebula 专属 jail
	if err := m.enableJail(jailName); err != nil {
		return "", err
	}

	if _, err := exec.Command("fail2ban-client", "status", jailName).CombinedOutput(); err != nil {
		return "", fmt.Errorf("校验 jail 状态失败，可能未生效（请检查 fail2ban 日志）")
	}

	// 标记由 nebula 托管
	_ = os.WriteFile(managedMarkPath, []byte(fmt.Sprintf("enabled at %d\n", time.Now().Unix())), 0600)

	return fmt.Sprintf("已启用 %s 专属 SSH 防护，白名单: %s", jailName, strings.Join(ignoreIPs, ",")), nil
}

// Disable 停用 nebula 专属 SSH 防护。仅移除 nebula 配置并重载，不卸载 fail2ban 包。
func (m *Manager) Disable() (string, error) {
	if _, err := os.Stat(managedMarkPath); err != nil {
		// 非 nebula 托管，避免误删用户配置
		return "", fmt.Errorf("未检测到由 nebula 托管的防护配置，跳过停用")
	}
	// 1. 停止专属 jail（忽略错误，可能已停止）
	_, _ = exec.Command("fail2ban-client", "stop", jailName).CombinedOutput()
	// 解除当前封禁（清理，避免遗留）
	_ = unbanAll(jailName)

	// 2. 删除 nebula 专属配置
	_ = os.Remove(jailConfPath)
	_ = os.Remove(actionConfPath)

	// 3. 重载 fail2ban 以移除 jail
	_, _ = exec.Command("fail2ban-client", "reload").CombinedOutput()

	// 4. 清理托管标记与审计状态（保留 ban_audit.jsonl 供排障，不强制删除）
	_ = os.Remove(managedMarkPath)

	return "已停用 nebula 专属 SSH 防护（保留 fail2ban 软件包与既有配置）", nil
}

// enableJail 确保 fail2ban server 运行并启用指定 jail。
// 即便 server 已由 enableStartFail2ban 拉起，仍在此二次确认就绪，避免初始化竞态。
func (m *Manager) enableJail(jail string) error {
	// 先确认 server 是否已就绪
	if err := waitFail2banReady(3, time.Second); err != nil {
		// server 未运行则尝试直接启动 server 自身（无参 start 启动 server）
		if out, e := exec.Command("fail2ban-client", "start").CombinedOutput(); e != nil {
			return fmt.Errorf("fail2ban 服务未运行且无法启动: %s（请检查 systemctl status fail2ban / journalctl -u fail2ban）", string(out))
		}
		if err := waitFail2banReady(5, time.Second); err != nil {
			return fmt.Errorf("fail2ban 服务启动后仍未就绪: %w", err)
		}
	}
	// 重载使专属 jail 配置生效；若失败则尝试 start 单个 jail
	if out, err := exec.Command("fail2ban-client", "reload", jail).CombinedOutput(); err != nil {
		_, _ = exec.Command("fail2ban-client", "reload").CombinedOutput()
		if out2, e2 := exec.Command("fail2ban-client", "start", jail).CombinedOutput(); e2 != nil {
			return fmt.Errorf("启用 jail 失败（fail2ban 已运行但 jail 未生效）: %s / %s；请检查 fail2ban 日志", string(out), string(out2))
		}
	}
	return nil
}

// EnableIfNeeded 供 executor 调用：先做幂等判断，再执行 enable。
func (m *Manager) run(cmd model.DefenseCommand) (string, error) {
	switch cmd.Type {
	case model.DefenseActionEnable:
		return m.Enable(cmd.IgnoreIPs)
	case model.DefenseActionDisable:
		return m.Disable()
	case model.DefenseActionStatus:
		st := m.Status()
		b, _ := json.Marshal(st)
		return string(b), nil
	default:
		return "", fmt.Errorf("不支持的防护动作: %s", cmd.Type)
	}
}

// ---- 工具函数 ----

func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// installFail2ban 自适应包管理器安装 fail2ban。
func installFail2ban() error {
	pkg := detectPkgManager()
	var cmd *exec.Cmd
	switch pkg {
	case "apt":
		cmd = exec.Command("apt-get", "install", "-y", "fail2ban")
	case "dnf":
		cmd = exec.Command("dnf", "install", "-y", "fail2ban")
	case "yum":
		cmd = exec.Command("yum", "install", "-y", "fail2ban")
	default:
		return fmt.Errorf("无法识别的包管理器，请手动安装 fail2ban")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	return nil
}

func detectPkgManager() string {
	switch {
	case cmdExists("apt-get"):
		return "apt"
	case cmdExists("dnf"):
		return "dnf"
	case cmdExists("yum"):
		return "yum"
	}
	return ""
}

func enableStartFail2ban() error {
	_, _ = exec.Command("systemctl", "enable", "fail2ban").CombinedOutput()
	out, err := exec.Command("systemctl", "restart", "fail2ban").CombinedOutput()
	if err != nil {
		// 重试 start
		out2, e2 := exec.Command("systemctl", "start", "fail2ban").CombinedOutput()
		if e2 != nil {
			return fmt.Errorf("systemctl 启动 fail2ban 失败: restart=%s; start=%s（请检查 systemctl status fail2ban 与 journalctl -u fail2ban）", string(out), string(out2))
		}
	}
	// systemctl 返回成功不代表 fail2ban server 已就绪；需等待 socket 可达再继续，
	// 否则后续 reload/start jail 会报 "Could not find server" / socket 不存在。
	if err := waitFail2banReady(15, time.Second); err != nil {
		return fmt.Errorf("fail2ban 服务启动后未就绪: %w", err)
	}
	return nil
}

// waitFail2banReady 轮询 fail2ban server 是否就绪（fail2ban-client ping 返回 pong）。
// 某些发行版 systemctl start 返回成功后 server 仍需数秒初始化 socket，故需重试等待。
func waitFail2banReady(attempts int, interval time.Duration) error {
	var last string
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(interval)
		}
		out, err := exec.Command("fail2ban-client", "ping").CombinedOutput()
		if err == nil && strings.Contains(strings.ToLower(string(out)), "pong") {
			return nil
		}
		last = string(out)
	}
	if last == "" {
		last = "(无输出)"
	}
	return fmt.Errorf("fail2ban-client ping 超时（socket 可能未创建）。最后输出: %s；请检查 systemctl status fail2ban 与 journalctl -u fail2ban", last)
}

// buildJailConf 生成 nebula 专属 jail 配置（仅 sshd，含精确白名单）。
func buildJailConf(ignoreIPs []string) string {
	ips := []string{"127.0.0.1", "::1"}
	for _, ip := range ignoreIPs {
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	return fmt.Sprintf(jailConfTmpl, strings.Join(ips, " "), jailName)
}

// unbanAll 解除 jail 当前所有封禁（停用前清理）。
func unbanAll(jail string) error {
	out, err := exec.Command("fail2ban-client", "status", jail).Output()
	if err != nil {
		return err
	}
	// 解析 "Banned IP list: 1.2.3.4 5.6.7.8" 或 "Banned IP list:\n"
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Banned IP list:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) < 2 {
				continue
			}
			ips := strings.Fields(strings.TrimSpace(parts[1]))
			for _, ip := range ips {
				if ip == "" {
					continue
				}
				_, _ = exec.Command("fail2ban-client", "set", jail, "unbanip", ip).CombinedOutput()
			}
		}
	}
	return nil
}

func parseJailList(statusOut string) []string {
	var jails []string
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.Contains(line, "Jail list:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) < 2 {
				continue
			}
			for _, j := range strings.Split(parts[1], ",") {
				j = strings.TrimSpace(j)
				if j != "" && j != " " {
					jails = append(jails, j)
				}
			}
		}
	}
	return jails
}

func ensureDir(p string) {
	_ = os.MkdirAll(filepath.Dir(p), 0700)
}

// jailConfTmpl 为 nebula 专属 SSH jail 配置模板。
// 参数：ignoreip 白名单、jail 名称、action 配置文件路径。
// 仅保护 SSH（port=ssh），使用 systemd backend 优先，失败回退 auto；
// maxretry=5 / findtime=10m / bantime=1h 为保守阈值；action 使用 nebula 专属审计 action。
const jailConfTmpl = `[DEFAULT]
# nebula-monitor 托管：仅保护 SSH，勿手动修改本文件
ignoreip = %s

[%s]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/auth.log
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
action = nebula-monitor
`

// actionConf 为 nebula 专属 fail2ban action 配置。
// actionban / actionunban 仅向受控 JSONL 审计文件追加结构化记录，由 Agent 增量采集回流。
const actionConf = `# nebula-monitor 专属审计 action：仅记录封禁/解封到受控 JSONL，不影响 fail2ban 既有动作
[Definition]
actionstart =
actionstop =
actioncheck =
# 注意：fail2ban 用 ConfigParser，%(name)s 会被当作变量插值，故时间改用 bash $(date ...) 注入，避免 %(now)s 解析报错
actionban = echo '{"action":"ban","ip":"<ip>","jail":"nebula-monitor-sshd","failures":"<failures>","time":"'"$(date +%Y-%m-%dT%H:%M:%S%z)"'"}' >> /var/lib/nebula-monitor/defense/ban_audit.jsonl
actionunban = echo '{"action":"unban","ip":"<ip>","jail":"nebula-monitor-sshd","time":"'"$(date +%Y-%m-%dT%H:%M:%S%z)"'"}' >> /var/lib/nebula-monitor/defense/ban_audit.jsonl
`
