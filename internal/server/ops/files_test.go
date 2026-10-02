package ops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func newTestFiles(t *testing.T) *FileStore {
	t.Helper()
	fs, err := OpenFileStore(filepath.Join(t.TempDir(), "ops_files"))
	if err != nil {
		t.Fatalf("打开文件存储失败: %v", err)
	}
	return fs
}

// 上传 → 取回：内容、摘要、大小、引用号与文件名归一化。
func TestFileStore_SaveAndContent(t *testing.T) {
	fs := newTestFiles(t)
	body := []byte("server {\n  listen 80;\n}\n")

	rec, err := fs.Save("/tmp/../evil/nginx.conf", body, "alice", nil)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !model.OpsFileRefPattern.MatchString(rec.Ref) {
		t.Fatalf("引用号形态不符：%q", rec.Ref)
	}
	if rec.Size != int64(len(body)) {
		t.Fatalf("大小应为 %d，实际 %d", len(body), rec.Size)
	}
	sum := sha256.Sum256(body)
	if rec.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("摘要不符：%s", rec.SHA256)
	}
	// 文件名只用于展示，但也不能把上传方给的路径带进来
	if rec.Name != "nginx.conf" {
		t.Fatalf("文件名应归一化为 nginx.conf，实际 %q", rec.Name)
	}
	if rec.UploadedBy != "alice" || rec.UploadedAt == 0 {
		t.Fatalf("应记录上传人与时间：%+v", rec)
	}

	got, gotRec := fs.Content(rec.Ref)
	if string(got) != string(body) || gotRec == nil || gotRec.Ref != rec.Ref {
		t.Fatalf("取回内容不符：%q / %+v", string(got), gotRec)
	}
	if _, missing := fs.Content("obf-999"); missing != nil {
		t.Fatal("不存在的引用应返回 nil 记录")
	}
}

// 空文件与超限必须拒绝：两者的后果都是"安静地做错事"。
func TestFileStore_RejectsEmptyAndOversize(t *testing.T) {
	fs := newTestFiles(t)
	if _, err := fs.Save("empty.conf", nil, "alice", nil); err == nil {
		t.Fatal("空文件应被拒绝")
	}
	_, err := fs.Save("big.bin", make([]byte, model.OpsFileMaxBytes+1), "alice", nil)
	if err == nil {
		t.Fatal("超过上限的文件应被拒绝")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限错误信息应说明上限，实际 %q", err.Error())
	}
}

// readStoreFile 读回任务存储的原文，用于断言"内容没有落进去"。
func readStoreFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取任务存储失败: %v", err)
	}
	return string(data)
}

// 淘汰：超过上限时丢最老的，但**跳过仍被未结束任务引用的**。
//
// 把一个排队中任务的内容删掉，那条任务会在领取时突然没有内容可发——
// 用户看到的是"点了分发没反应"，这类失败最难排查。
func TestFileStore_PruneSkipsInUse(t *testing.T) {
	fs := newTestFiles(t)
	now := int64(1_700_000_000_000)
	fs.SetNow(func() int64 { return now })

	first, err := fs.Save("first.conf", []byte("a"), "alice", nil)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 之后每个文件的时钟都往后推，保证淘汰顺序确定
	for i := 0; i < maxFiles; i++ {
		now++
		if _, err := fs.Save("later.conf", []byte("b"), "alice", nil); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
	}
	// 此时 first 应已被淘汰（它没有被引用）
	if _, rec := fs.Content(first.Ref); rec != nil {
		t.Fatal("没有被引用的最老文件应被淘汰")
	}

	// 再传一批，但把刚上传的这个标记为"引用中"：它必须活下来
	now++
	protected, err := fs.Save("protected.conf", []byte("c"), "alice", nil)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	for i := 0; i < maxFiles; i++ {
		now++
		inUse := func(ref string) bool { return ref == protected.Ref }
		if _, err := fs.Save("later.conf", []byte("b"), "alice", inUse); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
	}
	if _, rec := fs.Content(protected.Ref); rec == nil {
		t.Fatal("被未结束任务引用的文件不应被淘汰")
	}
	if n := len(fs.List()); n != maxFiles {
		t.Fatalf("应稳定在上限 %d，实际 %d", maxFiles, n)
	}
}

