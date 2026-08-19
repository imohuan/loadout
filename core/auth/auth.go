// Package auth 实现 Loadout 的认证基础能力：
//   - bcrypt 密码哈希与校验（管理后台登录）；
//   - HMAC-SHA256 签发的 JWT 会话（HttpOnly Cookie）；
//   - sk- key / MCP key 的签发与 sha256 哈希校验（完整 key 只展示一次，落盘只存哈希）。
//
// 设计依据见 DESIGN.md 第 6 节「认证体系」。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// SessionCookieName 管理后台会话 Cookie 名。
const SessionCookieName = "loadout_session"

// upperLetters 密码字符集：大写字母。
const upperLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// lowerLetters 密码字符集：小写字母。
const lowerLetters = "abcdefghijklmnopqrstuvwxyz"

// digits 密码字符集：数字。
const digits = "0123456789"

// symbols 密码字符集：符号（补齐第四类字符，满足「至少 4 类字符混合」）。
const symbols = "!@#$%^&*()-_=+[]{}"

// passwordAlphabet 完整密码字符集（大写、小写、数字、符号四类）。
const passwordAlphabet = upperLetters + lowerLetters + digits + symbols

// GeneratePassword 生成 n 字符随机密码（大写、小写、数字、符号四类混合）。
//
// 使用 crypto/rand 从字母表均匀取样（拒绝采样，无取模偏差），
// 并保证四类字符至少各出现一次，n 必须 >= 4。
func GeneratePassword(n int) (string, error) {
	if n < 4 {
		return "", fmt.Errorf("auth: 密码长度至少为 4（需混合大写、小写、数字、符号四类字符）")
	}

	groups := []string{upperLetters, lowerLetters, digits, symbols}
	result := make([]byte, n)

	// 先保证四类字符各至少一个。
	for i, group := range groups {
		idx, err := randomIndex(len(group))
		if err != nil {
			return "", fmt.Errorf("auth: 生成随机密码失败: %w", err)
		}
		result[i] = group[idx]
	}

	// 其余位置从完整字符集均匀取样。
	for i := len(groups); i < n; i++ {
		idx, err := randomIndex(len(passwordAlphabet))
		if err != nil {
			return "", fmt.Errorf("auth: 生成随机密码失败: %w", err)
		}
		result[i] = passwordAlphabet[idx]
	}

	// Fisher-Yates 洗牌，避免前四个位置固定按类别排列。
	for i := n - 1; i > 0; i-- {
		j, err := randomIndex(i + 1)
		if err != nil {
			return "", fmt.Errorf("auth: 生成随机密码失败: %w", err)
		}
		result[i], result[j] = result[j], result[i]
	}

	return string(result), nil
}

// randomIndex 返回 [0, max) 内的均匀随机整数（crypto/rand 拒绝采样）。
func randomIndex(max int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}

// HashPassword 用 bcrypt（默认 cost）哈希密码。
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: bcrypt 哈希密码失败: %w", err)
	}
	return string(hash), nil
}

// CheckPassword 校验密码是否匹配 bcrypt 哈希。
// hash 为空或非法、密码不匹配均返回 false，不会 panic。
func CheckPassword(hash, plain string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// Claims 管理后台会话 JWT 载荷。
type Claims struct {
	Username string `json:"username"` // Username 登录用户名。
	jwt.RegisteredClaims
}

// SignToken 用 HMAC-SHA256 签发会话 token（secret 来自 store.SecretKey()）。
// 设置 Subject=username、ExpiresAt=now+ttl、IssuedAt=now。
func SignToken(secret []byte, username string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("auth: 签发 JWT 失败: %w", err)
	}
	return signed, nil
}

// ParseToken 校验并解析会话 token。
// 签名方法非 HS256、签名无效、token 过期或格式错误均返回错误。
func ParseToken(secret []byte, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("auth: 非预期的签名方法: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: 解析 JWT 失败: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("auth: JWT 无效")
	}
	return claims, nil
}

// GenerateSecretKey 生成完整 key（prefix + 随机段）与 sha256 十六进制哈希。
//
// 完整 key 只在创建时展示一次，落盘只存 hash。prefix 如 "sk-"。
// 随机段为 32 字节 crypto/rand 的 hex 编码（64 字符）。
func GenerateSecretKey(prefix string) (full, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: 生成密钥随机段失败: %w", err)
	}
	full = prefix + hex.EncodeToString(buf)
	hash = HashSecretKey(full)
	return full, hash, nil
}

// HashSecretKey 计算完整 key 的 sha256 十六进制哈希（用于校验与只存哈希）。
func HashSecretKey(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}
