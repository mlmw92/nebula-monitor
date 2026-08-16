package defense

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// jailConfPath nebula 专属 fail2ban jail 配置文件路径。
	jailConfPath = "/etc/fail2ban/jail.d/nebula-monitor-sshd.conf"
	// actionConfPath nebula 专属 fail2ban action 配置文件路径。
	actionConfPath       = "/etc/fail2ban/action.d/nebula-monitor-audit.conf"
	legacyActionConfPath = "/etc/fail2ban/action.d/nebula-monitor.conf"
	// jailName nebula 专属 jail 名称。
	jailName = "nebula-monitor-sshd"
	// auditDir 防护审计数据根目录。
	auditDir = "/var/lib/nebula-monitor/defense"
	// auditPath 封禁审计流水文件路径（JSONL）。
	auditPath = auditDir + "/ban_audit.jsonl"
	// stateDir 防护状态目录（与 auditDir 相同）。
	stateDir = auditDir
	// executedPath 已执行指令记录文件路径，用于幂等。
	executedPath = stateDir + "/executed.json"
	// managedMarkPath 标记目录由 nebula 管理的标记文件路径。
	managedMarkPath = stateDir + "/managed_by_nebula"
	// firewallActionPath 记录启用时选定的原生防火墙 action。
	firewallActionPath = stateDir + "/firewall_action"
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
	if !st.Installed {
		st.Message = "未安装 fail2ban"
		return st
	}
	out, err := exec.Command("fail2ban-client", "status").Output()
	if err != nil || !strings.Contains(string(out), "Status") {
		st.Message = "fail2ban 已安装但未运行"
		return st
	}
	st.Running = true
	st.Jails = parseJailList(string(out))
	jailOut, err := exec.Command("fail2ban-client", "status", jailName).CombinedOutput()
	if err != nil {
		st.Message = "fail2ban 正在运行，但 nebula 专属 jail 未启用"
		return st
	}
	st.ManagedJail = true
	st.BannedIPs = parseBannedIPs(string(jailOut))
	st.FirewallAction = configuredFirewallAction()
	if st.FirewallAction != "" && isNativeFirewallAction(st.FirewallAction) {
		// get ... actions 是 Fail2Ban 官方查询接口；只有能看到原生 action
		// 才把状态标记为已验证，避免把 audit-only jail 显示成已防护。
		if actionOut, actionErr := exec.Command("fail2ban-client", "get", jailName, "actions").CombinedOutput(); actionErr == nil && strings.Contains(string(actionOut), st.FirewallAction) {
			st.FirewallVerified = true
		}
	}
	if !st.FirewallVerified {
		st.Message = "专属 jail 已运行，但未确认实际防火墙 action；当前仅视为审计/未验证"
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

	// 2. 选择发行版提供的真实封禁 action；没有可用后端时拒绝启用，不能退回审计-only。
	firewallAction, err := selectFirewallAction()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(firewallActionPath, []byte(firewallAction+"\n"), 0600); err != nil {
		return "", fmt.Errorf("记录防火墙 action 失败: %w", err)
	}
	// 3. 写入专属审计 action 配置（仅记录 ban/unban，实际封禁由原生 action 执行）。
	if err := os.WriteFile(actionConfPath, []byte(actionConf), 0644); err != nil {
		return "", fmt.Errorf("写入 action 配置失败: %w", err)
	}

	// 4. 写入专属 jail 配置（仅保护 SSH，含精确白名单）。
	jail, err := buildJailConf(ignoreIPs, firewallAction)
	if err != nil {
		return "", fmt.Errorf("生成 jail 配置失败: %w", err)
	}
	if err := os.WriteFile(jailConfPath, []byte(jail), 0644); err != nil {
		return "", fmt.Errorf("写入 jail 配置失败: %w", err)
	}

	// 5. 确保 fail2ban 服务启用并运行。
	if err := enableStartFail2ban(); err != nil {
		return "", fmt.Errorf("启动 fail2ban 失败: %w", err)
	}

	// 6. 确保 server 就绪并启用 nebula 专属 jail
	if err := m.enableJail(jailName); err != nil {
		return "", err
	}
	if err := validateJailAction(firewallAction); err != nil {
		return "", err
	}

	// 标记由 nebula 托管
	if err := os.WriteFile(managedMarkPath, []byte(fmt.Sprintf("enabled at %d\n", time.Now().Unix())), 0600); err != nil {
		return "", fmt.Errorf("写入托管标记失败: %w", err)
	}

	return fmt.Sprintf("已启用 %s 专属 SSH 防护，防火墙 action: %s，白名单: %s", jailName, firewallAction, strings.Join(ignoreIPs, ",")), nil
}

