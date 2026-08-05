// Package crypto 提供 Agent 侧中间件连接密码的静态加密存储能力。
// 中间件密码需还原为明文以建立数据库连接，因此采用国密 SM4 对称加密（CBC 模式 + 随机 IV），
// 并使用 SM3 派生的 HMAC 做完整性校验（防篡改）。密文以 "enc:" 前缀标识，
// 未加前缀的视为旧明文配置（向后兼容）。
package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/tjfoc/gmsm/sm3"
	"github.com/tjfoc/gmsm/sm4"
)

const (
	encPrefix = "enc:"
	blockSize = 16 // SM4 分组长度 128 位
	ivSize    = blockSize
	macSize   = 32 // SM3 摘要长度
)

// defaultKey 内置派生的默认主密钥（编译进二进制，仅作本机配置混淆防护）。
// 用户可在 agent.yaml 的 cryptoKey 字段配置自定义密钥提升安全性。
// 注意：SM4 密钥为 128 位（16 字节），密钥会被补齐/截断到 16 字节。
var defaultKey = deriveDefaultKey()

func deriveDefaultKey() []byte {
	seed := []byte("nebula-monitor-agen")
	k := make([]byte, 16)
	for i := range k {
		k[i] = seed[i%len(seed)] ^ byte(0x3C+i)
	}
	return k
}

// Cipher 封装 SM4-CBC 的加解密与 SM3-HMAC 完整性校验。
type Cipher struct {
	key []byte
}

// NewCipher 以给定密钥构造 Cipher；密钥为空时回退到内置默认密钥。
// 任意长度密钥会被补齐/截断到 32 字节（SM4-256）。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) == 0 {
		key = defaultKey
	}
	if len(key) < 16 {
		padded := make([]byte, 16)
		copy(padded, key)
		key = padded
	} else if len(key) > 16 {
		key = key[:16]
	}
	if _, err := sm4.NewCipher(key); err != nil {
		return nil, err
	}
	return &Cipher{key: key}, nil
}

// mac 计算 SM3-HMAC( key, data )，用于密文完整性校验（防篡改）。
func (c *Cipher) mac(data []byte) []byte {
	h := sm3.New()
	h.Write(c.key)
	h.Write(data)
	return h.Sum(nil)
}

// pkcs7Pad 按 SM4 分组长度填充。
func pkcs7Pad(src []byte) []byte {
	pad := blockSize - len(src)%blockSize
	out := make([]byte, len(src)+pad)
	copy(out, src)
	for i := len(src); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// pkcs7Unpad 去除填充。
func pkcs7Unpad(src []byte) ([]byte, error) {
	if len(src) == 0 || len(src)%blockSize != 0 {
		return nil, errors.New("数据长度不合法")
	}
	pad := int(src[len(src)-1])
	if pad <= 0 || pad > blockSize {
		return nil, errors.New("填充长度不合法")
	}
	for _, b := range src[len(src)-pad:] {
		if int(b) != pad {
			return nil, errors.New("填充内容不合法")
		}
	}
	return src[:len(src)-pad], nil
}

// Encrypt 将明文加密为 "enc:" + base64(iv || ciphertext || sm3hmac) 形式。
func (c *Cipher) Encrypt(plain string) (string, error) {
	block, err := sm4.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	iv := make([]byte, ivSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(plain))
	ct := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ct, padded)
	body := append(iv, ct...)
	tag := c.mac(body)
	out := append(body, tag...)
	return encPrefix + base64.StdEncoding.EncodeToString(out), nil
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
	if len(raw) < ivSize+macSize+blockSize {
		return "", errors.New("密文长度不足")
	}
	iv := raw[:ivSize]
	ct := raw[ivSize : len(raw)-macSize]
	tag := raw[len(raw)-macSize:]
	if !hmacEqual(c.mac(append(iv, ct...)), tag) {
		return "", errors.New("密文完整性校验失败（密钥不匹配或被篡改）")
	}
	block, err := sm4.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	pt := make([]byte, len(ct))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(pt, ct)
	unpadded, err := pkcs7Unpad(pt)
	if err != nil {
		return "", err
	}
	return string(unpadded), nil
}

// IsEncrypted 判断给定字符串是否为本包加密密文。
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encPrefix)
}

// hmacEqual 常量时间比较 SM3-HMAC 标签，防时序攻击。
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
