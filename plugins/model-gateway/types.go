package modelgateway

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// EventBeforeUpstream 在转发上游前触发的 waterfall 事件。
// 能力插件（如 vision）订阅它改写 Messages / VisionText。
const EventBeforeUpstream = "chat:before-upstream"

// EventUpstreamFailed 在上游转发失败时触发的 waterfall 事件。
// aggregate 插件订阅它，分析失败原因并切换到下一个模型。
const EventUpstreamFailed = "chat:upstream-failed"

// EventUpstreamSucceeded 在上游成功转发后触发。
// aggregate 插件订阅它，记录目标模型已经恢复可用。
const EventUpstreamSucceeded = "chat:upstream-succeeded"

// ChatRequest 归一化后的 chat 请求。
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// ChatMessage 一条消息。
type ChatMessage struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// MessageContent 消息内容：纯文本（Text）或分段（Parts，可含图片）。
type MessageContent struct {
	Text  string        // 纯文本
	Parts []MessagePart // 分段
}

// MessagePart 内容分段。
type MessagePart struct {
	Type     string `json:"type"` // "text" | "image_url"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"` // 图片 URL 或 base64 data URI
}

// Pipeline 贯穿请求管线的载荷。
type Pipeline struct {
	RequestID string
	Request   *ChatRequest
	Messages  []ChatMessage // 当前 messages（能力插件可改写）
	// StreamWriter 视觉流输出回调：流式请求时由 model-gateway 注入；vision 用它把视觉
	// 识别的 reasoning delta 实时输出到客户端。nil = 非流式（视觉描述只写进 Messages）。
	StreamWriter   func(delta string) error
	ResponseWriter http.ResponseWriter // 通用机制：任何插件都能在 waterfall 里直接写响应
	HTTPRequest    *http.Request       // 插件可能需要读 headers 或其他 HTTP 上下文
	Metadata       map[string]any      // 插件间传递元数据
}

// ResolvedChannel 解析后的转发渠道（APIKey 已解密）。
type ResolvedChannel struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string
}

// GatewayError 携带 OpenAI 标准错误信息（type 与 HTTP 状态码）的网关错误。
// 能力插件在 waterfall 处理器中返回它，网关据此生成 {"error":{...}} 响应。
type GatewayError struct {
	Status int    // HTTP 状态码（0 表示默认 400）
	Type   string // OpenAI 错误类型，如 vision_capability_error
	Msg    string // 错误消息
}

// Error 实现 error 接口，返回错误消息。
func (e *GatewayError) Error() string { return e.Msg }

// FailurePayload 上游转发失败的载荷
type FailurePayload struct {
	Pipe       *Pipeline // 原始请求管线
	Model      string    // 失败的模型名
	ChannelID  string    // 失败的渠道 ID
	Error      error     // 错误对象
	StatusCode int       // HTTP 状态码
	ErrorBody  string    // 上游错误响应体（用于 AI 分析）
}

// SuccessPayload 上游转发成功的载荷。
type SuccessPayload struct {
	Pipe      *Pipeline // 原始请求管线
	Model     string    // 成功的模型名
	ChannelID string    // 成功的渠道 ID
}

// RetryPayload 请求重试的载荷
type RetryPayload struct {
	Pipe *Pipeline // 改写后的请求管线
}

// UnmarshalJSON 让 MessageContent 兼容两种 content 形态：
// 字符串（纯文本）或数组（分段，image_url 部分填 Parts）。
func (c *MessageContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		return nil
	}

	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("modelgateway: content 必须为字符串或数组: %w", err)
	}
	for _, item := range raw {
		var probe struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			return fmt.Errorf("modelgateway: 解析 content 分段失败: %w", err)
		}
		switch probe.Type {
		case "image_url":
			c.Parts = append(c.Parts, MessagePart{Type: "image_url", ImageURL: extractImageURL(probe.ImageURL)})
		default:
			c.Parts = append(c.Parts, MessagePart{Type: "text", Text: probe.Text})
		}
	}
	return nil
}

// MarshalJSON 把 MessageContent 序列化回 OpenAI 标准格式：
// 纯文本时输出字符串，分段时输出数组。
func (c MessageContent) MarshalJSON() ([]byte, error) {
	// 如果有 Parts，序列化为数组
	if len(c.Parts) > 0 {
		parts := make([]map[string]any, 0, len(c.Parts))
		for _, p := range c.Parts {
			part := map[string]any{"type": p.Type}
			if p.Type == "text" {
				part["text"] = p.Text
			} else if p.Type == "image_url" {
				part["image_url"] = map[string]string{"url": p.ImageURL}
			}
			parts = append(parts, part)
		}
		return json.Marshal(parts)
	}
	// 否则序列化为纯文本字符串
	return json.Marshal(c.Text)
}

