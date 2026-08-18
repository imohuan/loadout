package logger

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// readLog 读取 dir/name 的日志文件内容。
func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("读取日志文件失败: %v", err)
	}
	return string(b)
}

// newFileLogger 创建一个写文件的 logger，并在测试结束时关闭底层文件写器，
// 避免 lumberjack 持有的句柄阻塞 TempDir 清理。
func newFileLogger(t *testing.T, opts Options) *slog.Logger {
	t.Helper()
	l, closer := NewWithCloser(opts)
	t.Cleanup(func() { _ = closer() })
	return l
}

// TestNewWritesFileAndFormats 验证日志写入 lumberjack 文件且符合固定格式正则。
func TestNewWritesFileAndFormats(t *testing.T) {
	dir := t.TempDir()

	log := newFileLogger(t, Options{LogDir: dir, Console: false})
	log.Info("视觉描述完成", "耗时", "3ms")

	// 文件确实被创建。
	if _, err := os.Stat(filepath.Join(dir, defaultFilename)); err != nil {
		t.Fatalf("日志文件未创建: %v", err)
	}

	content := readLog(t, dir, defaultFilename)
	re := regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[[A-Z]+\] \[\S+:\d+\] .*$`)
	if !re.MatchString(content) {
		t.Fatalf("输出不符合固定格式: %q", content)
	}
	if !strings.Contains(content, "[INFO]") {
		t.Errorf("输出缺少 [INFO] 等级: %q", content)
	}
	if !strings.Contains(content, "视觉描述完成") || !strings.Contains(content, "耗时=3ms") {
		t.Errorf("输出缺少消息或属性: %q", content)
	}
}

// TestLevelFromString 验证各取值与未知值回落到 Info。
func TestLevelFromString(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"DEBUG", slog.LevelDebug},
		{"Warning", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"trace", slog.LevelInfo},
		{"nonsense", slog.LevelInfo},
	}
	for _, c := range cases {
		if got := LevelFromString(c.in); got != c.want {
			t.Errorf("LevelFromString(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// TestRedaction 验证消息里的 sk- 令牌与敏感属性值都被脱敏。
func TestRedaction(t *testing.T) {
	dir := t.TempDir()

	log := newFileLogger(t, Options{LogDir: dir, Console: false})
	log.Info("拿到密钥 sk-abcdef1234 请勿泄露",
		"password", "P@ssw0rd-secret-value",
		"Authorization", "Bearer sk-abcdef1234",
		"note", "前缀 sk-abcdef1234 后缀")

	content := readLog(t, dir, defaultFilename)

	// 明文一律不出现。
	for _, banned := range []string{"sk-abcdef1234", "P@ssw0rd-secret-value", "Bearer sk-abcdef1234"} {
		if strings.Contains(content, banned) {
			t.Errorf("输出泄露明文 %q: %q", banned, content)
		}
	}
	// 脱敏占位符出现（消息 1 处 + password 1 处 + Authorization 1 处 + note 1 处）。
	if got := strings.Count(content, "sk-***"); got < 4 {
		t.Errorf("脱敏占位符数量不足，期望 >= 4，实际 %d: %q", got, content)
	}
	// 普通字符串值只替换令牌、保留首尾文本。
	if !strings.Contains(content, "note=前缀 sk-*** 后缀") {
		t.Errorf("普通值脱敏未保留上下文: %q", content)
	}
	// 敏感属性值整体替换。
	if !strings.Contains(content, "password=sk-***") || !strings.Contains(content, "Authorization=sk-***") {
		t.Errorf("敏感属性未整体替换: %q", content)
	}
}

// TestSourceRootTrim 验证 SourceRoot 裁剪成仓库相对路径且 \ 归一化为 /。
func TestSourceRootTrim(t *testing.T) {
	// 用本测试文件路径推导仓库根，保证与编译期嵌入路径同源。
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile))) // core/logger/logger_test.go → 仓库根。

	dir := t.TempDir()
	log := newFileLogger(t, Options{LogDir: dir, Console: false, SourceRoot: repoRoot})
	log.Info("位置信息")

	content := readLog(t, dir, defaultFilename)

	re := regexp.MustCompile(`\[core/logger/logger_test\.go:\d+\]`)
	if !re.MatchString(content) {
		t.Fatalf("源码路径未裁剪为仓库相对路径: %q", content)
	}
	if strings.Contains(content, "\\") {
		t.Errorf("源码路径未归一化为 /: %q", content)
	}
}

// TestConsoleFallback 验证 Console=false 且 LogDir 为空时仍返回有效控制台 logger。
func TestConsoleFallback(t *testing.T) {
	log := New(Options{Console: false})
	if log == nil {
		t.Fatal("New 返回 nil")
	}
	// 记录一条日志不应 panic，输出落到控制台。
	log.Info("兜底输出")

	// 全零 Options 也应得到有效 logger。
	if got := New(Options{}); got == nil {
		t.Fatal("New(Options{}) 返回 nil")
	}
}