// Disable 停用 nebula 专属 SSH 防护。仅移除 nebula 配置并重载，不卸载 fail2ban 包。
func (m *Manager) Disable() (string, error) {
	if _, err := os.Stat(managedMarkPath); err != nil {
		return "", fmt.Errorf("未检测到由 nebula 托管的防护配置，跳过停用")
	}
	if out, err := exec.Command("fail2ban-client", "stop", jailName).CombinedOutput(); err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return "", fmt.Errorf("停止 jail 失败: %s", strings.TrimSpace(string(out)))
	}
	if err := unbanAll(jailName); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
		return "", fmt.Errorf("清理封禁失败: %w", err)
	}
	for _, path := range []string{jailConfPath, actionConfPath, legacyActionConfPath, firewallActionPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("删除托管配置 %s 失败: %w", path, err)
		}
	}
	if out, err := exec.Command("fail2ban-client", "reload").CombinedOutput(); err != nil {
		return "", fmt.Errorf("重载 fail2ban 失败: %s", strings.TrimSpace(string(out)))
	}
	if err := os.Remove(managedMarkPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("清理托管标记失败: %w", err)
	}
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

// installFail2ban 自适应包管理器安装 fail2ban，并尽量附带 systemd 后端依赖。
// Debian 系 fail2ban 仅 Recommends python3-systemd，缺失时 systemd 后端不可用，
// 会导致 jail 报 “Have not found any log file”；因此显式安装该依赖以确保 journal 可用。
// 依赖包不可用时仅告警（核心 fail2ban 已安装，探测逻辑会回退到文件日志后端）。
func installFail2ban() error {
	pkg := detectPkgManager()
	var baseArgs []string
	var dep string
	switch pkg {
	case "apt":
		baseArgs = []string{"apt-get", "install", "-y", "fail2ban"}
		dep = "python3-systemd"
	case "dnf":
		baseArgs = []string{"dnf", "install", "-y", "fail2ban"}
		dep = "systemd-python"
	case "yum":
		baseArgs = []string{"yum", "install", "-y", "fail2ban"}
		dep = "systemd-python"
	default:
		return fmt.Errorf("无法识别的包管理器，请手动安装 fail2ban")
	}
	out, err := exec.Command(baseArgs[0], baseArgs[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	// 额外尝试安装 systemd 后端依赖；缺失时不影响核心安装。
	if dep != "" {
		if dout, derr := exec.Command(baseArgs[0], append([]string{"install", "-y"}, dep)...).CombinedOutput(); derr != nil {
			log.Printf("[defense] 安装 %s 失败（systemd 后端可能不可用，将回退文件日志）：%v %s", dep, derr, string(dout))
		}
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
// 自动探测：SSH 实际监听端口、SSH 服务单元、可用的认证日志后端。
//   - 端口：默认 22 不可靠（本机可能使用非标准端口），必须自动探测，否则封禁会落到错误端口；
//   - 日志后端：必须真实可用，否则 fail2ban 会报 “Have not found any log file for sshd jail”。
//
// 探测不到任何可用日志来源时返回错误，避免写入无法生效的 jail 配置。
func buildJailConf(ignoreIPs []string, firewallAction string) (string, error) {
	ips := []string{"127.0.0.1", "::1"}
	for _, ip := range ignoreIPs {
		if ip != "" {
			ips = append(ips, ip)
		}
	}

	// 1) SSH 实际端口（逗号分隔；回退 "ssh" 即 22）
	portCfg := strings.Join(detectSSHPorts(), ",")

	// 2) 认证日志后端：必须真实可用
	logCfg, err := detectLogConfig()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(jailConfTmpl, strings.Join(ips, " "), jailName, portCfg, logCfg, firewallAction), nil
}

// detectLogConfig 选择可用的 SSH 认证日志后端，返回 jail 的日志配置段落。
// 优先使用 systemd journal（需 python3-systemd 可由 fail2ban 导入），其次使用
// 真实存在的认证日志文件（/var/log/auth.log 或 /var/log/secure）；两者皆不可用时
// 返回明确错误，避免写入无法生效的 jail 配置。
func detectLogConfig() (string, error) {
	if systemdBackendAvailable() {
		unit := detectSSHUnit()
		match := "_SYSTEMD_UNIT=sshd.service"
		if unit != "" && unit != "sshd.service" {
			match = "_SYSTEMD_UNIT=" + unit
		}
		return "backend = systemd\njournalmatch = " + match, nil
	}
	if p := firstExistingFile("/var/log/auth.log", "/var/log/secure"); p != "" {
		return fmt.Sprintf("logpath = %s\nbackend = auto", p), nil
	}
	return "", fmt.Errorf("未找到可用的 SSH 认证日志来源：既无法使用 systemd journal（缺失 python3-systemd），也不存在 /var/log/auth.log 或 /var/log/secure。请先执行 `apt-get install -y python3-systemd`（Debian/Ubuntu）或 `dnf install -y systemd-python`（RHEL/CentOS），也可确保认证日志已写入文件后再重试")
}

// detectSSHPorts 探测本机 sshd 实际监听端口。
// 探测链路：sshd -T 生效配置 → 解析 /etc/ssh/sshd_config → 监听套接字反查；
// 均失败则回退 ["ssh"]（fail2ban 解析为 22）。返回端口字符串列表（可含多个）。
func detectSSHPorts() []string {
	if ports := parseSSHDEffectivePorts(sshdDumpConfig()); len(ports) > 0 {
		return ports
	}
	if ports := parseSSHDPortsFromConfig(readFileOrEmpty("/etc/ssh/sshd_config")); len(ports) > 0 {
		return ports
	}
	if ports := sshdListeningPorts(); len(ports) > 0 {
		return ports
	}
	return []string{"ssh"}
}

// sshdDumpConfig 通过 `sshd -T` 获取生效配置全文（失败返回空串）。
func sshdDumpConfig() string {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		return ""
	}
	out, err := exec.Command(sshd, "-T").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// parseSSHDEffectivePorts 解析 `sshd -T` 输出中的 port 行（可一行多端口）。
func parseSSHDEffectivePorts(cfg string) []string {
	var ports []string
	seen := map[string]bool{}
	for _, line := range strings.Split(cfg, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "port ") {
			continue
		}
		for _, p := range strings.Fields(t)[1:] {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return ports
}

// parseSSHDPortsFromConfig 解析 sshd_config 文本中的 Port 指令（支持多行多值）。
func parseSSHDPortsFromConfig(cfg string) []string {
	var ports []string
	seen := map[string]bool{}
	for _, line := range strings.Split(cfg, "\n") {
		t := strings.TrimSpace(line)
		if !(strings.HasPrefix(t, "Port ") || strings.HasPrefix(t, "port ")) {
			continue
		}
		for _, p := range strings.Fields(t)[1:] {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return ports
}

// sshdListeningPorts 通过 ss/netstat 反查 sshd 监听端口（兜底探测）。
func sshdListeningPorts() []string {
	for _, c := range [][]string{{"ss", "-tlnp"}, {"netstat", "-tlnp"}} {
		if !cmdExists(c[0]) {
			continue
		}
		out, err := exec.Command(c[0], c[1:]...).Output()
		if err != nil {
			continue
		}
		if ports := parseListenPortsFor(string(out), "sshd"); len(ports) > 0 {
			return ports
		}
	}
	return nil
}

// parseListenPortsFor 从 ss/netstat 输出中解析指定进程名的监听端口。
func parseListenPortsFor(out, proc string) []string {
	var ports []string
	seen := map[string]bool{}
	re := regexp.MustCompile(`:(\d+)\s`)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, proc) {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			p := m[1]
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return ports
}

// detectSSHUnit 返回实际运行（或存在）的 sshd systemd 单元名，用于 journalmatch。
// 优先选“已激活”的单元，否则回退到存在的单元；皆无则返回空串。
func detectSSHUnit() string {
	out, err := exec.Command("systemctl", "list-unit-files", "sshd.service", "ssh.service").Output()
	if err != nil {
		return ""
	}
	s := string(out)
	hasSshd := strings.Contains(s, "sshd.service")
	hasSsh := strings.Contains(s, "ssh.service")
	for _, u := range []string{"sshd.service", "ssh.service"} {
		if st, e := exec.Command("systemctl", "is-active", u).Output(); e == nil && strings.TrimSpace(string(st)) == "active" {
			return u
		}
	}
	if hasSshd {
		return "sshd.service"
	}
	if hasSsh {
		return "ssh.service"
	}
	return ""
}

// firstExistingFile 返回首个存在的普通文件路径（用于认证日志探测）。
func firstExistingFile(paths ...string) string {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// readFileOrEmpty 读取文件内容，失败返回空串（用于配置解析兜底）。
func readFileOrEmpty(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(data)
}

// systemdBackendAvailable 检查 fail2ban 能否使用 systemd 后端（依赖 python3-systemd）。
// Debian 系 fail2ban 仅 Recommends python3-systemd，缺失时 systemd 后端不可用，
// 会导致 jail 报 “Have not found any log file”。这里实测能否导入 systemd.journal。
func systemdBackendAvailable() bool {
	if !cmdExists("journalctl") {
		return false
	}
	cands := []string{}
	if p := fail2banPython(); p != "" {
		cands = append(cands, p)
	}
	for _, p := range []string{"python3", "python"} {
		if p != "" && !strSliceContains(cands, p) {
			cands = append(cands, p)
		}
	}
	for _, py := range cands {
		if _, err := exec.Command(py, "-c", "import systemd.journal").CombinedOutput(); err == nil {
			return true
		}
	}
	return false
}

// fail2banPython 返回 fail2ban-client 脚本使用的 Python 解释器（解析 shebang）。
func fail2banPython() string {
	path, err := exec.LookPath("fail2ban-client")
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "#!") {
		return ""
	}
	fields := strings.Fields(lines[0][2:])
	if len(fields) == 0 {
		return ""
	}
	if filepath.Base(fields[0]) == "env" && len(fields) > 1 {
		return fields[1]
	}
	return fields[0]
}

// strSliceContains 判断字符串切片是否包含指定值。
func strSliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// selectFirewallAction 选择 Fail2Ban 自带的实际封禁 action。
// 不提供 audit-only 回退：无法找到原生 action 时必须拒绝启用。
func selectFirewallAction() (string, error) {
	candidates := []struct {
		name string
		path string
		cmd  string
	}{
		{"firewallcmd-ipset", "/etc/fail2ban/action.d/firewallcmd-ipset.conf", "firewall-cmd"},
		{"firewallcmd-rich-rules", "/etc/fail2ban/action.d/firewallcmd-rich-rules.conf", "firewall-cmd"},
		{"nftables-multiport", "/etc/fail2ban/action.d/nftables-multiport.conf", "nft"},
		{"iptables-multiport", "/etc/fail2ban/action.d/iptables-multiport.conf", "iptables"},
	}
	firewalldActive := false
	if cmdExists("firewall-cmd") {
		if out, err := exec.Command("firewall-cmd", "--state").Output(); err == nil && strings.TrimSpace(string(out)) == "running" {
			firewalldActive = true
		}
	}
	for i, c := range candidates {
		if i < 2 && !firewalldActive {
			continue
		}
		if _, err := os.Stat(c.path); err == nil && cmdExists(c.cmd) {
			return c.name, nil
		}
	}
	return "", fmt.Errorf("未找到可用的 Fail2Ban 原生防火墙 action（需要 firewalld、nftables 或 iptables；不会退回仅审计模式）")
}

func isNativeFirewallAction(action string) bool {
	switch action {
	case "firewallcmd-ipset", "firewallcmd-rich-rules", "nftables-multiport", "iptables-multiport":
		return true
	default:
		return false
	}
}

func configuredFirewallAction() string {
	data, err := os.ReadFile(firewallActionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func validateJailAction(firewallAction string) error {
	out, err := exec.Command("fail2ban-client", "get", jailName, "actions").CombinedOutput()
	if err != nil {
		return fmt.Errorf("校验 jail action 失败: %s", strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), firewallAction) {
		return fmt.Errorf("jail 已加载但未发现原生防火墙 action %q: %s", firewallAction, strings.TrimSpace(string(out)))
	}
	return nil
}

// parseBannedIPs 解析 fail2ban-client status <jail> 的封禁 IP 列表。
// 兼容空列表、列表换行以及 IPv4/IPv6 地址。
func parseBannedIPs(statusOut string) []string {
	lines := strings.Split(statusOut, "\n")
	var ips []string
	inList := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Banned IP list:") {
			inList = true
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				ips = appendValidIPs(ips, strings.Fields(parts[1]))
			}
			continue
		}
		if inList {
			if trimmed == "" || strings.HasPrefix(trimmed, "Number of") || strings.Contains(trimmed, ":") && strings.Contains(trimmed, "Currently") {
				break
			}
			ips = appendValidIPs(ips, strings.Fields(trimmed))
		}
	}
	return ips
}

func appendValidIPs(dst []string, values []string) []string {
	for _, value := range values {
		if net.ParseIP(value) != nil {
			dst = append(dst, value)
		}
	}
	return dst
}

// unbanAll 解除 jail 当前所有封禁（停用前清理）。
func unbanAll(jail string) error {
	out, err := exec.Command("fail2ban-client", "status", jail).Output()
	if err != nil {
		return err
	}
	for _, ip := range parseBannedIPs(string(out)) {
		if _, err := exec.Command("fail2ban-client", "set", jail, "unbanip", ip).CombinedOutput(); err != nil {
			return fmt.Errorf("解除 IP %s 封禁失败: %w", ip, err)
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
// 参数：ignoreip 白名单、jail 名称、实际 SSH 端口、日志配置、原生防火墙 action。
const jailConfTmpl = `[DEFAULT]
# nebula-monitor 托管：仅保护 SSH，勿手动修改本文件
ignoreip = %s

[%s]
enabled = true
port = %s
filter = sshd
%s
maxretry = 5
findtime = 10m
bantime = 1h
action = %s
         nebula-monitor-audit
`

// actionConf 为 nebula 专属 fail2ban 审计 action 配置。
// 实际封禁由 jail 中的发行版原生 action 执行；该 action 只记录结构化事件。
const actionConf = `# nebula-monitor 审计 action：记录封禁/解封，不承担实际防火墙封禁
[Definition]
actionstart =
actionstop =
actioncheck =
# fail2ban ConfigParser 要求 shell date 的百分号写成 %%。
actionban = echo '{"action":"ban","ip":"<ip>","jail":"nebula-monitor-sshd","failures":"<failures>","time":"'"$(date +%%Y-%%m-%%dT%%H:%%M:%%S%%z)"'"}' >> /var/lib/nebula-monitor/defense/ban_audit.jsonl
actionunban = echo '{"action":"unban","ip":"<ip>","jail":"nebula-monitor-sshd","time":"'"$(date +%%Y-%%m-%%dT%%H:%%M:%%S%%z)"'"}' >> /var/lib/nebula-monitor/defense/ban_audit.jsonl
`
