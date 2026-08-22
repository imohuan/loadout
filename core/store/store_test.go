package store

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNewCreatesAndReusesSecret 验证 New 首次生成 .secret（0600 权限），再次 New 复用同一密钥。
func TestNewCreatesAndReusesSecret(t *testing.T) {
	dir := t.TempDir()

	s1, err := New(dir)
	if err != nil {
		t.Fatalf("首次 New 失败: %v", err)
	}
	if s1.Dir() != dir {
		t.Fatalf("Dir() = %q，期望 %q", s1.Dir(), dir)
	}

	// .secret 存在；Unix 语义下权限应为 0600（Windows 不遵循 Unix 权限位，跳过该断言）。
	secretPath := filepath.Join(dir, ".secret")
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("未生成 .secret 文件: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf(".secret 权限 = %o，期望 0600", perm)
		}
	}

	// 再次 New 应复用同一密钥。
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("再次 New 失败: %v", err)
	}
	if string(s1.SecretKey()) != string(s2.SecretKey()) {
		t.Fatal("再次 New 后密钥不一致")
	}
}

// TestNewRejectsShortSecret 验证密钥长度不足时 New 报错。
func TestNewRejectsShortSecret(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, ".secret")
	if err := os.WriteFile(secretPath, []byte("too-short"), 0o600); err != nil {
		t.Fatalf("写入短密钥失败: %v", err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("密钥长度不足时 New 应报错")
	}
}

// TestWriteReadRoundTrip 验证 Write + Read 往返一致。
func TestWriteReadRoundTrip(t *testing.T) {
	s := mustNew(t)

	in := map[string]any{
		"name":    "newapi",
		"base":    "http://127.0.0.1:3001/v1",
		"enabled": true,
		"models":  []string{"gpt-4o", "deepseek-chat"},
	}
	if err := s.Write("channels.json", in); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	var out map[string]any
	if err := s.Read("channels.json", &out); err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if out["name"] != "newapi" || out["enabled"] != true {
		t.Fatalf("Read 结果不匹配: %#v", out)
	}
	if len(out["models"].([]any)) != 2 {
		t.Fatalf("models 未正确往返: %#v", out["models"])
	}
}

// TestReadNotExist 验证读取不存在文件返回 ErrNotExist。
func TestReadNotExist(t *testing.T) {
	s := mustNew(t)

	var v any
	err := s.Read("missing.json", &v)
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("期望 ErrNotExist，得到 %v", err)
	}
}

// TestEncryptDecrypt 验证加密往返、密文不含明文、篡改后解密报错。
func TestEncryptDecrypt(t *testing.T) {
	s := mustNew(t)

	plain := "sk-1234567890abcdef 敏感渠道 key"
	ciphertext, err := s.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	if strings.Contains(ciphertext, plain) {
		t.Fatal("密文不应包含明文")
	}
	if ciphertext == plain {
		t.Fatal("密文不应等于明文")
	}

	got, err := s.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt 失败: %v", err)
	}
	if got != plain {
		t.Fatalf("解密结果 = %q，期望 %q", got, plain)
	}

	// 篡改密文后解密必须报错。
	raw, err := decodeBase64(ciphertext)
	if err != nil {
		t.Fatalf("解码密文失败: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if _, err := s.Decrypt(encodeBase64(raw)); err == nil {
		t.Fatal("篡改密文后 Decrypt 应报错")
	}

	// 非法 base64 输入应报错。
	if _, err := s.Decrypt("not-base64!!!"); err == nil {
		t.Fatal("非法 base64 输入 Decrypt 应报错")
	}
}

// TestWriteAtomicNoTmp 验证 Write 后目录里不存在残留 .tmp 文件。
func TestWriteAtomicNoTmp(t *testing.T) {
	s := mustNew(t)

	for i := 0; i < 5; i++ {
		if err := s.Write("data.json", map[string]int{"i": i}); err != nil {
			t.Fatalf("第 %d 次 Write 失败: %v", i, err)
		}
	}

	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("目录残留临时文件: %s", e.Name())
		}
	}
}

// TestExistsRemove 验证 Exists / Remove 行为。
func TestExistsRemove(t *testing.T) {
	s := mustNew(t)

	if s.Exists("x.json") {
		t.Fatal("文件不存在时 Exists 应返回 false")
	}
	if err := s.Write("x.json", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if !s.Exists("x.json") {
		t.Fatal("文件存在时 Exists 应返回 true")
	}

	if err := s.Remove("x.json"); err != nil {
		t.Fatalf("Remove 失败: %v", err)
	}
	if s.Exists("x.json") {
		t.Fatal("Remove 后 Exists 应返回 false")
	}
	// 删除不存在的文件应返回 nil。
	if err := s.Remove("x.json"); err != nil {
		t.Fatalf("删除不存在文件应返回 nil，得到 %v", err)
	}
}

// mustNew 创建临时目录上的 Store，失败直接终止测试。
func mustNew(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return s
}

// decodeBase64 / encodeBase64 便于篡改密文字节。
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
