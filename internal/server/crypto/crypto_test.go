package crypto

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword error: %v", err)
	}
	if !strings.HasPrefix(h, "sm3:") {
		t.Fatalf("expected sm3: prefix, got %q", h)
	}
	if !IsHashed(h) {
		t.Fatalf("expected IsHashed true, got %q", h)
	}
	if !VerifyPassword(h, "s3cret-password") {
		t.Fatalf("expected verify true for correct password")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatalf("expected verify false for wrong password")
	}
	// 相同明文两次哈希应不同（随机 salt）
	h2, _ := HashPassword("s3cret-password")
	if h == h2 {
		t.Fatalf("expected different hashes due to random salt")
	}
	if !VerifyPassword(h2, "s3cret-password") {
		t.Fatalf("expected second hash to verify")
	}
}

func TestVerifyPlaintextFallback(t *testing.T) {
	// 旧明文配置兼容：非 sm3: 前缀按明文比对
	if !VerifyPassword("admin", "admin") {
		t.Fatalf("expected plaintext fallback to match")
	}
	if VerifyPassword("admin", "root") {
		t.Fatalf("expected plaintext fallback to reject mismatch")
	}
}

func TestIsHashed(t *testing.T) {
	cases := map[string]bool{
		"sm3:00112233445566778899aabbccddeeff:deadbeef": true,
		"admin": false,
		"":      false,
	}
	for s, want := range cases {
		if IsHashed(s) != want {
			t.Fatalf("IsHashed(%q)=%v want %v", s, !want, want)
		}
	}
}
