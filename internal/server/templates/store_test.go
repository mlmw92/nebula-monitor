package templates

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	dsl "github.com/nebula/monitor/internal/template"
)

// validCfg 返回一个最小合法模板（含 Server 侧要求的 groups）。
func validCfg(id string) dsl.Config {
	return dsl.Config{
		ID:     id,
		Kind:   dsl.KindPrometheusExporter,
		Groups: []string{"default"},
		Targets: []dsl.Target{
			{Instance: id + "-01:15692", Addr: "http://127.0.0.1:15692/metrics"},
		},
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "templates.yaml")
	return NewStore(path), path
}

func TestStore_CreateListGetDelete(t *testing.T) {
	s, _ := newTestStore(t)

	if err := s.Upsert(validCfg("rabbitmq")); err != nil {
		t.Fatalf("新增应成功：%v", err)
	}
	if err := s.Upsert(validCfg("clickhouse")); err != nil {
		t.Fatalf("新增应成功：%v", err)
	}

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("列表长度 = %d，want 2", len(list))
	}
	// 顺序按写入顺序保持，前端展示与 diff 才稳定
	if list[0].ID != "rabbitmq" || list[1].ID != "clickhouse" {
		t.Fatalf("列表顺序不稳定：%v", []string{list[0].ID, list[1].ID})
	}

	if got, ok := s.Get("rabbitmq"); !ok || got.ID != "rabbitmq" {
		t.Fatalf("Get 失败：%+v ok=%v", got, ok)
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get 不存在的模板应返回 false")
	}

	if err := s.Delete("rabbitmq"); err != nil {
		t.Fatalf("删除应成功：%v", err)
	}
	if _, ok := s.Get("rabbitmq"); ok {
		t.Fatal("删除后仍能取到")
	}
	if err := s.Delete("rabbitmq"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除不存在的模板应返回 ErrNotExist，got %v", err)
	}
}

// TestStore_UpdateKeepsOrder 更新不应打乱列表顺序（同 id 覆盖）。
func TestStore_UpdateKeepsOrder(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []string{"aaa", "bbb", "ccc"} {
		if err := s.Upsert(validCfg(id)); err != nil {
			t.Fatalf("新增 %s 失败：%v", id, err)
		}
	}
	updated := validCfg("bbb")
	updated.Title = "改过的标题"
	if err := s.Upsert(updated); err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	ids := s.ListIDs()
	if strings.Join(ids, ",") != "aaa,bbb,ccc" {
		t.Fatalf("更新后顺序被改动：%v", ids)
	}
	if got, _ := s.Get("bbb"); got.Title != "改过的标题" {
		t.Fatalf("更新未生效：%+v", got)
	}
}

// TestStore_UpsertRejectsInvalid 校验在保存路径上必须挡住非法配置（否则会下发到 Agent 被拒）。
func TestStore_UpsertRejectsInvalid(t *testing.T) {
	s, path := newTestStore(t)
	if err := s.Upsert(validCfg("rabbitmq")); err != nil {
		t.Fatalf("新增失败：%v", err)
	}

	badID := validCfg("Bad-ID")
	// id 与既有指标族前缀冲突
	reservedPrefix := validCfg("redis")
	// rabbitmq 已存在，rabbitmq_prod 与它互为前缀（指标名无法分辨归属）
	prefixOverlap := validCfg("rabbitmq_prod")
	// http-json 必须配 rules.metrics
	missingRules := validCfg("ownapp")
	missingRules.Kind = dsl.KindHTTPJSON

	cases := []struct {
		name string
		cfg  dsl.Config
	}{
		{"id 非法", badID},
		{"id 与保留指标族冲突", reservedPrefix},
		{"id 与既有模板互为前缀", prefixOverlap},
		{"缺 rules.metrics", missingRules},
	}
	for _, tc := range cases {
		if err := s.Upsert(tc.cfg); err == nil {
			t.Errorf("%s：应被拒绝", tc.name)
		}
	}

	if len(s.List()) != 1 {
		t.Fatalf("被拒的配置不应落盘，当前条数 = %d", len(s.List()))
	}
	// 被拒后也不应污染磁盘
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取配置文件失败：%v", err)
	}
	var list []dsl.Config
	if err := yaml.Unmarshal(raw, &list); err != nil {
		t.Fatalf("配置文件解析失败：%v", err)
	}
	if len(list) != 1 || list[0].ID != "rabbitmq" {
		t.Fatalf("磁盘内容被非法配置污染：%+v", list)
	}
}

