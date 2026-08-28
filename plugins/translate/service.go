package translate

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	mcphub "loadout/plugins/mcp-hub"
	gatewaykeys "loadout/plugins/gateway-keys"
)

// Service 翻译服务。
type Service struct {
	st   *store.Store
	repo *db.Repository
	db   *sql.DB
	lg   *slog.Logger
	gw   *modelgateway.Service
	hub  *mcphub.Service
	keys *gatewaykeys.Manager

	// 批量翻译后台任务管理：task_id -> 任务状态。
	batchMu   sync.Mutex
	batchSeq  int64
	batchJobs map[string]*batchTask
}

// NewService 创建翻译服务。
func NewService(st *store.Store, repo *db.Repository, database *sql.DB, lg *slog.Logger, gw *modelgateway.Service, hub *mcphub.Service, keys *gatewaykeys.Manager) *Service {
	return &Service{st: st, repo: repo, db: database, lg: lg, gw: gw, hub: hub, keys: keys, batchJobs: make(map[string]*batchTask)}
}

// ---- 翻译核心 ----

// defaultPrompt 默认翻译提示词。{lang} 会在调用时替换为目标语言。
const defaultPrompt = `你是一个专业翻译。请把下面的文本翻译成{lang}。要求：
- 保持原意，语气自然，符合目标语言习惯
- 保留代码、URL、占位符、变量名、标点等不变
- 只输出译文，不要加任何解释、注释或多余内容
- 如果原文已经是目标语言或无需翻译（纯代码/数字/URL），原样返回`

// chunk 一个翻译单元（段落切句后的小块）。
type chunk struct {
	Index int    // 在原文里的顺序
	Text  string // 小块文本
}

// splitChunks 按「空行分大段 → 段内按句切小块」拆分文本。
func splitChunks(text string) []chunk {
	var chunks []chunk
	// 先按空行分大段
	paragraphs := strings.Split(text, "\n\n")
	idx := 0
	for _, para := range paragraphs {
		if strings.TrimSpace(para) == "" {
			continue
		}
		// 段内按句子切（。！？换行等边界）
		segments := splitSentences(para)
		for _, seg := range segments {
			s := strings.TrimSpace(seg)
			if s == "" {
				continue
			}
			chunks = append(chunks, chunk{Index: idx, Text: s})
			idx++
		}
	}
	return chunks
}

// splitSentences 把一段文本按句子边界切分。中英文句末标点与换行都视为边界。
func splitSentences(para string) []string {
	// 用正则或手动扫描；这里用简单规则：按 .!?。！？ 后跟空白/结束，以及换行。
	segs := splitOnPunct(para)
	return segs
}

// splitOnPunct 按句末标点切分，保留标点在句尾。
func splitOnPunct(s string) []string {
	var out []string
	var sb strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		sb.WriteRune(c)
		// 句末标点后若紧跟空白或到末尾，则成句
		if isSentenceEnd(c) {
			// 看下一个字符：空白/换行/结束则切
			if i+1 >= len(runes) || isSpace(runes[i+1]) {
				if t := strings.TrimSpace(sb.String()); t != "" {
					out = append(out, t)
				}
				sb.Reset()
			}
		}
	}
	if t := strings.TrimSpace(sb.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？', '；', ';', '\n', '\r':
		return true
	}
	return false
}

func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// hashText 计算内容 hash（作为缓存 key）。
func hashText(text string) string {
	// 简单 FNV-1a；可用更强 hash。这里保证「改一个字符 hash 必变」。
	h := uint64(2166136261)
	for _, b := range []byte(text) {
		h ^= uint64(b)
		h *= 16777619
	}
	return fmt.Sprintf("%016x", h)
}

// skipTranslation 判断该文本是否需要跳过翻译（已是目标语言/纯代码/URL/数字/占位符）。
// 返回 true 表示无需翻译。
func skipTranslation(text, targetLang string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	// 纯数字 / URL / 占位符 / 代码符号串
	if isURL(t) || isNumeric(t) || isPlaceholder(t) {
		return true
	}
	// 目标语言检测：若已是中文等，简单启发（可选）
	return false
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isNumeric(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' && c != ',' && c != '-' && c != '%' && c != ' ' {
			return false
		}
	}
	return s != ""
}

