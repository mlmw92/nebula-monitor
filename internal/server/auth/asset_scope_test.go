package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 业务范围（资产标签维度）的用例。
//
// 这一组为什么要单独存在：它是**授权的第二个维度**，错法的后果都是"权限静默放大/缩小"——
// 放大（把"限定了却没有选择器"当成"不限"）等于越权；缩小（把"未配置"当成"无权限"）等于
// 升级之后一堆账号突然什么都看不到。两种都不会报错，只能靠用例钉住。

func roleLookupOf(roles ...Role) func(string) (Role, bool) {
	m := make(map[string]Role, len(roles))
	for _, r := range roles {
		m[r.Name] = r
	}
	return func(name string) (Role, bool) {
		r, ok := m[name]
		return r, ok
	}
}

// newStoreWithLabelKey 用给定的约定标签键建一个 Store（该键属于授权数据文件）。
func newStoreWithLabelKey(t *testing.T, key string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("scope_label_key: %s\n", key)), 0o600); err != nil {
		t.Fatalf("写入数据文件失败: %v", err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

func TestScopeAssetModeThreeStates(t *testing.T) {
	// ① 未配置（存量配置的形态：字段缺省）→ 该维度不生效
	unset := Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}
	if unset.LimitsAssets() {
		t.Fatal("未配置业务维度时不应限制资产")
	}
	if d := ResolveAssetScope(&Principal{Scope: unset}); d.Deny || d.Selectors != nil {
		t.Fatalf("未配置应得到「不限制」：%+v", d)
	}

	// ② limited + 选择器 → 按选择器限制
	limited := Scope{
		Mode: ScopeRestricted, AssetMode: AssetScopeLimited,
		AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}},
	}
	if !limited.LimitsAssets() {
		t.Fatal("limited 模式应生效")
	}
	d := ResolveAssetScope(&Principal{Scope: limited})
	if d.Deny || len(d.Selectors) != 1 || d.Selectors[0].Value != "pay" {
		t.Fatalf("限定模式应给出选择器：%+v", d)
	}

	// ③ limited 但一个选择器都没有 → **无权限**（与节点维度 restricted+空 逐字同构，绝不放大）
	emptyLimited := Scope{Mode: ScopeRestricted, AssetMode: AssetScopeLimited}
	d = ResolveAssetScope(&Principal{Scope: emptyLimited})
	if !d.Deny {
		t.Fatalf("限定了却没有选择器必须是无权限，不能当成「不限」：%+v", d)
	}

	// ④ 无 Principal（未启用认证/单管理员）→ 不受限，与 nodeInScope 的既有取向一致
	if d := ResolveAssetScope(nil); d.Deny || d.Selectors != nil {
		t.Fatalf("无身份时不应限制：%+v", d)
	}
}

func TestValidateAssetScope(t *testing.T) {
	ok := func(name string, mode string, labels []AssetScope, key string) {
		t.Helper()
		if err := ValidateAssetScope(mode, labels, key); err != nil {
			t.Fatalf("%s：应通过，实际 %v", name, err)
		}
	}
	bad := func(name string, mode string, labels []AssetScope, key string) {
		t.Helper()
		if err := ValidateAssetScope(mode, labels, key); err == nil {
			t.Fatalf("%s：应被拒绝", name)
		}
	}
	sel := func(k, v string) AssetScope { return AssetScope{Key: k, Value: v} }

	ok("空模式（= all）", "", nil, "")
	ok("显式 all", AssetScopeAll, nil, "")
	ok("limited + 一个选择器", AssetScopeLimited, []AssetScope{sel("biz", "pay")}, "")
	// 允许"限定了但一个都没有"：它的语义是无权限（fail-closed），是合法配置
	ok("limited + 空列表", AssetScopeLimited, nil, "")
	ok("部署方自定义键", AssetScopeLimited, []AssetScope{sel("env", "prod")}, "env")
	ok("值含中文（标签是给人看的）", AssetScopeLimited, []AssetScope{sel("biz", "支付")}, "")

	bad("模式非法", "partial", nil, "")
	bad("all 却带选择器（自相矛盾）", AssetScopeAll, []AssetScope{sel("biz", "pay")}, "")
	bad("空模式却带选择器", "", []AssetScope{sel("biz", "pay")}, "")
	bad("用了非约定键", AssetScopeLimited, []AssetScope{sel("env", "prod")}, "biz")
	bad("值空", AssetScopeLimited, []AssetScope{sel("biz", "")}, "")
	bad("键空", AssetScopeLimited, []AssetScope{sel("", "pay")}, "")
	bad("值含换行", AssetScopeLimited, []AssetScope{sel("biz", "a\nb")}, "")
	bad("值过长", AssetScopeLimited, []AssetScope{sel("biz", strings.Repeat("x", MaxAssetScopeValueLen+1))}, "")
}

