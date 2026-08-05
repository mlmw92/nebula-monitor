// Package crypto 提供登录密码的安全存储与校验能力。
// 登录密码只用于验证、永不还原明文，因此采用 bcrypt 哈希（不可逆）。
// 这样即使配置文件 server.yaml 被攻破，也无法反推原始密码。
package crypto

import (
	"crypto/subtle"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 是哈希计算强度，默认 10（单次校验约 ~100ms，登录为低频操作可忽略）。
const bcryptCost = 10

// IsHashed 判断存储值是否为 bcrypt 哈希（形如 $2a$/ $2b$/ $2y$）。
func IsHashed(s string) bool {
	return strings.HasPrefix(s, "$2a$") ||
		strings.HasPrefix(s, "$2b$") ||
		strings.HasPrefix(s, "$2y$")
}

// HashPassword 将明文密码生成 bcrypt 哈希（含随机盐）。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验明文密码与存储值是否匹配。
// 兼容未迁移的旧明文配置：存储值非 bcrypt 哈希前缀时，按明文做常量时间比较兜底。
func VerifyPassword(stored, plain string) bool {
	if IsHashed(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil
	}
	// 旧明文兜底：避免极端场景下（未落盘迁移）无法登录。
	return subtle.ConstantTimeCompare([]byte(stored), []byte(plain)) == 1
}
