package ops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// requireUnixFS 跳过需要真实文件系统落盘的用例。
//
// 目标路径必须是以 "/" 开头的绝对路径（OpsFilePathPattern 的约定，Agent 也只在 Linux/macOS 上跑），
// 而 Windows 的盘符路径无法满足它。护栏、参数、摘要这几组用例不碰文件系统，在哪个平台都跑。
func requireUnixFS(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("落盘用例需要 POSIX 路径语义，本机为 Windows")
	}
}

func filePushCmd(target string, content []byte, mode string) model.OpsCommand {
	sum := sha256.Sum256(content)
	params := map[string]string{"path": target, "fileId": "obf-1"}
	if mode != "" {
		params["mode"] = mode
	}
	return model.OpsCommand{
		ID: "cmd-1", Kind: model.OpsKindFilePush, Params: params,
		File: &model.OpsFileBlob{
			Name: "app.conf", Size: int64(len(content)),
			SHA256:  hex.EncodeToString(sum[:]),
			Content: base64.StdEncoding.EncodeToString(content),
		},
	}
}

func fileExecutor(dirs []string, write bool) *Executor {
	return New("web-01", config.OpsGuards{File: config.OpsFileGuards{Write: write, Dirs: dirs}}, "")
}

// 正常路径：新文件落盘、内容与权限都对。
func TestFilePush_WritesNewFile(t *testing.T) {
	requireUnixFS(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "app.conf")
	body := []byte("listen 8080;\n")

	res := fileExecutor([]string{dir}, true).Execute(filePushCmd(target, body, "0640"))
	if res.State != model.OpsStateSucceeded {
		t.Fatalf("应成功，实际 %s：%s", res.State, res.Message)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(body) {
		t.Fatalf("目标文件内容不符：%q / %v", string(got), err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("权限应为 0640，实际 %o", st.Mode().Perm())
	}
	if !strings.Contains(res.Data["原文件"], "新增") {
		t.Fatalf("新增文件应说明没有备份，实际 %q", res.Data["原文件"])
	}
	// 临时文件不能残留
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nebula-filepush-") {
			t.Fatalf("不应残留临时文件：%s", e.Name())
		}
	}
}

// 覆盖：原文件必须先备份，且备份里是旧内容。
func TestFilePush_BacksUpBeforeOverwrite(t *testing.T) {
	requireUnixFS(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("准备原文件失败: %v", err)
	}

	res := fileExecutor([]string{dir}, true).Execute(filePushCmd(target, []byte("new\n"), ""))
	if res.State != model.OpsStateSucceeded {
		t.Fatalf("应成功，实际 %s：%s", res.State, res.Message)
	}
	if got, _ := os.ReadFile(target); string(got) != "new\n" {
		t.Fatalf("目标应为新内容，实际 %q", string(got))
	}
	bak := strings.TrimPrefix(res.Data["原文件"], "已备份为 ")
	if !strings.Contains(bak, ".bak-") {
		t.Fatalf("备份路径形态不符：%q", bak)
	}
	if got, err := os.ReadFile(bak); err != nil || string(got) != "old\n" {
		t.Fatalf("备份里应是旧内容：%q / %v", string(got), err)
	}
}

// 护栏：总开关、目录清单、目录边界（允许 /opt/app 不等于允许 /opt/application）。
func TestFilePush_Guards(t *testing.T) {
	dir := "/opt/app"
	cases := []struct {
		name  string
		exec  *Executor
		path  string
		want  string
		match bool
	}{
		{"未开总开关", fileExecutor([]string{dir}, false), dir + "/a.conf", "未放行文件分发", true},
		{"没给目录清单", fileExecutor(nil, true), dir + "/a.conf", "未列出允许写入的目录", true},
		{"目录外", fileExecutor([]string{dir}, true), "/etc/passwd", "不在允许写入的目录内", true},
		{"同前缀的兄弟目录", fileExecutor([]string{dir}, true), "/opt/application/a.conf", "不在允许写入的目录内", true},
		{"相对路径", fileExecutor([]string{dir}, true), "opt/app/a.conf", "目标路径不合法", true},
		{"带穿越的相对形态", fileExecutor([]string{dir}, true), "/opt/app/../etc/passwd", "目标路径不合法", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.exec.Execute(filePushCmd(tc.path, []byte("x"), ""))
			if res.State != model.OpsStateFailed {
				t.Fatalf("应失败，实际 %s", res.State)
			}
			if !strings.Contains(res.Message, tc.want) {
				t.Fatalf("失败原因应包含 %q，实际 %q", tc.want, res.Message)
			}
		})
	}
}

// 摘要不符：必须拒绝，且**绝不碰目标文件**（这条错了就是"把坏内容写上了生产"）。
func TestFilePush_RejectsTamperedContent(t *testing.T) {
	cmd := filePushCmd("/opt/app/app.conf", []byte("good"), "")
	// 篡改内容但保留原摘要
	cmd.File.Content = base64.StdEncoding.EncodeToString([]byte("evil"))
	cmd.File.Size = 4

	res := fileExecutor([]string{"/opt/app"}, true).Execute(cmd)
	if res.State != model.OpsStateFailed {
		t.Fatalf("摘要不符应失败，实际 %s", res.State)
	}
	if !strings.Contains(res.Message, "sha256") {
		t.Fatalf("失败原因应指出摘要不符，实际 %q", res.Message)
	}
}

