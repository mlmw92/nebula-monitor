package collector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// TestSecurityCollector_SudoUsesOwnOffset sudo 与 SSH 读同一个文件、但关注的是不同内容，
// 因此必须**各用各的偏移**。
//
// 此前这里复用 sshOff：collectSSH 在本函数之前执行、已把偏移推到文件末尾，
// 于是 sudo 永远从末尾开始读——表现为「sudo 审计一条都收不到」，而且不报任何错
//（既没有错误日志，也没有 up/失败指标），属于最难发现的那类缺陷。
func TestSecurityCollector_SudoUsesOwnOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.log")
	body := "Sep 26 10:00:00 host sshd[1]: Accepted password for alice from 10.0.0.9 port 22 ssh2\n" +
		"Sep 26 10:00:01 host sudo:   alice : TTY=pts/0 ; PWD=/home/alice ; USER=root ; COMMAND=/bin/systemctl restart nginx\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewSecurityCollector("n1", "10.0.0.1", config.SecurityConfig{SSHLogPaths: []string{path}})

	// 先跑 SSH：它会把自己的偏移推到末尾
	if got := c.collectSSH(); len(got) != 1 {
		t.Fatalf("SSH 应解析出 1 条登录成功事件，got %d", len(got))
	}
	// 再跑 sudo：同一文件里的 sudo 行仍必须被读到
	sudo := c.collectSudo()
	if len(sudo) != 1 {
		t.Fatalf("sudo 应解析出 1 条提权事件（与 SSH 各用独立偏移），got %d", len(sudo))
	}
	if sudo[0].Category != model.SecurityCatSudoAudit {
		t.Fatalf("事件类别不符：%+v", sudo[0])
	}
	// 偏移已推进：重复采集不应重复上报
	if again := c.collectSudo(); len(again) != 0 {
		t.Fatalf("偏移应已推进，不应重复上报，got %d", len(again))
	}
	// 追加新行后应能续读（增量语义）
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(
		"Sep 26 10:00:02 host sudo:   bob : TTY=pts/1 ; PWD=/root ; USER=root ; COMMAND=/usr/bin/apt update\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := c.collectSudo(); len(got) != 1 {
		t.Fatalf("追加的 sudo 行应被续读，got %d", len(got))
	}
	// SSH 自己的偏移不受 sudo 影响：追加一行 SSH 成功后仍能读到
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(
		"Sep 26 10:00:03 host sshd[2]: Accepted password for bob from 10.0.0.8 port 22 ssh2\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := c.collectSSH(); len(got) != 1 {
		t.Fatalf("SSH 应能续读自己关注的行，got %d", len(got))
	}
}
