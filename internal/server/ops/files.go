package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 文件分发的**内容存储**：任务里只有引用（fileId），内容单独落盘。
//
// 为什么不把内容放进任务参数：Task 内嵌 OpsCommand，而 Store.save() 每次状态流转都用
// MarshalIndent **整体重写** ops_tasks.json（保留上限 500 条）。内容一旦进了 Params，
// 这个文件就会以「条数 × 文件大小」的量级膨胀并被反复序列化——按 256KiB 上限算，
// 满载时是几十 MB 的 JSON 每有一次状态变化就重写一遍。
//
// 于是：内容落 <dir>/<ref>.bin，索引落 <dir>/ops_files.json，任务里只留 fileId；
// 真正下发时由 Store.Take 把内容注入到**那一份副本**里（不落任务存储）。

const (
	// fileIndexName 是上传文件的索引文件名（与任务存储同目录便于一起备份）。
	fileIndexName = "ops_files.json"
	// maxFiles 是保留的上传文件数上限。
	//
	// 内容不落任务存储，所以磁盘要靠这里管住：100 × 256KiB ≈ 25MB 封顶。
	// 淘汰时**跳过仍被未结束任务引用的文件**——把排队中任务的内容删掉，会让它在领取时
	// 突然没有内容可发，表现为"点了分发没反应"，是最难排查的一类失败。
	maxFiles = 100
	// fileDirPerm / fileContentPerm 分别是目录与内容的权限。
	//
	// 内容收 0600：这是运维要分发出去的东西，但它在本机落盘时不该对同机其它用户可读
	// （分发证书私钥是完全正当的用法）。
	fileDirPerm     = 0o700
	fileContentPerm = 0o600
)