// 核心不变量：内容**只在下发给 Agent 的那一份里**，任务存储里只有引用。
//
// 这条一旦破了，ops_tasks.json 会以「条数 × 文件大小」的量级膨胀，
// 而它是每次状态流转都整体重写的一份文件。
func TestStore_TakeInjectsFileBlobWithoutPersistingIt(t *testing.T) {
	s := newTestStore(t)
	fs := newTestFiles(t)
	s.SetFileStore(fs)

	body := []byte("listen 8080;\n")
	rec, err := fs.Save("app.conf", body, "alice", nil)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	task := s.Create(model.OpsCommand{
		Node: "web-01", Kind: KindFilePush,
		Params: map[string]string{"path": "/opt/app/conf/app.conf", "fileId": rec.Ref},
	}, "alice", "10.0.0.9", "改端口")
	s.AttachFile(task.ID, rec)
	s.SaveCaps("web-01", []string{KindFilePush})

	// 落盘的任务里不能有内容
	data := readStoreFile(t, s.path)
	if strings.Contains(data, base64.StdEncoding.EncodeToString(body)) {
		t.Fatal("任务存储里不应出现文件内容")
	}
	if !strings.Contains(data, rec.Ref) {
		t.Fatal("任务存储里应保留文件引用")
	}

	cmd := s.Take("web-01", []string{KindFilePush})
	if cmd == nil {
		t.Fatal("应能领取到任务")
	}
	if cmd.File == nil {
		t.Fatal("下发的指令必须带上文件载荷")
	}
	if cmd.File.SHA256 != rec.SHA256 || cmd.File.Size != int64(len(body)) || cmd.File.Name != "app.conf" {
		t.Fatalf("载荷元信息不符：%+v", cmd.File)
	}
	got, err := base64.StdEncoding.DecodeString(cmd.File.Content)
	if err != nil || string(got) != string(body) {
		t.Fatalf("载荷内容不符：%q / %v", string(got), err)
	}

	// 领取之后再看一次存储：仍然不能有内容（注入的是副本）
	if data := readStoreFile(t, s.path); strings.Contains(data, base64.StdEncoding.EncodeToString(body)) {
		t.Fatal("领取不应把内容写进任务存储")
	}
	if got, _ := s.Get(task.ID); got.File == nil || got.File.Ref != rec.Ref {
		t.Fatal("任务记录应保留文件元信息快照")
	}
}

// 内容不在了：任务必须**明确失败**，而不是下发一条没有内容的指令。
func TestStore_TakeFailsWhenFileGone(t *testing.T) {
	s := newTestStore(t)
	fs := newTestFiles(t)
	s.SetFileStore(fs)
	s.SaveCaps("web-01", []string{KindFilePush})

	task := s.Create(model.OpsCommand{
		Node: "web-01", Kind: KindFilePush,
		Params: map[string]string{"path": "/opt/app/conf/app.conf", "fileId": "obf-42"},
	}, "alice", "", "")

	if cmd := s.Take("web-01", []string{KindFilePush}); cmd != nil {
		t.Fatalf("内容不存在时不应下发，实际 %+v", cmd)
	}
	got, _ := s.Get(task.ID)
	if got.State != model.OpsStateFailed {
		t.Fatalf("任务应被判失败，实际 %s", got.State)
	}
	if !strings.Contains(got.Message, "obf-42") {
		t.Fatalf("失败原因应带上引用号，实际 %q", got.Message)
	}
}

// 引用占用查询：只有未结束的任务才算占用（已结束的任务不该挡住淘汰）。
func TestStore_FileRefInUse(t *testing.T) {
	s := newTestStore(t)
	s.SaveCaps("web-01", []string{KindFilePush})
	task := s.Create(model.OpsCommand{
		Node: "web-01", Kind: KindFilePush,
		Params: map[string]string{"path": "/opt/app.conf", "fileId": "obf-1"},
	}, "alice", "", "")

	if !s.FileRefInUse("obf-1") {
		t.Fatal("排队中的任务应算占用")
	}
	if s.FileRefInUse("obf-2") {
		t.Fatal("未被引用的文件不应算占用")
	}
	s.ApplyResult(model.OpsResult{CommandID: task.ID, State: model.OpsStateSucceeded})
	if s.FileRefInUse("obf-1") {
		t.Fatal("已结束的任务不应再算占用")
	}
}
