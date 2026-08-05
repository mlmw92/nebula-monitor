package crypto

import "testing"

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword error: %v", err)
	}
	if !IsHashed(h) {
		t.Fatalf("expected hash prefix, got %q", h)
	}
	if !VerifyPassword(h, "s3cret-password") {
		t.Fatalf("expected verify true for correct password")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatalf("expected verify false for wrong password")
	}
}

func TestVerifyPlaintextFallback(t *testing.T) {
	// 旧明文配置兼容：非 bcrypt 前缀按明文比对
	if !VerifyPassword("admin", "admin") {
		t.Fatalf("expected plaintext fallback to match")
	}
	if VerifyPassword("admin", "root") {
		t.Fatalf("expected plaintext fallback to reject mismatch")
	}
}

func TestIsHashed(t *testing.T) {
	cases := map[string]bool{
		"$2a$10$abc": true,
		"$2b$10$abc": true,
		"$2y$10$abc": true,
		"admin":      false,
		"":           false,
	}
	for s, want := range cases {
		if IsHashed(s) != want {
			t.Fatalf("IsHashed(%q)=%v want %v", s, !want, want)
		}
	}
}
