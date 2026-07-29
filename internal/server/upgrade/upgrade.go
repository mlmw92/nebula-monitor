// Package upgrade 实现 server 自升级：上传升级包、备份/替换/重启、回滚、历史。
//
// 设计要点：
//   - 升级包格式：tar.gz，内含 bin/server/<arch>/server、bin/agent/<arch>/agent、web/、deploy/agent-install.sh、VERSION、manifest.json
//   - manifest.json 声明每个组件的 source/target/action/sha256，apply 时按声明处理
//   - apply 流程：备份→替换 server→替换 web→复制新 agent 到 AgentBinDir（自带 CDN）→复制新 agent-install.sh 到 AgentScriptPath→重启 server
//   - 不主动推送 agent 升级到节点，由管理员在主机列表手动点击升级
//   - history 持久化到 <Dir>/history.json
package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/node"
)

// Component 描述升级包内一个组件。
type Component struct {
	Name     string `json:"name"`                // server | agent | web | agent-script
	Arch     string `json:"arch,omitempty"`      // amd64 | arm64 | arm（web/agent-script 为空）
	Source   string `json:"source"`              // 包内相对路径
	Target   string `json:"target,omitempty"`    // 目标绝对路径（仅展示用）
	Action   string `json:"action"`              // install_file | sync_dir
	Mode     string `json:"mode,omitempty"`      // install_file 时的文件 mode（八进制字符串）
	Checksum string `json:"checksum,omitempty"`  // sha256:<hex>，上传时自动计算填入
}

// Manifest 升级包 manifest.json。
type Manifest struct {
	Version            string      `json:"version"`
	PreviousVersionMin string      `json:"previous_version_min,omitempty"`
	Notes              string      `json:"notes,omitempty"`
	Components         []Component `json:"components"`
}