// 长度与声明不符：同样在校验阶段拦下（避免"截断的内容被当成完整文件写上去"）。
func TestFilePush_RejectsSizeMismatch(t *testing.T) {
	cmd := filePushCmd("/opt/app/app.conf", []byte("good"), "")
	cmd.File.Size = 999

	res := fileExecutor([]string{"/opt/app"}, true).Execute(cmd)
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "长度与声明不符") {
		t.Fatalf("长度不符应失败，实际 %s：%s", res.State, res.Message)
	}
}

// setuid 位：服务端已拒，本机也要拒——分发一个 setuid 文件等于远程提权。
func TestFilePush_RejectsSetuidMode(t *testing.T) {
	res := fileExecutor([]string{"/opt/app"}, true).Execute(filePushCmd("/opt/app/app.conf", []byte("x"), "4755"))
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "权限不合法") {
		t.Fatalf("setuid 权限应被拒，实际 %s：%s", res.State, res.Message)
	}
}

// 缺载荷：不能当成"空文件写入"，必须明确失败。
func TestFilePush_RejectsMissingBlob(t *testing.T) {
	cmd := filePushCmd("/opt/app/app.conf", []byte("x"), "")
	cmd.File = nil
	res := fileExecutor([]string{"/opt/app"}, true).Execute(cmd)
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "没有携带文件内容") {
		t.Fatalf("缺载荷应明确失败，实际 %s：%s", res.State, res.Message)
	}
}

// 软链逃逸：允许 /opt/app，但 /opt/app/conf 是指向外部的软链——必须拒绝。
func TestFilePush_RejectsSymlinkEscape(t *testing.T) {
	requireUnixFS(t)
	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	link := filepath.Join(allowed, "conf")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("本环境不支持创建符号链接：%v", err)
	}

	res := fileExecutor([]string{allowed}, true).Execute(filePushCmd(filepath.Join(link, "app.conf"), []byte("x"), ""))
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "解析后不在允许写入的范围内") {
		t.Fatalf("软链逃逸应被拒，实际 %s：%s", res.State, res.Message)
	}
	if _, err := os.Stat(filepath.Join(outside, "app.conf")); err == nil {
		t.Fatal("外部目录不应被写入")
	}
}

// 目标已是目录：拒绝（把目录改名搬走再放文件上去，破坏性远超分发一个文件）。
func TestFilePush_RejectsDirectoryTarget(t *testing.T) {
	requireUnixFS(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	res := fileExecutor([]string{dir}, true).Execute(filePushCmd(target, []byte("x"), ""))
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "不是普通文件") {
		t.Fatalf("目录目标应被拒，实际 %s：%s", res.State, res.Message)
	}
}

// 能力协商：只有总开关与目录清单都就位才声明 file.push。
func TestExecutor_SupportedIncludesFilePushOnlyWhenAllowed(t *testing.T) {
	cases := []struct {
		name  string
		guard config.OpsFileGuards
		want  bool
	}{
		{"默认不放行", config.OpsFileGuards{}, false},
		{"只开总开关", config.OpsFileGuards{Write: true}, false},
		{"给了目录但没开总开关", config.OpsFileGuards{Dirs: []string{"/opt/app"}}, false},
		{"只给了非绝对路径", config.OpsFileGuards{Write: true, Dirs: []string{"opt/app"}}, false},
		{"只给了根目录", config.OpsFileGuards{Write: true, Dirs: []string{"/"}}, false},
		{"都就位", config.OpsFileGuards{Write: true, Dirs: []string{"/opt/app"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New("web-01", config.OpsGuards{File: tc.guard}, "")
			got := false
			for _, k := range e.Supported() {
				if k == model.OpsKindFilePush {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("file.push 声明与否不符：实际 %v，期望 %v（supported=%v）", got, tc.want, e.Supported())
			}
		})
	}
}

// 同一条指令重复投递只执行一次：否则会把刚写好的文件再覆盖一遍并多出一个备份。
func TestFilePush_IdempotentPerCommand(t *testing.T) {
	requireUnixFS(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "app.conf")
	e := fileExecutor([]string{dir}, true)

	cmd := filePushCmd(target, []byte("v1\n"), "")
	if res := e.Execute(cmd); res.State != model.OpsStateSucceeded {
		t.Fatalf("首次应成功，实际 %s：%s", res.State, res.Message)
	}
	if err := os.WriteFile(target, []byte("manual\n"), 0o644); err != nil {
		t.Fatalf("改写目标失败: %v", err)
	}
	if res := e.Execute(cmd); res.State != model.OpsStateSucceeded {
		t.Fatalf("重复投递应返回上次结果，实际 %s", res.State)
	}
	if got, _ := os.ReadFile(target); string(got) != "manual\n" {
		t.Fatalf("重复投递不应再次写入，实际 %q", string(got))
	}
}
