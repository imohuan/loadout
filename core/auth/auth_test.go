package auth

import (
	"strings"
	"testing"
	"time"
)

// TestHashAndCheckPassword 验证 bcrypt 哈希与校验：
// 正确密码通过、错误密码不通过、空/非法哈希不 panic。
func TestHashAndCheckPassword(t *testing.T) {
	const plain = "correct-horse-battery"

	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	if hash == "" || hash == plain {
		t.Fatalf("HashPassword 返回值异常: %q", hash)
	}

	if !CheckPassword(hash, plain) {
		t.Error("正确密码应通过校验")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("错误密码不应通过校验")
	}
	// 空哈希与非法哈希都返回 false，且不 panic。
	if CheckPassword("", plain) {
		t.Error("空哈希不应通过校验")
	}
	if CheckPassword("not-a-bcrypt-hash", plain) {
		t.Error("非法哈希不应通过校验")
	}
}

// TestSignAndParseToken 验证 JWT 签发与解析：
// 往返成功、篡改报错、过期报错。
func TestSignAndParseToken(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-key!!")

	token, err := SignToken(secret, "admin", time.Hour)
	if err != nil {
		t.Fatalf("SignToken 失败: %v", err)
	}

	claims, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken 失败: %v", err)
	}
	if claims.Username != "admin" {
		t.Errorf("Username = %q，期望 admin", claims.Username)
	}
	if claims.Subject != "admin" {
		t.Errorf("Subject = %q，期望 admin", claims.Subject)
	}
	if claims.ExpiresAt == nil {
		t.Error("ExpiresAt 不应为 nil")
	}
	if claims.IssuedAt == nil {
		t.Error("IssuedAt 不应为 nil")
	}

	// 篡改签名：改签名段首个字符（6 位均有效，必然破坏校验）。
	// 注意不能改末尾字符——base64url 无填充的末字符存在冗余位，
	// 若替换字符与原字符仅冗余位不同，解码字节不变、校验仍会通过（flaky）。
	dot := strings.LastIndex(token, ".")
	repl := byte('a')
	if token[dot+1] == repl {
		repl = 'b'
	}
	tampered := token[:dot+1] + string(repl) + token[dot+2:]
	if _, err := ParseToken(secret, tampered); err == nil {
		t.Error("篡改后的 token 应解析失败")
	}

	// 过期 token（ttl 为负）。
	expired, err := SignToken(secret, "admin", -time.Second)
	if err != nil {
		t.Fatalf("SignToken 失败: %v", err)
	}
	if _, err := ParseToken(secret, expired); err == nil {
		t.Error("过期 token 应解析失败")
	}

	// 错误密钥。
	if _, err := ParseToken([]byte("wrong-secret"), token); err == nil {
		t.Error("错误密钥解析应失败")
	}
}

// TestGenerateSecretKey 验证密钥签发：
// full 以 prefix 开头、hash 与 HashSecretKey(full) 一致、两次生成不同。
func TestGenerateSecretKey(t *testing.T) {
	full, hash, err := GenerateSecretKey("sk-")
	if err != nil {
		t.Fatalf("GenerateSecretKey 失败: %v", err)
	}
	if !strings.HasPrefix(full, "sk-") {
		t.Errorf("full = %q，应以 sk- 开头", full)
	}
	if len(full) != len("sk-")+64 {
		t.Errorf("full 长度 = %d，期望 %d", len(full), len("sk-")+64)
	}
	if hash != HashSecretKey(full) {
		t.Error("返回的 hash 应与 HashSecretKey(full) 一致")
	}
	if len(hash) != 64 {
		t.Errorf("hash 长度 = %d，期望 64", len(hash))
	}

	full2, hash2, err := GenerateSecretKey("sk-")
	if err != nil {
		t.Fatalf("第二次 GenerateSecretKey 失败: %v", err)
	}
	if full == full2 {
		t.Error("两次生成的 full 应不同")
	}
	if hash == hash2 {
		t.Error("两次生成的 hash 应不同")
	}
}

// TestGeneratePassword 验证随机密码：
// 长度正确、两次不同、字符都在字母表内。
func TestGeneratePassword(t *testing.T) {
	p, err := GeneratePassword(16)
	if err != nil {
		t.Fatalf("GeneratePassword 失败: %v", err)
	}
	if len(p) != 16 {
		t.Errorf("密码长度 = %d，期望 16", len(p))
	}
	for _, r := range p {
		if !strings.ContainsRune(passwordAlphabet, r) {
			t.Errorf("密码含字母表外字符 %q", r)
		}
	}

	p2, err := GeneratePassword(16)
	if err != nil {
		t.Fatalf("第二次 GeneratePassword 失败: %v", err)
	}
	if p == p2 {
		t.Error("两次生成的密码应不同")
	}

	// 长度不足 4 应返回错误。
	if _, err := GeneratePassword(3); err == nil {
		t.Error("长度 3 应返回错误")
	}
}
