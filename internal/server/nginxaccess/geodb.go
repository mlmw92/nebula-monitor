package nginxaccess

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

//go:embed data/ip2region_v4.xdb
var builtinDB []byte

// DBInfo 描述当前生效的 IP 地理库。
type DBInfo struct {
	Source    string `json:"source"`          // builtin=随程序内置；custom=运行时覆盖文件
	Path      string `json:"path"`            // 覆盖文件路径（未配置时为空）
	Size      int64  `json:"size"`            // 当前生效库的字节数
	SHA256    string `json:"sha256"`          // 当前生效库的完整校验和
	UpdatedAt string `json:"updatedAt"`       // 覆盖文件的修改时间（RFC3339），内置库为空
	Loaded    bool   `json:"loaded"`          // 是否已成功加载
	Error     string `json:"error,omitempty"` // 加载失败原因
}

// geoDB 是全局 IP 地理库数据源。
// 优先加载磁盘上的覆盖文件（可经 Web 端上传更新，无需重新打包 server），
// 覆盖文件不存在或损坏时回退到随程序内置的库。替换后立即对所有查询生效。
type geoDB struct {
	mu           sync.RWMutex
	searcher     *xdb.Searcher
	info         DBInfo
	overridePath string
	once         sync.Once
}

var defaultDB = &geoDB{}

// probeIPs 用于校验一份 xdb 是否可正常查询。
var probeIPs = []string{"114.114.114.114", "8.8.8.8"}

// SetGeoOverridePath 设置运行时覆盖文件路径并尝试加载。
// path 为空表示不启用覆盖，仅使用内置库。文件不存在时回退内置库且不视为错误。
func SetGeoOverridePath(path string) error {
	return defaultDB.setOverridePath(path)
}

// GeoOverridePath 返回当前配置的覆盖文件路径（未配置时为空）。
func GeoOverridePath() string {
	defaultDB.mu.RLock()
	defer defaultDB.mu.RUnlock()
	return defaultDB.overridePath
}

// GeoDBInfo 返回当前生效库的信息。
func GeoDBInfo() DBInfo {
	defaultDB.ensure()
	defaultDB.mu.RLock()
	defer defaultDB.mu.RUnlock()
	return defaultDB.info
}

// ReplaceGeoDB 用上传的 xdb 内容替换覆盖文件并立即热加载。
// 校验不通过时保持原库不变。返回替换后的库信息。
func ReplaceGeoDB(data []byte) (DBInfo, error) {
	return defaultDB.replace(data)
}

// ResetGeoDB 删除覆盖文件并回退到内置库。
func ResetGeoDB() (DBInfo, error) {
	return defaultDB.reset()
}

// GeoSearchRaw 用当前库查询原始 region 串（国家|省份|城市|ISP|国家代码），供测试查询使用。
func GeoSearchRaw(ip string) (string, error) {
	defaultDB.ensure()
	defaultDB.mu.RLock()
	s := defaultDB.searcher
	defaultDB.mu.RUnlock()
	if s == nil {
		return "", errors.New("IP 地理库未加载")
	}
	return s.Search(ip)
}

// ensure 惰性加载：首次查询时若尚未加载则加载内置库。
func (d *geoDB) ensure() {
	d.once.Do(func() {
		d.mu.RLock()
		loaded := d.searcher != nil
		d.mu.RUnlock()
		if loaded {
			return
		}
		_ = d.loadBuiltin()
	})
}

// search 用当前库查询，未加载或查询失败返回空串。
func (d *geoDB) search(ip string) string {
	d.ensure()
	d.mu.RLock()
	s := d.searcher
	d.mu.RUnlock()
	if s == nil {
		return ""
	}
	region, err := s.Search(ip)
	if err != nil {
		return ""
	}
	return region
}

