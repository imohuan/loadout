// Package logger 实现 Loadout 的日志：Go 标准库 slog + lumberjack 轮转，
// 固定文本格式输出与 logger 层统一脱敏。
//
// 设计规范（见 DESIGN.md 第 9 节）：
//   - 固定格式：时间 [等级] [源码相对路径:行号] 消息 [k=v ...]，例如
//     2026-08-15 15:00:01 [INFO] [plugins/vision/plugin.go:42] 视觉描述完成，缓存命中，耗时 3ms；
//   - 文件写入使用 lumberjack 轮转（大小 / 备份数 / 保留天数），目录不存在时自动创建；
//   - 脱敏：敏感属性名（key/token/secret/password/authorization/api_key）的字符串值整体
//     替换为 sk-***，所有字符串里出现的 sk-[A-Za-z0-9_-]{4,} 令牌替换为 sk-***。
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options 日志器配置。
type Options struct {
	Level      string // 日志级别：debug/info/warn/error，空 = "info"
	LogDir     string // 日志目录；空 = 不写文件，仅控制台
	Filename   string // 日志文件名；空 = "loadout.log"
	MaxSizeMB  int    // 单文件大小上限（MB）；0 = 默认 50
	MaxBackups int    // 保留历史文件数；0 = 默认 7
	MaxAgeDays int    // 历史文件保留天数；0 = 默认 30
	SourceRoot string // 源码绝对前缀，用于把源码路径裁成「仓库相对路径」；空 = 不裁剪
	Console    bool   // 是否同时输出到控制台（默认 true）
}

// 默认值：与 DESIGN.md 4.2 的日志常量保持一致。
const (
	defaultFilename   = "loadout.log" // 默认日志文件名。
	defaultMaxSizeMB  = 50            // 默认单文件大小上限（MB）。
	defaultMaxBackups = 7             // 默认保留历史文件数。
	defaultMaxAgeDays = 30            // 默认历史文件保留天数。
)

// 时间输出格式：固定为「2006-01-02 15:04:05」本地时区。
const timeLayout = "2006-01-02 15:04:05"

// secretKeyRe 匹配敏感属性名（不区分大小写）。
// api[_]?key 同时覆盖 "api_key" 与 "apiKey" 两种写法。
var secretKeyRe = regexp.MustCompile(`(?i)\b(key|token|secret|password|authorization|api[_]?key)\b`)

// secretValueRe 匹配需要脱敏的 sk- 令牌（长度 >= 4 的 [A-Za-z0-9_-]）。
var secretValueRe = regexp.MustCompile(`sk-[A-Za-z0-9_-]{4,}`)

// New 构建日志器：文件（lumberjack 轮转）+ 控制台。
//
//   - LogDir 非空时把日志写入 LogDir/Filename，目录不存在自动创建；
//   - Console=true（且 LogDir 非空）时用 io.MultiWriter 同时写文件与控制台；
//   - LogDir 为空时无论 Console 为何值都兜底输出到控制台，保证始终有输出。
//
// 固定输出格式：时间 [等级] [源码相对路径:行号] 消息 [k=v ...]
func New(opts Options) *slog.Logger {
	l, _ := NewWithCloser(opts)
	return l
}

// NewWithCloser 与 New 相同，额外返回关闭底层文件写器的函数。
// 关闭函数用于释放 lumberjack 持有的文件句柄（测试与进程退出时调用）。
func NewWithCloser(opts Options) (*slog.Logger, func() error) {
	level := LevelFromString(opts.Level)

	// 文件写器：lumberjack 负责大小 / 备份数 / 保留天数的轮转。
	var fileWriter *lumberjack.Logger
	if opts.LogDir != "" {
		if err := os.MkdirAll(opts.LogDir, 0o755); err == nil {
			filename := opts.Filename
			if filename == "" {
				filename = defaultFilename
			}
			maxSize := opts.MaxSizeMB
			if maxSize <= 0 {
				maxSize = defaultMaxSizeMB
			}
			maxBackups := opts.MaxBackups
			if maxBackups <= 0 {
				maxBackups = defaultMaxBackups
			}
			maxAge := opts.MaxAgeDays
			if maxAge <= 0 {
				maxAge = defaultMaxAgeDays
			}
			fileWriter = &lumberjack.Logger{
				Filename:   filepath.Join(opts.LogDir, filename),
				MaxSize:    maxSize,
				MaxBackups: maxBackups,
				MaxAge:     maxAge,
				LocalTime:  true,
			}
		}
		// 目录创建失败时丢弃文件写器，落入下方控制台兜底。
	}

	// 控制台：LogDir 为空时强制兜底（否则没有任何输出）。
	useConsole := opts.Console || opts.LogDir == ""

	var w io.Writer
	switch {
	case fileWriter != nil && useConsole:
		w = io.MultiWriter(fileWriter, os.Stdout)
	case fileWriter != nil:
		w = fileWriter
	default:
		w = os.Stdout
	}

	closer := func() error {
		if fileWriter != nil {
			return fileWriter.Close()
		}
		return nil
	}

	return slog.New(&textHandler{
		w:          w,
		level:      level,
		sourceRoot: opts.SourceRoot,
	}), closer
}

