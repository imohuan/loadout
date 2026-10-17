package requestlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loadout/core/config"
)

// ReplayScriptOptions 复现脚本生成参数。
//
// 默认值面向「完整请求日志」详情页：路径固定 chat/completions、目标地址取当前
// 服务监听端口。request-capture 等其它面板可以显式传入原始路径，复用同一个生成器。
type ReplayScriptOptions struct {
	// RequestID 日志 UUID（写进脚本注释，方便反查来源）。
	RequestID string
	// Path 转发路径（相对 /v1/ 之后的部分），空值回退 chat/completions。
	Path string
	// BaseURL 目标地址，空值按 config.ServerAddr 推导 http://127.0.0.1:<port>。
	BaseURL string
	// CreatedAt 生成时间，零值取当前时间（测试固定时间用）。
	CreatedAt time.Time
}

// replayDefaultPath 缺省转发的相对路径。
const replayDefaultPath = "chat/completions"

// BuildReplayScript 生成一个可直接运行的 Node.js 复现脚本。
//
// bodyJSON 原样嵌入脚本（不解析、不重排），保证重放报文与日志里记录的一致。
// 调用方负责落盘；本函数只做纯字符串拼装，便于测试与其它插件复用。
func BuildReplayScript(bodyJSON []byte, requestID string) ([]byte, error) {
	return BuildReplayScriptWithOptions(bodyJSON, ReplayScriptOptions{RequestID: requestID})
}