func (d *geoDB) setOverridePath(path string) error {
	d.mu.Lock()
	d.overridePath = path
	d.mu.Unlock()
	// 标记已初始化，避免后续 ensure 再次加载内置库覆盖本次结果
	d.once.Do(func() {})

	if path == "" {
		return d.loadBuiltin()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			// 文件存在但读取失败：记录原因，回退内置库
			_ = d.loadBuiltin()
			return fmt.Errorf("读取 IP 地理库覆盖文件失败: %w", err)
		}
		return d.loadBuiltin()
	}
	if err := d.loadCustom(data, path); err != nil {
		_ = d.loadBuiltin()
		return fmt.Errorf("加载 IP 地理库覆盖文件失败: %w", err)
	}
	return nil
}

func (d *geoDB) replace(data []byte) (DBInfo, error) {
	d.mu.RLock()
	path := d.overridePath
	d.mu.RUnlock()
	if path == "" {
		return d.snapshot(), errors.New("未配置 IP 地理库存放路径（server.yaml: geoipFile）")
	}
	searcher, err := newSearcher(data)
	if err != nil {
		return d.snapshot(), err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return d.snapshot(), fmt.Errorf("创建目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return d.snapshot(), fmt.Errorf("写入文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return d.snapshot(), fmt.Errorf("替换文件失败: %w", err)
	}
	d.apply(searcher, DBInfo{
		Source:    "custom",
		Path:      path,
		Size:      int64(len(data)),
		SHA256:    sum256(data),
		UpdatedAt: time.Now().Format(time.RFC3339),
		Loaded:    true,
	})
	return d.snapshot(), nil
}

func (d *geoDB) reset() (DBInfo, error) {
	d.mu.RLock()
	path := d.overridePath
	d.mu.RUnlock()
	if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return d.snapshot(), fmt.Errorf("删除覆盖文件失败: %w", err)
		}
	}
	if err := d.loadBuiltin(); err != nil {
		return d.snapshot(), err
	}
	return d.snapshot(), nil
}

func (d *geoDB) loadBuiltin() error {
	searcher, err := newSearcher(builtinDB)
	info := DBInfo{
		Source: "builtin",
		Size:   int64(len(builtinDB)),
		SHA256: sum256(builtinDB),
		Loaded: err == nil,
	}
	if err != nil {
		info.Error = err.Error()
	}
	d.mu.RLock()
	info.Path = d.overridePath
	d.mu.RUnlock()
	d.apply(searcher, info)
	return err
}

func (d *geoDB) loadCustom(data []byte, path string) error {
	searcher, err := newSearcher(data)
	if err != nil {
		return err
	}
	updated := ""
	if st, e := os.Stat(path); e == nil {
		updated = st.ModTime().Format(time.RFC3339)
	}
	d.apply(searcher, DBInfo{
		Source:    "custom",
		Path:      path,
		Size:      int64(len(data)),
		SHA256:    sum256(data),
		UpdatedAt: updated,
		Loaded:    true,
	})
	return nil
}

func (d *geoDB) apply(s *xdb.Searcher, info DBInfo) {
	d.mu.Lock()
	if s != nil {
		d.searcher = s
	}
	d.info = info
	d.mu.Unlock()
}

func (d *geoDB) snapshot() DBInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.info
}

// newSearcher 校验并构造查询器：既要能解析 xdb 头，也要能实际查出结果，
// 避免上传了截断或格式错误的文件后 IP 归属全部变空。
func newSearcher(data []byte) (*xdb.Searcher, error) {
	if len(data) < 1024 {
		return nil, errors.New("文件过小，不是有效的 ip2region xdb 文件")
	}
	s, err := xdb.NewWithBuffer(xdb.IPv4, data)
	if err != nil {
		return nil, fmt.Errorf("不是有效的 ip2region v4 xdb 文件: %w", err)
	}
	ok := false
	for _, ip := range probeIPs {
		if region, e := s.Search(ip); e == nil && region != "" {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errors.New("库校验失败：样本 IP 查询无结果，请确认为 ip2region v4（IPv4）格式")
	}
	return s, nil
}

func sum256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