func TestExpandPrincipalAssetScope(t *testing.T) {
	pay := Role{Name: "r_pay", ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
		AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}}
	risk := Role{Name: "r_risk", ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
		AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "risk"}}}
	unlimited := Role{Name: "r_all", ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"}}

	t.Run("单角色限定", func(t *testing.T) {
		p := ExpandPrincipal(User{Username: "u", Roles: []string{"r_pay"}}, roleLookupOf(pay))
		if !p.Scope.LimitsAssets() {
			t.Fatal("应受业务维度限制")
		}
		if got := p.Scope.AssetSelectors(); len(got) != 1 || got[0].Value != "pay" {
			t.Fatalf("选择器不符：%+v", got)
		}
	})

	t.Run("任一角色不限则该维度不生效", func(t *testing.T) {
		p := ExpandPrincipal(User{Username: "u", Roles: []string{"r_pay", "r_all"}}, roleLookupOf(pay, unlimited))
		if p.Scope.LimitsAssets() {
			t.Fatal("有一个角色不限业务范围时，该维度对该用户就不生效（与节点维度「任一 global 即 global」同构）")
		}
	})

	t.Run("多个限定角色取并集且去重有序", func(t *testing.T) {
		p := ExpandPrincipal(User{Username: "u", Roles: []string{"r_pay", "r_risk", "r_pay"}}, roleLookupOf(pay, risk))
		got := p.Scope.AssetSelectors()
		if len(got) != 2 || got[0].Value != "pay" || got[1].Value != "risk" {
			t.Fatalf("应是去重且有序的并集：%+v", got)
		}
	})

	t.Run("用户自身的业务范围并入选择器", func(t *testing.T) {
		u := User{Username: "u", Roles: []string{"r_pay"},
			Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"},
				AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "risk"}}}}
		p := ExpandPrincipal(u, roleLookupOf(pay))
		if got := p.Scope.AssetSelectors(); len(got) != 2 {
			t.Fatalf("用户范围应与角色范围并集：%+v", got)
		}
	})

	// 这条是本组里最容易被写错的一条：普通用户 scope 的 AssetMode 默认是空（= all），
	// 如果让它参与"是否关掉维度"的判断，这个维度就永远关不掉了（角色的限制被静默抵消）。
	t.Run("用户 scope 的默认空模式不能把维度关掉", func(t *testing.T) {
		u := User{Username: "u", Roles: []string{"r_pay"}, Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}}
		p := ExpandPrincipal(u, roleLookupOf(pay))
		if !p.Scope.LimitsAssets() {
			t.Fatal("用户自身未配业务范围时，不能取消角色强加的业务限制")
		}
		if got := p.Scope.AssetSelectors(); len(got) != 1 || got[0].Value != "pay" {
			t.Fatalf("应保留角色的选择器：%+v", got)
		}
	})

	t.Run("角色不限而用户限定：仍受限（并集里没有「不限」以外的东西）", func(t *testing.T) {
		u := User{Username: "u", Roles: []string{"r_all"},
			Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"},
				AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}}}
		p := ExpandPrincipal(u, roleLookupOf(unlimited))
		if !p.Scope.LimitsAssets() {
			t.Fatal("用户自己配了业务范围就该生效")
		}
	})

	t.Run("存量账号行为不变", func(t *testing.T) {
		p := ExpandPrincipal(User{Username: "u", Roles: []string{"r_all"}}, roleLookupOf(unlimited))
		if p.Scope.LimitsAssets() || len(p.Scope.AssetLabels) != 0 {
			t.Fatalf("未配置业务范围的账号不应受该维度影响：%+v", p.Scope)
		}
	})
}

