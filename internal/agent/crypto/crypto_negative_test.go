package crypto

import (
	"strings"
	"testing"
)

// TestIsEncrypted 加密标记的识别（旧明文配置必须原样通过，不能被误判为密文）。
func TestIsEncrypted(t *testing.T) {
	cases := map[string]bool{
		"enc:abc":                 true,
		"":                        false,
		"plain-password":          false,
		"enc":                     false, // 缺冒号
		"ENC:abc":                 false, // 前缀区分大小写
		"encrypted-looking-value": false,
		" enc:abc":                false, // 前导空格
	}
	for in, want := range cases {
		if got := IsEncrypted(in); got != want {
			t.Errorf("IsEncrypted(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestDecryptRejectsTamperedCiphertext 密文被改动任意字符都必须失败：
// 完整性由 SM3-HMAC 保证，静默解出错误明文会把中间件密码改坏且难以定位。
func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	c, err := NewCipher(nil)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	enc, err := c.Encrypt("Passw0rd!")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, "enc:") {
		t.Fatalf("密文应带 enc: 前缀: %q", enc)
	}

	body := strings.TrimPrefix(enc, "enc:")
	// 翻转 body 中间的一个字符（避开前缀，保证仍是合法 base64 长度）
	i := len(body) / 2
	flip := byte('A')
	if body[i] == 'A' {
		flip = 'B'
	}
	tampered := "enc:" + body[:i] + string(flip) + body[i+1:]

	if got, err := c.Decrypt(tampered); err == nil {
		t.Fatalf("篡改密文应报错，却解出 %q", got)
	}
}

// TestDecryptRejectsMalformedInput base64 非法或长度不足时给出错误而非 panic。
func TestDecryptRejectsMalformedInput(t *testing.T) {
	c, err := NewCipher(nil)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	for _, in := range []string{
		"enc:!!!not-base64!!!",
		"enc:",
		"enc:AAAA",
		"enc:YWJj",
	} {
		got, err := c.Decrypt(in)
		if err == nil {
			t.Errorf("Decrypt(%q) 应报错，却返回 %q", in, got)
		}
	}
}

// TestNewCipherKeyLength 密钥补齐/截断到 16 字节（SM4 要求），不同密钥不得互通。
func TestNewCipherKeyLength(t *testing.T) {
	short, err := NewCipher([]byte("short"))
	if err != nil {
		t.Fatalf("短密钥应被补齐: %v", err)
	}
	long, err := NewCipher([]byte("this-key-is-way-longer-than-16-bytes"))
	if err != nil {
		t.Fatalf("长密钥应被截断: %v", err)
	}
	enc, err := short.Encrypt("Passw0rd!")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := long.Decrypt(enc); err == nil {
		t.Fatal("不同密钥不应能解密对方的密文")
	}
}

// TestEncryptProducesDistinctCiphertext 相同明文两次加密应产生不同密文（随机 IV），
// 否则密文可比对，会泄露「两个实例用了同一密码」。
func TestEncryptProducesDistinctCiphertext(t *testing.T) {
	c, err := NewCipher(nil)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	a, err := c.Encrypt("same-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	b, err := c.Encrypt("same-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if a == b {
		t.Fatal("相同明文的两次加密结果相同，IV 未随机化")
	}
	for _, in := range []string{a, b} {
		got, err := c.Decrypt(in)
		if err != nil || got != "same-secret" {
			t.Fatalf("解密失败: got=%q err=%v", got, err)
		}
	}
}
