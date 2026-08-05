// Package crypto 提供登录密码的安全存储与校验能力。
// 登录密码只用于验证、永不还原明文，因此采用国密 SM3 哈希（带随机 salt，不可逆）。
// 这样即使配置文件 server.yaml 被攻破，也无法反推原始密码。
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/tjfoc/gmsm/sm3"
)

// sm3Prefix 存储值前缀，用于区分国密哈希 / 旧明文。
const sm3Prefix = "sm3:"

// generateSalt 生成 16 字节随机 salt。
func generateSalt() ([]byte, error) {
	s := make([]byte, 16)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

// sm3Hash 计算 salted SM3：SM3(salt || password)。
func sm3Hash(salt []byte, plain string) []byte {
	h := sm3.New()
	h.Write(salt)
	h.Write([]byte(plain))
	return h.Sum(nil)
}

// IsHashed 判断存储值是否为本包的国密哈希格式（sm3: 前缀）。
func IsHashed(s string) bool {
	return strings.HasPrefix(s, sm3Prefix)
}

// HashPassword 将明文密码生成为「sm3:<saltHex>:<hashHex>」格式的国密哈希。
func HashPassword(plain string) (string, error) {
	salt, err := generateSalt()
	if err != nil {
		return "", err
	}
	sum := sm3Hash(salt, plain)
	return sm3Prefix + hex.EncodeToString(salt) + ":" + hex.EncodeToString(sum), nil
}

// VerifyPassword 校验明文密码与存储值是否匹配。
//   - 国密哈希（sm3: 前缀）：用相同 salt 重算 SM3 并做常量时间比较。
//   - 其它（旧明文）：按明文常量时间比较兜底，保证极端场景仍可登录。
func VerifyPassword(stored, plain string) bool {
	if strings.HasPrefix(stored, sm3Prefix) {
		return verifySM3(stored, plain)
	}
	// 旧明文兜底：避免极端场景下（未落盘迁移）无法登录。
	return subtle.ConstantTimeCompare([]byte(stored), []byte(plain)) == 1
}

// verifySM3 校验国密 salted 哈希。
func verifySM3(stored, plain string) bool {
	parts := strings.SplitN(strings.TrimPrefix(stored, sm3Prefix), ":", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	got := sm3Hash(salt, plain)
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ErrInvalidFormat 表示密文/哈希格式不合法。
var ErrInvalidFormat = errors.New("密码存储格式不合法")