func TestScopeCovers(t *testing.T) {
	pay := func(v string) Scope {
		return Scope{Mode: ScopeRestricted, Groups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: v}}}
	}
	global := Scope{Mode: ScopeGlobal}

	if err := ScopeCovers(global, pay("pay")); err != nil {
		t.Fatalf("全局操作者应覆盖任何范围：%v", err)
	}
	if err := ScopeCovers(Scope{Mode: ScopeRestricted, Groups: []string{"g1", "g2"}}, Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}); err != nil {
		t.Fatalf("超集应覆盖子集：%v", err)
	}
	if err := ScopeCovers(Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}, Scope{Mode: ScopeRestricted, Groups: []string{"g1", "g2"}}); err == nil {
		t.Fatal("节点维度超出应被拒绝")
	}
	// 业务维度：操作者限定，就不能授予"不限"（那是把自己没有的权限给出去）
	if err := ScopeCovers(pay("pay"), Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}); err == nil {
		t.Fatal("受限操作者不应能授予不受业务限制的范围")
	}
	if err := ScopeCovers(pay("pay"), pay("risk")); err == nil {
		t.Fatal("业务范围超出应被拒绝")
	}
	if err := ScopeCovers(pay("pay"), pay("pay")); err != nil {
		t.Fatalf("相同的业务范围应被覆盖：%v", err)
	}
}

func TestStoreAssetScope(t *testing.T) {
	t.Run("默认约定键是 biz", func(t *testing.T) {
		s, _ := newTempStore(t)
		if got := s.ScopeLabelKey(); got != DefaultScopeLabelKey {
			t.Fatalf("默认键应为 %q，实际 %q", DefaultScopeLabelKey, got)
		}
		if err := s.CreateRole(Role{Name: "r_env", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "env", Value: "prod"}}},
			"admin", Scope{Mode: ScopeGlobal}); err == nil {
			t.Fatal("用了非约定键应被拒绝（只认一个约定键）")
		}
		if err := s.CreateRole(Role{Name: "r_pay", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}},
			"admin", Scope{Mode: ScopeGlobal}); err != nil {
			t.Fatalf("合法业务范围应可创建：%v", err)
		}
	})

	t.Run("部署方可改约定键", func(t *testing.T) {
		s, _ := newStoreWithLabelKey(t, "env")
		if got := s.ScopeLabelKey(); got != "env" {
			t.Fatalf("应读到配置的键，实际 %q", got)
		}
		if err := s.CreateRole(Role{Name: "r_biz", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}},
			"admin", Scope{Mode: ScopeGlobal}); err == nil {
			t.Fatal("改键之后旧的 biz 选择器应被拒绝")
		}
		if err := s.CreateRole(Role{Name: "r_env", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "env", Value: "prod"}}},
			"admin", Scope{Mode: ScopeGlobal}); err != nil {
			t.Fatalf("新键应可用：%v", err)
		}
	})

	t.Run("落盘并重载后仍生效", func(t *testing.T) {
		s, path := newTempStore(t)
		r := Role{Name: "r_pay", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}}
		if err := s.CreateRole(r, "admin", Scope{Mode: ScopeGlobal}); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		if err := s.CreateUser(User{Username: "user1", Roles: []string{"r_pay"}, Status: StatusEnabled,
			Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}}, "Passw0rd!", "admin"); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		p, ok := s.VerifyPassword("user1", "Passw0rd!")
		if !ok || p == nil {
			t.Fatal("校验密码应成功")
		}
		if !p.Scope.LimitsAssets() || p.Scope.AssetSelectors()[0].Value != "pay" {
			t.Fatalf("展开后的业务范围不符：%+v", p.Scope)
		}

		// 重新打开同一个文件：业务范围必须还在（否则升级/重启就是一次静默放大）
		again, err := NewStore(path)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		p2, _ := again.VerifyPassword("user1", "Passw0rd!")
		if p2 == nil || !p2.Scope.LimitsAssets() || p2.Scope.AssetSelectors()[0].Value != "pay" {
			t.Fatalf("重载后业务范围丢失：%+v", p2.Scope)
		}
	})

	t.Run("更新：模式与选择器一起校验", func(t *testing.T) {
		s, _ := newTempStore(t)
		if err := s.CreateRole(Role{Name: "r_x", Permissions: []string{"assets:read"},
			ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"},
			AssetMode: AssetScopeLimited, AssetLabels: []AssetScope{{Key: "biz", Value: "pay"}}},
			"admin", Scope{Mode: ScopeGlobal}); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		// 把模式改成 all 却留着选择器：自相矛盾，必须拒绝
		all := AssetScopeAll
		if err := s.UpdateRole("r_x", RolePatch{AssetMode: &all}); err == nil {
			t.Fatal("模式改 all 却留着选择器应被拒绝")
		}
		// 一起改（模式下 + 清空选择器）才合法
		if err := s.UpdateRole("r_x", RolePatch{AssetMode: &all, AssetLabels: []AssetScope{}}); err != nil {
			t.Fatalf("同时清掉选择器应通过：%v", err)
		}
		r, _ := s.LookupRole("r_x")
		if r.RoleScope().LimitsAssets() {
			t.Fatal("改完之后业务维度应不再生效")
		}
	})
}
