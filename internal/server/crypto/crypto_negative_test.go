package crypto

import "testing"

// TestVerifyPasswordRejectsMalformedHash 畸形哈希必须一律返回 false：
// 这些值可能来自被截断/损坏的 users.yaml，绝不能因为解析失败而放行。
func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, stored := range []string{
		"sm3:",
		"sm3:00",
		"sm3:00:",
		"sm3::ff",
		"sm3:00:ff",     // 长度不足的摘要
		"sm3:zz:ff",     // salt 非 hex
		"sm3:00:zz",     // 摘要非 hex
		"sm3::",         // 两段都空
		"sm3:abcd:abcd", // 长度不对
	} {
		if VerifyPassword(stored, "anything") {
			t.Errorf("VerifyPassword(%q, ...) 应为 false", stored)
		}
	}
}

// TestVerifyPasswordRejectsWrongPassword 正确哈希 + 错误口令必须失败。
func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	hashed, err := HashPassword("Passw0rd!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hashed, "Passw0rd!") {
		t.Fatal("正确口令应通过")
	}
	if VerifyPassword(hashed, "passw0rd!") || VerifyPassword(hashed, "") {
		t.Fatal("错误口令不应通过")
	}
}

// TestHashPasswordIsSalted 相同口令两次哈希必须不同（随机盐），
// 否则可用彩虹表反查，且能看出两个用户用了同一口令。
func TestHashPasswordIsSalted(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Fatal("相同口令的两次哈希结果相同，盐未随机化")
	}
	for _, h := range []string{a, b} {
		if !IsHashed(h) {
			t.Fatalf("哈希结果应被 IsHashed 识别: %q", h)
		}
		if !VerifyPassword(h, "same-password") {
			t.Fatalf("哈希结果应可校验: %q", h)
		}
	}
}

// TestIsHashed_PrefixStrictness 仅严格匹配 sm3: 前缀（大小写与前导空格都不算），
// 判断宽松会让旧明文口令被当成哈希去校验，直接导致无法登录。
func TestIsHashed_PrefixStrictness(t *testing.T) {
	cases := map[string]bool{
		"sm3:00:ff":  true,
		"plaintext":  false,
		"":           false,
		"SM3:00:ff":  false,
		" sm3:00:ff": false,
	}
	for in, want := range cases {
		if got := IsHashed(in); got != want {
			t.Errorf("IsHashed(%q) = %v, want %v", in, got, want)
		}
	}
}
