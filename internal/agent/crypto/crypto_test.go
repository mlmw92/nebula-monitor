package crypto

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewCipher(nil) // 默认密钥
	if err != nil {
		t.Fatalf("NewCipher error: %v", err)
	}
	plain := "my-redis-p@ssw0rd!"
	enc, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}
	if !IsEncrypted(enc) {
		t.Fatalf("expected enc: prefix, got %q", enc)
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if dec != plain {
		t.Fatalf("round trip mismatch: %q != %q", dec, plain)
	}
}

func TestDecryptPlaintextPassthrough(t *testing.T) {
	c, _ := NewCipher(nil)
	// 旧明文配置：无 enc: 前缀应原样返回
	plain := "legacy-plain-text"
	out, err := c.Decrypt(plain)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if out != plain {
		t.Fatalf("expected passthrough, got %q", out)
	}
}

func TestCustomKeyDiffersFromDefault(t *testing.T) {
	def, _ := NewCipher(nil)
	custom, _ := NewCipher([]byte("user-supplied-long-key-1234567890"))
	encDef, _ := def.Encrypt("secret")
	// 用默认密钥加密的密文，用自定义密钥解密应失败
	if _, err := custom.Decrypt(encDef); err == nil {
		t.Fatalf("expected decrypt failure with wrong key")
	}
}