// BuildReplayScriptWithOptions 同上，可指定路径与目标地址。
func BuildReplayScriptWithOptions(bodyJSON []byte, opts ReplayScriptOptions) ([]byte, error) {
	if len(bodyJSON) == 0 {
		return nil, fmt.Errorf("request-log: 请求体为空，无法生成复现脚本")
	}
	// 先确认是合法 JSON：脚本里直接按原字符串发送，坏数据也能跑，但用户会拿到
	// 一个必然 400 的脚本，不如生成时就说清楚。
	if !json.Valid(bodyJSON) {
		return nil, fmt.Errorf("request-log: 请求体不是合法 JSON，无法生成复现脚本")
	}

	reqPath := strings.TrimSpace(opts.Path)
	reqPath = strings.TrimPrefix(reqPath, "/")
	reqPath = strings.TrimPrefix(reqPath, "v1/")
	reqPath = strings.TrimPrefix(reqPath, "v2/")
	if reqPath == "" {
		reqPath = replayDefaultPath
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = ReplayBaseURL()
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}

	// 请求体以 JSON 字符串字面量嵌入：消息里出现反引号、脚本结束标签、模板占位符
	// 都不会破坏脚本语法，运行时按原字节发送。
	bodyLiteral, err := json.Marshal(string(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("request-log: 序列化请求体失败: %w", err)
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env node\n")
	b.WriteString("// 自动生成：从 Loadout 完整请求日志复现一次请求\n")
	b.WriteString("// 日志来源：" + strings.TrimSpace(opts.RequestID) + "\n")
	b.WriteString("// 生成时间：" + created.Format(time.RFC3339) + "\n")
	b.WriteString("// 运行方式：LOADOUT_API_KEY=sk-xxx node <本文件>\n")
	b.WriteString("// 可选环境变量：LOADOUT_BASE_URL（默认 " + baseURL + "）、LOADOUT_PATH（默认 " + reqPath + "）\n")
	b.WriteString("\n")
	b.WriteString("const REQUEST_BODY = " + string(bodyLiteral) + ";\n")
	b.WriteString("\n")
	b.WriteString("const BASE_URL = process.env.LOADOUT_BASE_URL || " + JSONQuote(baseURL) + ";\n")
	b.WriteString("const REQUEST_PATH = process.env.LOADOUT_PATH || " + JSONQuote(reqPath) + ";\n")
	b.WriteString("const API_KEY = process.env.LOADOUT_API_KEY || '';\n")
	b.WriteString("\n")
	b.WriteString("if (!API_KEY) {\n")
	b.WriteString("  console.error('缺少环境变量 LOADOUT_API_KEY。用法：LOADOUT_API_KEY=sk-xxx node ' + process.argv[1]);\n")
	b.WriteString("  process.exit(1);\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("async function main() {\n")
	b.WriteString("  const url = BASE_URL.replace(/\\/+$/, '') + '/v1/' + REQUEST_PATH.replace(/^\\/+/, '');\n")
	b.WriteString("  console.log('POST ' + url);\n")
	b.WriteString("  console.log('请求体 ' + REQUEST_BODY.length + ' 字节');\n")
	b.WriteString("  const res = await fetch(url, {\n")
	b.WriteString("    method: 'POST',\n")
	b.WriteString("    headers: {\n")
	b.WriteString("      'Content-Type': 'application/json',\n")
	b.WriteString("      Authorization: 'Bearer ' + API_KEY,\n")
	b.WriteString("    },\n")
	b.WriteString("    body: REQUEST_BODY,\n")
	b.WriteString("  });\n")
	b.WriteString("  console.log('HTTP ' + res.status + ' ' + res.statusText);\n")
	b.WriteString("  const text = await res.text();\n")
	b.WriteString("  const limit = 2000;\n")
	b.WriteString("  console.log(text.length > limit ? text.slice(0, limit) : text);\n")
	b.WriteString("  if (text.length > limit) console.log('...（共 ' + text.length + ' 字符，已截断）');\n")
	b.WriteString("  if (!res.ok) process.exitCode = 1;\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("main().catch((err) => {\n")
	b.WriteString("  console.error(err);\n")
	b.WriteString("  process.exit(1);\n")
	b.WriteString("});\n")
	return []byte(b.String()), nil
}

// ReplayBaseURL 由服务监听地址推导本机可访问地址。
//
// config.ServerAddr 形如 ":3000" 或 "0.0.0.0:3000"（server 模式），桌面模式监听
// 127.0.0.1。复现脚本在本机运行，统一指向 127.0.0.1 最稳。
func ReplayBaseURL() string {
	addr := strings.TrimSpace(config.ServerAddr)
	if addr == "" {
		return "http://127.0.0.1:3000"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return "http://127.0.0.1" + addr[i:]
	}
	return "http://127.0.0.1:" + addr
}

// JSONQuote 用 encoding/json 的规则转义一段普通字符串，避免手写引号转义。
func JSONQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// WriteReplayScript 把脚本写到 <数据目录>/replay-scripts/<id>.mjs 并返回绝对路径。
//
// 目录与 request-logs/ 同级（由块存储目录推导），这样备份/迁移数据目录时脚本
// 跟着走；重复点击覆盖同名文件，不堆积历史版本。
func (s *Service) WriteReplayScript(id string, bodyJSON []byte, opts ReplayScriptOptions) (string, int, error) {
	if s.blobs == nil {
		return "", 0, fmt.Errorf("request-log: 块存储未装配")
	}
	return WriteReplayScriptFile(filepath.Dir(s.blobs.dir), id, bodyJSON, opts)
}

// WriteReplayScriptFile 把复现脚本写到 baseDir/replay-scripts/<id>.mjs，返回绝对路径。
//
// 给其它面板复用（request-capture 的详情页也要同样的「填入测试」）。baseDir 传
// 数据目录（请求日志目录的上一级），脚本统一落在 replay-scripts/ 下，避免每个
// 插件各建一个目录。
func WriteReplayScriptFile(baseDir, id string, bodyJSON []byte, opts ReplayScriptOptions) (string, int, error) {
	dir := filepath.Join(baseDir, "replay-scripts")
	return writeReplayScriptTo(dir, id, bodyJSON, opts)
}

// writeReplayScriptTo 内部实现：写到指定目录。
func writeReplayScriptTo(dir, id string, bodyJSON []byte, opts ReplayScriptOptions) (string, int, error) {
	if opts.RequestID == "" {
		opts.RequestID = id
	}
	script, err := BuildReplayScriptWithOptions(bodyJSON, opts)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("request-log: 创建复现脚本目录失败: %w", err)
	}
	// 文件名只允许日志 UUID：id 来自 URL 路径，防御路径穿越。
	safeID := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, id)
	if safeID == "" {
		return "", 0, fmt.Errorf("request-log: 非法日志 ID")
	}
	path := filepath.Join(dir, safeID+".mjs")
	if err := os.WriteFile(path, script, 0o644); err != nil {
		return "", 0, fmt.Errorf("request-log: 写入复现脚本失败: %w", err)
	}
	return path, len(script), nil
}

// replayBodyFromDetail 从 Detail 结果里取原始请求体与路径（复现必须不脱敏）。
func replayBodyFromDetail(d requestLogDetail) ([]byte, string, error) {
	var snap requestSnapshot
	if len(d.RequestJSON) > 0 {
		if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
			return nil, "", fmt.Errorf("request-log: 解析请求快照失败: %w", err)
		}
	}
	if strings.TrimSpace(snap.Body) == "" {
		return nil, "", fmt.Errorf("request-log: 该日志没有可复现的请求体（可能已被清理或未记录）")
	}
	return []byte(snap.Body), snap.Path, nil
}