// 占位符/代码片段的精确匹配模板。整体字符串必须「基本只由这些 token 组成」才算占位符，
// 否则视为自然语言短语（需要翻译）。此前用「是否含句末标点」判断，导致
// "Repository owner" 这类不含句末标点的英文描述/标题被错误跳过、不发翻译请求。
var (
	rePlaceholderBraces = regexp.MustCompile(`^\{\{.+?\}\}$|^\{.+?\}$`)
	rePlaceholderDollar = regexp.MustCompile(`^\$\{.+?\}$|^\$[A-Za-z_][A-Za-z0-9_]*$`)
	rePlaceholderAngle  = regexp.MustCompile(`^<[^<>\s]+>$`)
	rePlaceholderPrintf = regexp.MustCompile(`^%[#0\- +]?\d*\.?\d*[svdfbxXocqEeGgTt]$`)
	reCodeIdentifier    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-/:]*$`)
)

func isPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	// 整体必须是明确的模板/代码 token 才算占位符
	if rePlaceholderBraces.MatchString(t) ||
		rePlaceholderDollar.MatchString(t) ||
		rePlaceholderAngle.MatchString(t) ||
		rePlaceholderPrintf.MatchString(t) {
		return true
	}
	// 纯代码标识符/路径（无空格、像 slug/类名/命令），才视为占位符跳过。
	// 含空格的英文短语（如 "Repository owner"）不属于此类，需要翻译。
	if !strings.ContainsAny(t, " \u3000\t") && reCodeIdentifier.MatchString(t) {
		return true
	}
	return false
}

// Translate 翻译一组文本（可多块合并进一次大模型请求）。返回与输入一一对应的译文。
// 优先命中缓存，未命中的合并请求一次翻译，然后落库。
func (s *Service) Translate(ctx context.Context, req TranslateRequest) ([]string, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("translate: 未指定目标模型")
	}
	if req.TargetLang == "" {
		req.TargetLang = "zh-CN"
	}
	if req.Type == "" {
		req.Type = TypeTranslate
	}
	src := req.Texts
	if req.SourceText != "" {
		src = []string{req.SourceText}
	}

	results := make([]string, len(src))
	var missing []struct {
		idx  int
		text string
	}
	// 先查缓存 + 跳过无需翻译的
	for i, text := range src {
		if skipTranslation(text, req.TargetLang) {
			results[i] = text
			continue
		}
		h := hashText(text)
		if tr, ok, err := s.getCached(ctx, h, req.TargetLang, req.Type); err == nil && ok {
			results[i] = tr
			continue
		}
		missing = append(missing, struct {
			idx  int
			text string
		}{i, text})
	}

	if len(missing) > 0 {
		// 合并缺失文本为一次大模型请求
		var texts []string
		for _, m := range missing {
			texts = append(texts, m.text)
		}
		translations, err := s.callModelOnce(ctx, req, texts)
		if err != nil {
			return nil, fmt.Errorf("translate: 翻译失败: %w", err)
		}
		for j, m := range missing {
			if j < len(translations) {
				results[m.idx] = translations[j]
				// 落库
				_ = s.save(ctx, Translation{
					Hash:           hashText(m.text),
					SourceText:     m.text,
					TranslatedText: translations[j],
					SourceType:     req.SourceType,
					SourceID:       req.SourceID,
					Key:            req.Key,
					TargetLang:     req.TargetLang,
					Model:          req.Model,
					Type:           req.Type,
				})
			}
		}
	}
	return results, nil
}

// callModelOnce 调大模型翻译一组文本（合并为一次请求）。
func (s *Service) callModelOnce(ctx context.Context, req TranslateRequest, texts []string) ([]string, error) {
	prompt := req.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultPrompt
	}
	// 统一替换 {lang} 占位符为目标语言（默认与自定义 prompt 都支持）
	prompt = strings.ReplaceAll(prompt, "{lang}", req.TargetLang)
	// 把多条文本编号交给模型，让它逐条返回
	numbered := make([]string, len(texts))
	for i, t := range texts {
		numbered[i] = fmt.Sprintf("[%d] %s", i+1, t)
	}
	userMsg := prompt + "\n\n待翻译文本：\n" + strings.Join(numbered, "\n")
	userMsg += "\n\n请按同样的 [编号] 格式逐条返回译文，每条一行。"

	payload := map[string]any{
		"model": req.Model,
		"messages": []map[string]any{
			{"role": "user", "content": userMsg},
		},
		// 模型要求流式（部分上游不支持非流式 chat 请求），统一走 stream:true，
		// 从 SSE 流里逐块累积 content 得到完整译文。
		"stream": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// 走 Loadout 自己的 /v1/chat/completions 对外接口（带 SK key），
	// 与模型测试一样产生完整转发日志（request-log / route-log / 额度）。
	skKey, err := s.resolveSKKey()
	if err != nil {
		return nil, err
	}
	base := internalBaseURL()
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+skKey)

	resp, err := (&http.Client{Timeout: config.UpstreamTimeout}).Do(upReq)
	if err != nil {
		return nil, fmt.Errorf("调用本机网关失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return nil, fmt.Errorf("网关返回错误(%d): %s", resp.StatusCode, truncateStr(string(respBody), 300))
	}
	// 流式解析：逐行读 SSE，累积 choices[0].delta.content
	var content strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			content.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return parseNumberedContent(content.String(), len(texts)), nil
}

// resolveSKKey 获取一个可用的自建 SK key 明文（用于走自家 /v1 网关）。
func (s *Service) resolveSKKey() (string, error) {
	if s.keys == nil {
		return "", fmt.Errorf("translate: gateway-keys 未初始化")
	}
	keys, err := s.keys.ListAPIKeys()
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if !k.Enabled || k.Hash == "" {
			continue
		}
		plain, _, err := s.keys.ResolveAPIKey(k.Hash)
		if err == nil && plain != "" {
			return plain, nil
		}
	}
	return "", fmt.Errorf("translate: 没有可用的 SK key，请在设置中创建")
}

// internalBaseURL 本机内部调用地址（按 ServerAddr 端口，连 127.0.0.1）。
func internalBaseURL() string {
	addr := config.ServerAddr
	host := "127.0.0.1"
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if p := strings.TrimPrefix(addr[i:], ":"); p != "" {
			return "http://" + host + ":" + p
		}
	}
	return "http://" + host
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// parseNumberedTranslations 解析模型返回的 [编号] 格式译文（非流式响应 JSON 形态）。
// 保留给测试与兼容；生产流式路径走 parseNumberedContent。
func parseNumberedTranslations(body []byte, n int) ([]string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("模型未返回内容")
	}
	return parseNumberedContent(parsed.Choices[0].Message.Content, n), nil
}

// parseNumberedContent 解析纯文本形式的 [编号] 译文内容，返回与 n 对应的译文列表。
func parseNumberedContent(content string, n int) []string {
	result := make([]string, n)
	// 按行解析 [i] 前缀
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 匹配 [数字] 或 "数字." 前缀
		idx := parseIndexPrefix(line)
		if idx >= 0 && idx < n {
			result[idx] = trimPrefix(line)
		}
	}
	// 兜底：若有空项，顺序填充未编号行
	var filler []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if parseIndexPrefix(line) < 0 {
			filler = append(filler, line)
		}
	}
	fi := 0
	for i := range result {
		if result[i] == "" && fi < len(filler) {
			result[i] = filler[fi]
			fi++
		}
	}
	return result
}

// parseIndexPrefix 解析 "[1]" 或 "1." 前缀，返回索引(0基)，无前缀返回 -1。
func parseIndexPrefix(line string) int {
	// [1]
	if len(line) > 3 && line[0] == '[' {
		if end := strings.IndexByte(line, ']'); end > 1 {
			var idx int
			if _, err := fmt.Sscanf(line[1:end], "%d", &idx); err == nil {
				return idx - 1
			}
		}
	}
	// 1.
	if len(line) > 1 {
		dot := strings.IndexAny(line, ".:、")
		if dot > 0 {
			var idx int
			if _, err := fmt.Sscanf(line[:dot], "%d", &idx); err == nil {
				return idx - 1
			}
		}
	}
	return -1
}

func trimPrefix(line string) string {
	if len(line) > 3 && line[0] == '[' {
		if end := strings.IndexByte(line, ']'); end > 1 {
			return strings.TrimSpace(line[end+1:])
		}
	}
	if dot := strings.IndexAny(line, ".:、"); dot > 0 {
		var idx int
		if _, err := fmt.Sscanf(line[:dot], "%d", &idx); err == nil {
			return strings.TrimSpace(line[dot+1:])
		}
	}
	return line
}

// ---- 缓存存取 ----

func (s *Service) getCached(ctx context.Context, hash, targetLang string, typ TranslationType) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT translated_text FROM translations WHERE hash=? AND target_lang=? AND type=? ORDER BY updated_at DESC LIMIT 1`,
		hash, targetLang, typ)
	var text string
	err := row.Scan(&text)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

