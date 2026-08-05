// Package crypto 提供 Agent 侧中间件连接密码的静态加密存储能力。
// 中间件密码需还原为明文以建立数据库连接，因此采用 AES-GCM 对称加密。
// 密文约定以 "enc:" 前缀标识，未加前缀的视为旧明文配置（向后兼容）。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// encPrefix 密文前缀，用于区分加密值与旧明文值。
const encPrefix = "enc:"

// DefaultKey 返回内置派生的默认主密钥。
// Agent 通常无人工配置密钥能力，内置常量保证开箱即用、能解密历史密文。
// 用户可在 agent.yaml 的 cryptoKey 字段配置自定义密钥提升安全性。
// 注意：默认密钥仅作「配置单文件泄露/只读挂载」场景的混淆防护，
// 最强的防护来自自定义 cryptoKey（同样存于本机 agent.yaml，与需求一致）。
var defaultKey = deriveDefaultKey()

func deriveDefaultKey() []byte {
	// 由固定种子派生的 32 字节密钥（AES-256）。值编译进二进制，仅用于加密本机配置。
	seed := []byte("nebula-monitor-agent-static-key-v1-32bytes!!")
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed[i%len(seed)] ^ byte(0x5A+i)
	}
	return k
}

// Cipher 封装 AES-GCM 的加解密。
type Cipher struct {
	gcm cipher.AEAD
}

// NewCipher 以给定密钥构造 Cipher；密钥为空时回退到内置默认密钥。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) == 0 {
		key = defaultKey
	}
	// 允许任意长度密钥：不足 32 字节右补齐，超过则截断，保证 NewCipher 不报错。
	if len(key) < 32 {
		padded := make([]byte, 32)
		copy(padded, key)
		key = padded
	} else if len(key) > 32 {
		key = key[:32]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{gcm: gcm}, nil
}

// Encrypt 将明文加密为 "enc:" + base64(ciphertext) 形式。
func (c *Cipher) Encrypt(plain string) (string, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := c.gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解析密文；识别 "enc:" 前缀则解密，否则原样返回（旧明文兼容）。
// 解密失败返回错误，由调用方决定是否告警并保留原值。
func (c *Cipher) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, nil // 旧明文配置，直接返回
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", err
	}
	ns := c.gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// IsEncrypted 判断给定字符串是否为本包加密密文。
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encPrefix)
}