// FileRecord 是一个已上传文件的元信息（不含内容）。
//
// 它会被快照进任务记录（Task.File），让"当时分发的是哪个文件"在内容被淘汰后仍然可查——
// 审计价值就在这里，因此这几个字段是刻意冗余的。
type FileRecord struct {
	Ref        string `json:"ref"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	UploadedAt int64  `json:"uploadedAt"`
	UploadedBy string `json:"uploadedBy,omitempty"`
}

// FileStore 保存上传的文件内容（<dir>/<ref>.bin）与索引（<dir>/ops_files.json）。
type FileStore struct {
	dir string
	now func() int64

	mu    sync.Mutex
	files map[string]*FileRecord
	seq   int64
}

// OpenFileStore 打开（必要时创建）文件存储目录并载入索引。
func OpenFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, fileDirPerm); err != nil {
		return nil, fmt.Errorf("创建操作文件目录失败: %w", err)
	}
	s := &FileStore{
		dir:   dir,
		now:   func() int64 { return time.Now().UnixMilli() },
		files: map[string]*FileRecord{},
	}
	s.load()
	return s, nil
}

// SetNow 覆盖时钟（测试用）。
func (s *FileStore) SetNow(f func() int64) {
	if f != nil {
		s.now = f
	}
}

// Save 保存一个上传的文件并返回它的引用记录。
//
// inUse 用于淘汰保护（返回 true 表示该引用仍被未结束的任务占用，不能删）；可为 nil。
func (s *FileStore) Save(name string, content []byte, by string, inUse func(string) bool) (*FileRecord, error) {
	if len(content) == 0 {
		// 空文件几乎总是上传环节出了问题（读了个空流），而它的后果是"把机器上的配置换成空的"。
		// 备份能救回来，但这种"安静地做错事"不该放行。
		return nil, fmt.Errorf("文件内容为空，拒绝分发（如确实要清空，请分发一个只有注释/换行的文件）")
	}
	if len(content) > model.OpsFileMaxBytes {
		return nil, fmt.Errorf("文件大小 %d 字节超过单次分发上限 %d KiB：大文件请分批或改用其它方式",
			len(content), model.OpsFileMaxBytes>>10)
	}

	sum := sha256.Sum256(content)
	rec := &FileRecord{
		Name:       sanitizeFileName(name),
		Size:       int64(len(content)),
		SHA256:     hex.EncodeToString(sum[:]),
		UploadedAt: s.now(),
		UploadedBy: by,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	rec.Ref = "obf-" + itoa(s.seq)

	// 先落内容再记索引：反过来的话，索引里会出现一个没有内容的引用，
	// 而那条任务会在领取时才失败——问题暴露得太晚。
	path := s.contentPath(rec.Ref)
	if err := os.WriteFile(path, content, fileContentPerm); err != nil {
		s.seq--
		return nil, fmt.Errorf("写入操作文件失败: %w", err)
	}
	s.files[rec.Ref] = rec
	for _, ref := range s.prune(inUse) {
		if err := os.Remove(s.contentPath(ref)); err != nil && !os.IsNotExist(err) {
			// 索引已经删了，文件留着不影响正确性；下次启动不会载入它。
			continue
		}
	}
	s.persist()
	return rec, nil
}

// Content 读取某个引用的内容。第二个返回值为 nil 表示引用不存在（已被淘汰或从未上传）。
func (s *FileStore) Content(ref string) ([]byte, *FileRecord) {
	s.mu.Lock()
	rec, ok := s.files[ref]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(s.contentPath(ref))
	if err != nil {
		return nil, nil
	}
	return data, rec
}

// Get 返回引用对应的元信息（不含内容）。
func (s *FileStore) Get(ref string) (*FileRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.files[ref]
	if !ok {
		return nil, false
	}
	cp := *rec
	return &cp, true
}

// List 返回全部已上传文件（新上传的在前）。
func (s *FileStore) List() []*FileRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*FileRecord, 0, len(s.files))
	for _, r := range s.files {
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UploadedAt != out[j].UploadedAt {
			return out[i].UploadedAt > out[j].UploadedAt
		}
		return out[i].Ref > out[j].Ref
	})
	return out
}

// prune 淘汰最老的文件直到回到上限之内，返回被淘汰的引用。调用方需持有 s.mu。
func (s *FileStore) prune(inUse func(string) bool) []string {
	if len(s.files) <= maxFiles {
		return nil
	}
	recs := make([]*FileRecord, 0, len(s.files))
	for _, r := range s.files {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].UploadedAt != recs[j].UploadedAt {
			return recs[i].UploadedAt < recs[j].UploadedAt
		}
		// 同一毫秒上传的用引用号兜底定序，只为让淘汰结果可复现
		return recs[i].Ref < recs[j].Ref
	})
	var removed []string
	for _, r := range recs {
		if len(s.files) <= maxFiles {
			break
		}
		if inUse != nil && inUse(r.Ref) {
			continue
		}
		delete(s.files, r.Ref)
		removed = append(removed, r.Ref)
	}
	return removed
}

func (s *FileStore) contentPath(ref string) string {
	return filepath.Join(s.dir, ref+".bin")
}

func (s *FileStore) indexPath() string {
	return filepath.Join(s.dir, fileIndexName)
}

func (s *FileStore) load() {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return
	}
	var snap struct {
		Files []*FileRecord `json:"files"`
		Seq   int64         `json:"seq"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		// 索引坏了不该让服务起不来：丢失的是"可选的历史上传"，不是任务本身。
		return
	}
	for _, r := range snap.Files {
		if r == nil || r.Ref == "" {
			continue
		}
		// 内容不在的引用一并丢掉：留着它只会让一条任务在领取时才失败。
		if _, err := os.Stat(s.contentPath(r.Ref)); err != nil {
			continue
		}
		s.files[r.Ref] = r
	}
	s.seq = snap.Seq
}

func (s *FileStore) persist() {
	snap := struct {
		Files []*FileRecord `json:"files"`
		Seq   int64         `json:"seq"`
	}{Seq: s.seq, Files: make([]*FileRecord, 0, len(s.files))}
	for _, r := range s.files {
		snap.Files = append(snap.Files, r)
	}
	sort.Slice(snap.Files, func(i, j int) bool { return snap.Files[i].Ref < snap.Files[j].Ref })

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	tmp := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.indexPath())
}

// sanitizeFileName 归一化展示用的文件名。
//
// 只用于展示（内容按引用号落盘，名字永不参与路径拼接），但仍要清洗：
// 上传方给的原始名字可能带路径与换行，直接渲染会把界面搞乱，也会让审计记录读不懂。
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "未命名文件"
	}
	if r := []rune(name); len(r) > 128 {
		name = string(r[:128])
	}
	return name
}