func (s *Service) save(ctx context.Context, t Translation) error {
	now := time.Now()
	// 若同 hash+lang+type 已存在则更新
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO translations(hash, source_text, translated_text, source_type, source_id, key, target_lang, model, type, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(hash) DO UPDATE SET
		   translated_text=excluded.translated_text,
		   source_type=excluded.source_type,
		   source_id=excluded.source_id,
		   key=excluded.key,
		   model=excluded.model,
		   target_lang=excluded.target_lang,
		   updated_at=excluded.updated_at`,
		t.Hash, t.SourceText, t.TranslatedText, t.SourceType, t.SourceID, t.Key, t.TargetLang, t.Model, t.Type, now, now)
	return err
}

// ---- 文本收集 ----

// collectSources 收集所有可翻译来源（skill + MCP 工具描述）。
func (s *Service) collectSources(ctx context.Context) ([]SourceItem, error) {
	if s.hub == nil {
		return nil, fmt.Errorf("translate: mcp-hub 未初始化")
	}
	index, err := s.hub.BuildIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("translate: 构建索引失败: %w", err)
	}
	var items []SourceItem
	for _, t := range index.Tools {
		st := SourceMCP
		sid := t.Name
		if t.IsSkill {
			st = SourceSkill
		} else if t.Source != "" {
			sid = t.Source + "/" + t.Name
		}
		// 参数翻译：从 InputSchema 提取（MCP 工具才有；skill 无 schema）
		var params []ParamItem
		var inputSchema map[string]any
		if !t.IsSkill && t.InputSchema != nil {
			inputSchema = t.InputSchema
			params = extractParams(t.InputSchema)
		}
		// 描述和参数都为空则跳过（无可翻译内容）
		if strings.TrimSpace(t.Description) == "" && len(params) == 0 {
			continue
		}
		items = append(items, SourceItem{
			SourceType:  st,
			SourceID:    sid,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: inputSchema,
			Params:      params,
		})
	}
	return items, nil
}

// extractParams 从 JSON Schema 提取可翻译的参数项（name/title/description/type/enum）。
// MCP 工具 schema 形如 { type:"object", properties:{ <name>:{ title, description, type, enum } }, required:[...] }。
func extractParams(schema map[string]any) []ParamItem {
	var out []ParamItem
	required := map[string]bool{}
	if reqArr, ok := schema["required"].([]any); ok {
		for _, r := range reqArr {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return out
	}
	// 保持稳定顺序
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		item := ParamItem{
			Name:     name,
			Required: required[name],
		}
		if v, ok := p["title"].(string); ok {
			item.Title = v
		}
		if v, ok := p["description"].(string); ok {
			item.Description = v
		}
		if v, ok := p["type"].(string); ok {
			item.Type = v
		}
		// 只收集有可翻译文本的参数（title/description 至少一个非空）
		if item.Title != "" || item.Description != "" {
			out = append(out, item)
		}
	}
	return out
}