// LevelFromString 把字符串转成 slog.Level，未知值返回 slog.LevelInfo。
func LevelFromString(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// textHandler 自定义 slog.Handler，输出 DESIGN.md 第 9 节规定的固定文本格式。
//
// 标准库 slog.TextHandler 会强制输出 key=value 形式（如 time=...、level=...），
// 无法精确产出「时间 [等级] [源码] 消息」的格式，因此这里自行实现 Handler，
// 复用了 slog.Record 的语义与同样的脱敏 / 路径裁剪逻辑。
type textHandler struct {
	w          io.Writer      // w 输出目标（文件 + 控制台的多路复用器）。
	level      slog.Level     // level 最低输出级别。
	sourceRoot string         // sourceRoot 源码绝对前缀，用于裁剪相对路径。
	preAttrs   []prefixedAttr // preAttrs 通过 WithAttrs 累积的属性（含 group 前缀快照）。
	groups     []string       // groups 通过 WithGroup 累积的组名前缀。
}

// prefixedAttr 记录一个属性及其在 WithAttrs 当时的 group 前缀。
type prefixedAttr struct {
	prefix string    // prefix 组名前缀（如 "g1.g2."），空表示无前缀。
	attr   slog.Attr // attr 属性。
}

// Enabled 判断是否输出该级别日志。
func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

// Handle 把一条记录按固定格式写入 h.w。
func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.Grow(160)

	// 时间：本地时区，固定 2006-01-02 15:04:05。
	if !r.Time.IsZero() {
		b.WriteString(r.Time.Local().Format(timeLayout))
	}

	// 等级：大写 [INFO] / [DEBUG] / [WARN] / [ERROR]。
	b.WriteString(" [")
	b.WriteString(levelLabel(r.Level))
	b.WriteByte(']')

	// 源码：裁掉 SourceRoot 前缀、\ 归一化为 /，得到 文件:行号。
	if s := sourceString(r.PC, h.sourceRoot); s != "" {
		b.WriteString(" [")
		b.WriteString(s)
		b.WriteByte(']')
	}

	// 消息：脱敏 sk- 令牌。
	b.WriteByte(' ')
	b.WriteString(redact(r.Message))

	// 属性：先输出 WithAttrs 累积的，再输出本次调用传入的。
	prefix := groupPrefix(h.groups)
	for _, pa := range h.preAttrs {
		appendAttr(&b, pa.prefix, pa.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, prefix, a)
		return true
	})

	b.WriteByte('\n')
	_, err := io.WriteString(h.w, b.String())
	return err
}

// WithAttrs 返回一个带额外属性的 handler 副本。
func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := h.clone()
	prefix := groupPrefix(h.groups)
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if a.Equal(slog.Attr{}) {
			continue
		}
		h2.preAttrs = append(h2.preAttrs, prefixedAttr{prefix: prefix, attr: a})
	}
	return h2
}

// WithGroup 返回一个带组名前缀的 handler 副本。
func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := h.clone()
	h2.groups = append(h2.groups, name)
	return h2
}

// clone 深拷贝 handler，避免共享 preAttrs / groups 底层切片。
func (h *textHandler) clone() *textHandler {
	h2 := *h
	h2.preAttrs = append([]prefixedAttr(nil), h.preAttrs...)
	h2.groups = append([]string(nil), h.groups...)
	return &h2
}

// levelLabel 把 slog.Level 转成大写标签。
func levelLabel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// sourceString 从调用点 PC 解析出「仓库相对路径:行号」；pc==0 时返回空串。
func sourceString(pc uintptr, sourceRoot string) string {
	if pc == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	if frame.File == "" {
		return ""
	}
	file := frame.File
	if sourceRoot != "" {
		if rel, err := filepath.Rel(sourceRoot, file); err == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			file = rel
		}
	}
	return filepath.ToSlash(file) + ":" + strconv.Itoa(frame.Line)
}

// groupPrefix 把组名拼成 "g1.g2." 形式的 key 前缀；无组时返回空串。
func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

// appendAttr 把单个属性（含脱敏）写入 b，前缀 prefix 为组名前缀。
func appendAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	// 组属性递归展开为 g.key.inner=value。
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			appendAttr(b, prefix, g)
		}
		return
	}
	if a.Key == "" {
		return
	}
	b.WriteByte(' ')
	b.WriteString(prefix)
	b.WriteString(a.Key)
	b.WriteByte('=')
	// 敏感属性名的字符串值整体替换为 sk-***。
	if a.Value.Kind() == slog.KindString && isSensitiveKey(a.Key) {
		b.WriteString("sk-***")
		return
	}
	appendValue(b, a.Value)
}

// appendValue 把属性值写入 b，字符串值统一做 sk- 令牌脱敏。
func appendValue(b *strings.Builder, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		b.WriteString(redact(v.String()))
	case slog.KindInt64:
		b.WriteString(strconv.FormatInt(v.Int64(), 10))
	case slog.KindUint64:
		b.WriteString(strconv.FormatUint(v.Uint64(), 10))
	case slog.KindFloat64:
		b.WriteString(strconv.FormatFloat(v.Float64(), 'g', -1, 64))
	case slog.KindBool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case slog.KindDuration:
		b.WriteString(v.Duration().String())
	case slog.KindTime:
		b.WriteString(v.Time().Format(timeLayout))
	case slog.KindAny:
		b.WriteString(fmt.Sprintf("%+v", v.Any()))
	default:
		b.WriteString(v.String())
	}
}

// isSensitiveKey 判断属性名是否命中敏感正则（不区分大小写）。
func isSensitiveKey(name string) bool {
	return secretKeyRe.MatchString(name)
}

// redact 把字符串里出现的 sk-[A-Za-z0-9_-]{4,} 令牌替换为 sk-***。
func redact(s string) string {
	return secretValueRe.ReplaceAllString(s, "sk-***")
}