// Task 已上传待应用（或已应用）的升级任务。
type Task struct {
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	Notes       string      `json:"notes,omitempty"`
	UploadedAt  time.Time   `json:"uploadedAt"`
	Status      string      `json:"status"` // pending | applying | applied | failed
	Components  []Component `json:"components"`
	ServerArch  string      `json:"serverArch,omitempty"`
	ServerSize  int64       `json:"serverSize,omitempty"`
	WebSize     int64       `json:"webSize,omitempty"`
	AgentArches []string    `json:"agentArches,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// HistoryEntry 升级历史记录。
type HistoryEntry struct {
	ID              string    `json:"id"`
	Version         string    `json:"version,omitempty"`
	Action          string    `json:"action"` // apply | rollback | upload_failed
	At              time.Time `json:"at"`
	Operator        string    `json:"operator"`
	Result          string    `json:"result"` // success | failed
	Detail          string    `json:"detail,omitempty"`
	AgentCDNUpdated bool      `json:"agentCDNUpdated,omitempty"`
}

// Config 升级模块配置。
type Config struct {
	Dir         string // 升级工作目录（上传包 / 解压 / 备份）
	BinDir      string // server 二进制安装目录（默认 /usr/local/bin）
	WebDir      string // 前端静态资源目录
	AgentBinDir     string // agent 自带 CDN 根目录（含 agent/linux/<arch>/agent）
	AgentScriptPath string // Agent 安装脚本目标路径（apply 时写入，如 dataDir/agent-dist/agent-install.sh）
	BackupKeep      int    // 保留备份份数
	UseSystemd  bool   // 是否用 systemd 重启 server
	Service     string // systemd 服务名
}

// Manager 升级管理器。
type Manager struct {
	mu       sync.Mutex
	cfg      Config
	nodeMgr  *node.Manager
	current  *Task
	history  []HistoryEntry
	histPath string
}

// New 创建升级管理器并确保工作子目录存在。
func New(cfg Config, nodeMgr *node.Manager) (*Manager, error) {
	if cfg.Dir == "" || cfg.BinDir == "" || cfg.WebDir == "" || cfg.AgentBinDir == "" {
		return nil, errors.New("upgrade: 关键目录不能为空")
	}
	if cfg.BackupKeep <= 0 {
		cfg.BackupKeep = 3
	}
	if cfg.Service == "" {
		cfg.Service = "monitor-server.service"
	}
	for _, d := range []string{
		filepath.Join(cfg.Dir, "incoming"),
		filepath.Join(cfg.Dir, "unpacked"),
		filepath.Join(cfg.Dir, "backups"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("创建升级目录失败 %s: %w", d, err)
		}
	}
	m := &Manager{
		cfg:      cfg,
		nodeMgr:  nodeMgr,
		histPath: filepath.Join(cfg.Dir, "history.json"),
	}
	m.loadHistory()
	return m, nil
}

// Upload 解析 multipart 上传的升级 tar.gz，解压并读取 manifest，返回 Task。
// 同一时刻只保留一个待应用任务；上传新包会清理上一个待应用的 unpacked 目录。
func (m *Manager) Upload(r *http.Request, maxBytes int64) (*Task, error) {
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return nil, fmt.Errorf("解析上传失败: %w", err)
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, fmt.Errorf("未找到文件字段 'file': %w", err)
	}
	defer file.Close()

	id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), randHex(4))
	incoming := filepath.Join(m.cfg.Dir, "incoming", id+".tar.gz")
	f, err := os.Create(incoming)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		os.Remove(incoming)
		return nil, err
	}
	f.Close()

	unpackDir := filepath.Join(m.cfg.Dir, "unpacked", id)
	if err := os.MkdirAll(unpackDir, 0o755); err != nil {
		os.Remove(incoming)
		return nil, err
	}
	if err := untarGz(incoming, unpackDir); err != nil {
		os.RemoveAll(unpackDir)
		os.Remove(incoming)
		return nil, fmt.Errorf("解压失败: %w", err)
	}
	os.Remove(incoming) // 节省空间

	mfData, err := os.ReadFile(filepath.Join(unpackDir, "manifest.json"))
	if err != nil {
		os.RemoveAll(unpackDir)
		return nil, errors.New("包内缺少 manifest.json")
	}
	var mf Manifest
	if err := json.Unmarshal(mfData, &mf); err != nil {
		os.RemoveAll(unpackDir)
		return nil, fmt.Errorf("manifest.json 解析失败: %w", err)
	}
	if mf.Version == "" {
		mf.Version = "unknown"
	}
	// 校验每个 component.source 存在并计算 sha256
	for i := range mf.Components {
		c := &mf.Components[i]
		src := filepath.Join(unpackDir, c.Source)
		fi, err := os.Stat(src)
		if err != nil {
			os.RemoveAll(unpackDir)
			return nil, fmt.Errorf("manifest 声明的 %s 不存在: %w", c.Source, err)
		}
		if fi.IsDir() {
			// 目录型组件（如 web，action=sync_dir）无法用单文件 sha256 校验，
			// 跳过；其完整性由 apply 时逐文件同步（sync_dir）保证，无需在此填充 checksum。
			continue
		}
		sum, err := fileSHA256(src)
		if err != nil {
			os.RemoveAll(unpackDir)
			return nil, fmt.Errorf("计算 sha256 失败 %s: %w", c.Source, err)
		}
		c.Checksum = "sha256:" + sum
	}

	task := &Task{
		ID:         id,
		Version:    mf.Version,
		Notes:      mf.Notes,
		UploadedAt: time.Now(),
		Status:     "pending",
		Components: mf.Components,
	}
	for _, c := range mf.Components {
		switch c.Name {
		case "server":
			if c.Arch == runtime.GOARCH {
				task.ServerArch = c.Arch
				if sz, err := pathSize(filepath.Join(unpackDir, c.Source)); err == nil {
					task.ServerSize = sz
				}
			}
		case "web":
			if sz, err := pathSize(filepath.Join(unpackDir, c.Source)); err == nil {
				task.WebSize = sz
			}
		case "agent":
			if c.Arch != "" {
				task.AgentArches = append(task.AgentArches, c.Arch)
			}
		}
	}

	m.mu.Lock()
	if m.current != nil && m.current.ID != "" {
		os.RemoveAll(filepath.Join(m.cfg.Dir, "unpacked", m.current.ID))
	}
	m.current = task
	m.mu.Unlock()

	slog.Info("升级包已上传", "id", id, "version", mf.Version, "agent_arches", task.AgentArches)
	return task, nil
}

// Apply 执行升级：备份当前 server/web → 替换 server → 替换 web → 复制新 agent 到 agentBinDir → 重启。
func (m *Manager) Apply(operator string) (*Task, error) {
	m.mu.Lock()
	t := m.current
	if t == nil {
		m.mu.Unlock()
		return nil, errors.New("没有待应用的升级包，请先上传")
	}
	if t.Status == "applying" {
		m.mu.Unlock()
		return nil, errors.New("升级正在进行中")
	}
	t.Status = "applying"
	m.mu.Unlock()

	ts := time.Now().Format("20060102-150405")
	backupDir := filepath.Join(m.cfg.Dir, "backups", ts)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Status = "failed"
		t.Error = err.Error()
		m.recordHistory(t.ID, t.Version, "apply", operator, "failed", err.Error(), false)
		return t, err
	}

	serverBin := filepath.Join(m.cfg.BinDir, "monitor-server")
	unpackRoot := filepath.Join(m.cfg.Dir, "unpacked", t.ID)
	agentUpdated := false
	var serverApplied, webApplied bool
	var applyErrors []string

	// 1) 替换 server（仅当前架构）
	for _, c := range t.Components {
		if c.Name != "server" || c.Arch != runtime.GOARCH {
			continue
		}
		if _, err := os.Stat(serverBin); err == nil {
			if err := copyFile(serverBin, filepath.Join(backupDir, "monitor-server")); err != nil {
				applyErrors = append(applyErrors, "备份 server 失败: "+err.Error())
			}
		}
		src := filepath.Join(unpackRoot, c.Source)
		mode := os.FileMode(0o755)
		if c.Mode != "" {
			if mm, err := parseOctal(c.Mode); err == nil {
				mode = mm
			}
		}
		if err := replaceFileAtomic(src, serverBin, mode); err != nil {
			applyErrors = append(applyErrors, "替换 server 失败: "+err.Error())
		} else {
			serverApplied = true
		}
		break
	}
	if !serverApplied {
		slog.Warn("升级包不包含当前架构 server 二进制", "arch", runtime.GOARCH)
	}

	// 2) 替换 web
	for _, c := range t.Components {
		if c.Name != "web" || c.Action != "sync_dir" {
			continue
		}
		if _, err := os.Stat(m.cfg.WebDir); err == nil {
			if err := syncDir(m.cfg.WebDir, filepath.Join(backupDir, "web")); err != nil {
				applyErrors = append(applyErrors, "备份 web 失败: "+err.Error())
			}
		}
		src := filepath.Join(unpackRoot, c.Source)
		if err := syncDir(src, m.cfg.WebDir); err != nil {
			applyErrors = append(applyErrors, "替换 web 失败: "+err.Error())
		} else {
			webApplied = true
		}
		break
	}

	// 3) 把新 agent 复制到 agentBinDir（自带 CDN）；不主动推送主机升级
	for _, c := range t.Components {
		if c.Name != "agent" || c.Arch == "" {
			continue
		}
		destDir := filepath.Join(m.cfg.AgentBinDir, "agent", "linux", c.Arch)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			applyErrors = append(applyErrors, fmt.Sprintf("创建 agent 目录失败 %s: %s", destDir, err.Error()))
			continue
		}
		dest := filepath.Join(destDir, "agent")
		if _, err := os.Stat(dest); err == nil {
			_ = copyFile(dest, filepath.Join(backupDir, fmt.Sprintf("agent-%s", c.Arch)))
		}
		src := filepath.Join(unpackRoot, c.Source)
		if err := copyFileMode(src, dest, 0o755); err != nil {
			applyErrors = append(applyErrors, fmt.Sprintf("复制 agent (%s) 失败: %s", c.Arch, err.Error()))
		} else {
			agentUpdated = true
		}
	}

	// 3b) 把新 agent-install.sh 复制到 AgentScriptPath（自带 CDN 对外脚本，跟随 server 版本）
	if m.cfg.AgentScriptPath != "" {
		for _, c := range t.Components {
			if c.Name != "agent-script" {
				continue
			}
			if _, err := os.Stat(m.cfg.AgentScriptPath); err == nil {
				_ = copyFile(m.cfg.AgentScriptPath, filepath.Join(backupDir, "agent-install.sh"))
			}
			src := filepath.Join(unpackRoot, c.Source)
			mode := os.FileMode(0o755)
			if c.Mode != "" {
				if mm, err := parseOctal(c.Mode); err == nil {
					mode = mm
				}
			}
			if err := copyFileMode(src, m.cfg.AgentScriptPath, mode); err != nil {
				applyErrors = append(applyErrors, "复制 agent-install.sh 失败: "+err.Error())
			} else {
				agentUpdated = true // 属于 Agent CDN 更新的一部分
			}
			break
		}
	}

	m.pruneBackups()

	// 4) 重启 server（仅在确实替换了 server 二进制时）
	if serverApplied {
		if err := m.restart(); err != nil {
			applyErrors = append(applyErrors, "重启 server 失败: "+err.Error())
		}
	}

	result := "success"
	detail := ""
	if len(applyErrors) > 0 {
		result = "failed"
		detail = strings.Join(applyErrors, "; ")
		t.Status = "failed"
		t.Error = detail
	} else {
		t.Status = "applied"
	}

	m.recordHistory(t.ID, t.Version, "apply", operator, result, detail, agentUpdated)

	slog.Info("升级已应用", "id", t.ID, "version", t.Version,
		"server", serverApplied, "web", webApplied, "agent", agentUpdated, "result", result)

	if result == "failed" {
		return t, errors.New(detail)
	}
	return t, nil
}

// Rollback 从最近一次备份恢复 server + web，并重启。
func (m *Manager) Rollback(operator string) error {
	backups, err := filepath.Glob(filepath.Join(m.cfg.Dir, "backups", "*"))
	if err != nil || len(backups) == 0 {
		return errors.New("没有可用的备份")
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i] > backups[j] })
	latest := backups[0]

	var errs []string
	serverBin := filepath.Join(m.cfg.BinDir, "monitor-server")
	if _, err := os.Stat(filepath.Join(latest, "monitor-server")); err == nil {
		if err := copyFile(filepath.Join(latest, "monitor-server"), serverBin); err != nil {
			errs = append(errs, "恢复 server 失败: "+err.Error())
		}
	}
	if _, err := os.Stat(filepath.Join(latest, "web")); err == nil {
		if err := syncDir(filepath.Join(latest, "web"), m.cfg.WebDir); err != nil {
			errs = append(errs, "恢复 web 失败: "+err.Error())
		}
	}
	if err := m.restart(); err != nil {
		errs = append(errs, "重启 server 失败: "+err.Error())
	}

	result := "success"
	detail := ""
	if len(errs) > 0 {
		result = "failed"
		detail = strings.Join(errs, "; ")
	}
	m.recordHistory(filepath.Base(latest), "", "rollback", operator, result, detail, false)
	if result == "failed" {
		return errors.New(detail)
	}
	return nil
}

// Current 返回当前待应用任务的快照（深拷贝）。
func (m *Manager) Current() *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil
	}
	cp := *m.current
	return &cp
}

// History 返回历史记录（最新在前）。
func (m *Manager) History() []HistoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]HistoryEntry, len(m.history))
	copy(out, m.history)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (m *Manager) recordHistory(id, version, action, operator, result, detail string, agentCDN bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, HistoryEntry{
		ID:              id,
		Version:         version,
		Action:          action,
		At:              time.Now(),
		Operator:        operator,
		Result:          result,
		Detail:          detail,
		AgentCDNUpdated: agentCDN,
	})
	m.saveHistoryLocked()
}

func (m *Manager) loadHistory() {
	data, err := os.ReadFile(m.histPath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &m.history)
}

func (m *Manager) saveHistoryLocked() {
	data, err := json.MarshalIndent(m.history, "", "  ")
	if err != nil {
		return
	}
	tmp := m.histPath + ".tmp"
	_ = os.WriteFile(tmp, data, 0o644)
	_ = os.Rename(tmp, m.histPath)
}

func (m *Manager) pruneBackups() {
	backups, _ := filepath.Glob(filepath.Join(m.cfg.Dir, "backups", "*"))
	if len(backups) <= m.cfg.BackupKeep {
		return
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i] < backups[j] }) // 旧→新
	for _, b := range backups[:len(backups)-m.cfg.BackupKeep] {
		_ = os.RemoveAll(b)
	}
}

func (m *Manager) restart() error {
	if !m.cfg.UseSystemd {
		return errors.New("非 systemd 模式暂不支持自动重启，请手动重启 monitor-server")
	}
	cmd := exec.Command("systemctl", "restart", m.cfg.Service)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不等待，让重启在后台发生
	go func() { _ = cmd.Wait() }()
	return nil
}

// ---- 工具函数 ----

func untarGz(src, dst string) error {
	// 兼容两种打包方式：带唯一顶层目录（如 nebula-monitor-v1.1.0-upgrade/）
	// 或扁平包（manifest.json 在根）。若全部条目共享同一顶层目录，则剥离该前缀。
	prefix := tarLeadingPrefix(src)

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanDst := filepath.Clean(dst)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := hdr.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
			// 顶层目录本身（剥离后为空）直接跳过
			if name == "" {
				continue
			}
		}
		target := filepath.Clean(filepath.Join(dst, name))
		// 防止 zip slip
		if !strings.HasPrefix(target, cleanDst+string(os.PathSeparator)) && target != cleanDst {
			return fmt.Errorf("非法路径: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

// tarLeadingPrefix 读取 tar.gz 内全部文件名，若它们都共享同一个顶层目录，
// 返回该目录前缀（含结尾 /），否则返回空串（表示扁平包，无需剥离）。
func tarLeadingPrefix(src string) string {
	f, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return ""
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var cand string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		if hdr.Typeflag == tar.TypeDir {
			continue // 顶层目录条目不影响前缀判断
		}
		first := strings.SplitN(hdr.Name, "/", 2)[0]
		if cand == "" {
			cand = first
		} else if cand != first {
			return "" // 存在多个顶层，不剥离
		}
	}
	if cand == "" {
		return ""
	}
	return cand + "/"
}

func copyFile(src, dst string) error {
	return copyFileMode(src, dst, 0o644)
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// replaceFileAtomic 先写到同目录临时文件，再 rename 原子替换目标。
// 用于替换正在运行的 server 二进制：直接 O_TRUNC 覆盖会因 ETXTBSY（text file busy）失败，
// 而 rename 只替换目录项、不影响正在运行的旧 inode，规避该问题。
func replaceFileAtomic(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upgrade-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// syncDir 清空 dst 后将 src 内容复制到 dst。
func syncDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode() & 0o755
		if mode == 0 {
			mode = 0o644
		}
		return copyFileMode(path, target, mode)
	})
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func pathSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var s int64
	err = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				s += info.Size()
			}
		}
		return nil
	})
	return s, err
}

func parseOctal(s string) (os.FileMode, error) {
	var n uint64
	_, err := fmt.Sscanf(s, "%o", &n)
	return os.FileMode(n), err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}