// extractImageURL 从 image_url 字段提取 URL：兼容字符串或 {"url": "..."} 对象。
func extractImageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.URL
	}
	return ""
}

// ---- 透明代理（proxy）管线 ----
//
// HandleProxy 对 /v1/{path...} 任意路径做透明转发：请求体原字节、参数原样
// 透传，不做字段白名单清洗。插件通过三个 waterfall 事件介入：
//   - ProxyBeforeUpstream：转发前拦截/修改输入（Body/Path/Query/Header）
//   - ProxyAfterUpstream：非流式响应返回后拦截/修改输出（状态码/Header/Body）
//   - ProxyStreamChunk：流式响应逐块拦截/修改/删除（返回 nil 删除该块）

// ProxyBeforeUpstream 转发上游前触发的 waterfall 事件（输入拦截/修改）。
const ProxyBeforeUpstream = "proxy:before-upstream"

// ProxyAfterUpstream 上游非流式响应返回后触发的 waterfall 事件（输出拦截/修改）。
const ProxyAfterUpstream = "proxy:after-upstream"

// ProxyStreamChunk 流式响应逐块转发的 waterfall 事件（逐 chunk 拦截/修改/删除）。
const ProxyStreamChunk = "proxy:stream-chunk"

// ProxyUpstreamFailed 上游转发失败时触发的 waterfall 事件。
// aggregate 插件订阅它，分析失败原因并切换下一个目标模型。
const ProxyUpstreamFailed = "proxy:upstream-failed"

// ProxyUpstreamSucceeded 上游成功转发后触发的 waterfall 事件。
// aggregate 插件订阅它，记录目标模型已经恢复可用。
const ProxyUpstreamSucceeded = "proxy:upstream-succeeded"

// ProxyRequest 透明代理请求侧：原始字节，不做字段清洗。
type ProxyRequest struct {
	Method string      // 原样透传的方法
	Path   string      // 剩余路径，如 "responses"、"chat/completions"、"messages"
	Query  string      // 原始 query（RawQuery），原样透传
	Header http.Header // 客户端 headers（插件可改）
	Body   []byte      // 原始请求体（插件可改）
	Model  string      // 轻量提取的 model（仅用于路由/白名单；空 = 不做模型匹配）
	Stream bool        // 轻量提取的 stream 标记（用于流式转发判定）
}

// ProxyResponse 透明代理响应侧：输出拦截的修改点。
type ProxyResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// ProxyPipeline 透明代理请求管线载荷。
type ProxyPipeline struct {
	RequestID string
	Request   *ProxyRequest
	// Response 由输出 hook 填充/修改；非流式时 HandleProxy 读完整 body 写入。
	Response *ProxyResponse
	// ResponseWriter 供插件在 waterfall 里直接写响应（与旧 Pipeline 同机制）。
	ResponseWriter http.ResponseWriter
	// StreamWriter 视觉流输出回调：流式请求时由 model-gateway 注入；vision 用它把
	// 视觉识别的 reasoning delta 实时输出到客户端。nil = 非流式（描述只写进请求体）。
	StreamWriter func(delta string) error
	HTTPRequest  *http.Request
	Metadata     map[string]any // 插件间传递元数据
}

// AfterUpstreamPayload 非流式输出 hook 的载荷：管线 + 待改写的响应。
type AfterUpstreamPayload struct {
	Pipe     *ProxyPipeline
	Response *ProxyResponse
}

// StreamChunkPayload 流式 chunk hook 的载荷。
type StreamChunkPayload struct {
	Pipe *ProxyPipeline
	// Data 为单个 chunk 的原始字节（SSE 一行）；插件可改写，置 nil 表示删除。
	Data []byte
}

// ProxyFailurePayload 代理上游转发失败的载荷（聚合插件切换目标用）。
type ProxyFailurePayload struct {
	Pipe       *ProxyPipeline
	Model      string
	ChannelID  string
	Error      error
	StatusCode int
	ErrorBody  string
}

// ProxySuccessPayload 代理上游转发成功的载荷。
type ProxySuccessPayload struct {
	Pipe      *ProxyPipeline
	Model     string
	ChannelID string
}

// ProxyRetry 代理聚合切换后的管线（aggregate 改写 model 与 body 后返回）。
type ProxyRetry struct {
	Pipe *ProxyPipeline
}