// TestStore_RequiresGroups Server 侧附加规则：由 Server 下发的模板必须声明生效分组。
//
// 若允许留空而默认「全部节点」，一台只跑某中间件的机器配一个模板，
// 会让其余节点每轮各报一个 template_target_up=0（序列与日志双噪声）。
// 注意这条规则**不在共享 DSL 里**：agent.yaml 的本机模板天然只对本机生效，不该被迫填它。
func TestStore_RequiresGroups(t *testing.T) {
	s, _ := newTestStore(t)

	noGroups := validCfg("rabbitmq")
	noGroups.Groups = nil
	if err := s.Upsert(noGroups); err == nil {
		t.Fatal("缺 groups 应被 Server 侧拒绝")
	} else if !strings.Contains(err.Error(), "groups") {
		t.Fatalf("错误信息应指明 groups，got %v", err)
	}

	// 共享 DSL 单独看是合法的（可选字段），差别只在 Server 侧规则
	if err := dsl.ValidateAll([]dsl.Config{noGroups}); err != nil {
		t.Fatalf("DSL 层不应要求 groups（agent.yaml 本机模板无需填写）：%v", err)
	}
	if err := s.Validate(noGroups); err == nil {
		t.Fatal("Validate 也应报出缺 groups（编辑器要能提前发现）")
	}

	// 格式问题由共享 DSL 负责
	badGroup := validCfg("rabbitmq")
	badGroup.Groups = []string{"mq", " "}
	if err := s.Upsert(badGroup); err == nil {
		t.Fatal("groups 含空项应被拒绝")
	}
	dupGroup := validCfg("rabbitmq")
	dupGroup.Groups = []string{"mq", "mq"}
	if err := s.Upsert(dupGroup); err == nil {
		t.Fatal("groups 重复应被拒绝")
	}
}

// TestStore_ValidateDoesNotPersist 仅校验不得落盘（前端编辑器的「校验」按钮不能有副作用）。
func TestStore_ValidateDoesNotPersist(t *testing.T) {
	s, path := newTestStore(t)
	if err := s.Validate(validCfg("rabbitmq")); err != nil {
		t.Fatalf("合法配置校验应通过：%v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("Validate 不应写入存储")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Validate 不应创建配置文件")
	}
	// 跨条约束也要在这里被报出：与既有模板互为前缀
	if err := s.Upsert(validCfg("mq")); err != nil {
		t.Fatalf("新增失败：%v", err)
	}
	if err := s.Validate(validCfg("mq_prod")); err == nil {
		t.Fatal("Validate 应报出与既有模板互为前缀")
	}
}

func TestStore_RevisionBumpsOnChange(t *testing.T) {
	s, _ := newTestStore(t)
	start := s.Revision()
	if err := s.Upsert(validCfg("rabbitmq")); err != nil {
		t.Fatalf("新增失败：%v", err)
	}
	afterCreate := s.Revision()
	if afterCreate == start {
		t.Fatal("新增后版本号应递增（下发端据此判断是否需要重发）")
	}
	if err := s.Delete("rabbitmq"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if s.Revision() == afterCreate {
		t.Fatal("删除后版本号应递增")
	}
}

// TestStore_PersistAndReload 落盘后可被新实例加载（重启不丢配置）。
func TestStore_PersistAndReload(t *testing.T) {
	s, path := newTestStore(t)
	if err := s.Upsert(validCfg("rabbitmq")); err != nil {
		t.Fatalf("新增失败：%v", err)
	}
	if err := s.Upsert(validCfg("clickhouse")); err != nil {
		t.Fatalf("新增失败：%v", err)
	}

	reloaded := NewStore(path)
	ids := reloaded.ListIDs()
	if strings.Join(ids, ",") != "rabbitmq,clickhouse" {
		t.Fatalf("重载后内容不符：%v", ids)
	}
	if got, ok := reloaded.Get("rabbitmq"); !ok || len(got.Targets) != 1 {
		t.Fatalf("重载后条目内容不符：%+v", got)
	}
}

// TestStore_LoadBrokenFileStillStarts 手工改坏配置文件不应让 Server 起不来：
// 只告警并加载能用的部分，管理员需要能进 UI 修（与 Agent 侧 fail-fast 刻意不同）。
func TestStore_LoadBrokenFileStillStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "templates.yaml")
	if err := os.WriteFile(path, []byte("这不是 YAML: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if len(s.List()) != 0 {
		t.Fatalf("无法解析的文件不应加载出内容，got %d", len(s.List()))
	}

	// 非法条目（id 与保留前缀冲突）同样只告警、不阻断启动
	bad, err := yaml.Marshal([]dsl.Config{validCfg("redis")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(path)
	if len(s2.List()) != 1 {
		t.Fatalf("非法条目仍应加载（便于在 Web 端修正），got %d", len(s2.List()))
	}
}

// TestStore_ConcurrentAccess 并发读写不应竞态（CI 以 -race 运行 Server 侧包）。
func TestStore_ConcurrentAccess(t *testing.T) {
	s, _ := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := []string{"rabbitmq", "clickhouse", "etcd", "zookeeper"}[i%4]
			_ = s.Upsert(validCfg(id))
			_ = s.List()
			_ = s.Revision()
			_, _ = s.Snapshot()
		}(i)
	}
	wg.Wait()
	if len(s.List()) == 0 {
		t.Fatal("并发写入后不应为空")
	}
}
