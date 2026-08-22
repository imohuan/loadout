// Package contracts contains the small interfaces shared by routing plugins.
package contracts

import (
	"context"
	"encoding/json"
	"time"

	"loadout/core/db"
)

// DurationMS serializes a time.Duration as an integer millisecond count so the
// JSON wire format matches the frontend's `duration_ms` field. Using the raw
// time.Duration would emit nanoseconds (e.g. 1s -> 1000000000), which the UI
// then misreads as milliseconds.
type DurationMS time.Duration

func (d DurationMS) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).Milliseconds())
}

func (d DurationMS) Milliseconds() int64  { return time.Duration(d).Milliseconds() }
func (d DurationMS) Duration() time.Duration { return time.Duration(d) }

type RoutingRepository interface {
	ListChannels(context.Context) ([]db.Channel, error)
	ReplaceChannels(context.Context, []db.Channel) error
	ListAggregates(context.Context) ([]db.Aggregate, error)
	ReplaceAggregates(context.Context, []db.Aggregate) error
}

type Availability struct {
	ManualEnabled      bool   `json:"manual_enabled"`
	HealthStatus       string `json:"health_status"`
	EffectiveAvailable bool   `json:"effective_available"`
	Reason             string `json:"reason"`
}

type RouteFailure struct {
	RequestID  string
	Model      string
	ChannelID  string
	StatusCode int
	ErrorBody  string
	Error      string
}

type ModelHealth interface {
	Check(context.Context, string, string) (Availability, error)
	RecordSuccess(context.Context, string, string) error
	RecordFailure(context.Context, RouteFailure) (string, error)
	SetChannelEnabled(context.Context, string, bool) error
	SetModelEnabled(context.Context, string, string, bool) error
	SetModelsEnabled(context.Context, string, []string, bool) error
	DeleteModel(context.Context, string, string) error
	DeleteModels(context.Context, string, []string) error
	RecoverChannel(context.Context, string) error
	RecoverModel(context.Context, string, string) error
	RecoverModels(context.Context, string, []string) error
	RecoverAllModels(context.Context) (int64, error)
	RecoverAllModelsByChannel(context.Context, string) (int64, error)
	RecoverAllChannels(context.Context) (int64, error)
	List(context.Context) ([]ChannelStatus, error)
	CheckNow(context.Context, bool) error
	// PurgeChannelStates 删除某渠道下不在 keep 清单内的模型状态记录
	// （编辑渠道全量替换模型清单后清理幽灵状态，保证模型状态与模型渠道一致）。
	PurgeChannelStates(context.Context, string, []string) error
}

type ChannelStatus struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ChannelName   string        `json:"channel_name,omitempty"`
	BaseURL       string        `json:"base_url"`
	ManualEnabled bool          `json:"manual_enabled"`
	SyncBilling   bool          `json:"sync_billing"`
	Health        Availability  `json:"health"`
	Models        []ModelStatus `json:"models"`
}

type ModelStatus struct {
	Model         string        `json:"model"`
	ManualEnabled bool          `json:"manual_enabled"`
	Health        Availability  `json:"health"`
	LastError     string        `json:"last_error"`
	FailCount     int           `json:"fail_count"`
	LastSuccessAt *time.Time    `json:"last_success_at,omitempty"`
	DisabledUntil *time.Time    `json:"disabled_until,omitempty"`
	Source        string        `json:"source,omitempty"`
}

type RouteRequest struct {
	RequestID      string
	RequestedModel string
	VirtualModel   string
	StartedAt      time.Time
}

type RouteAttempt struct {
	RequestID         string         `json:"request_id"`
	PreviousAttemptID *int64         `json:"previous_attempt_id,omitempty"`
	StepNo            int            `json:"step_no"`
	Action            string         `json:"action"`
	Model             string         `json:"model"`
	// Channel 三种粒度（与 AggregateTarget 对齐）：ChannelID（单 Key 兼容）>
	// ChannelIDs（Key 多选）> ChannelBaseURL（渠道级，按 base_url 整组轮询 Key）。
	// 聚合目标 rejected 时不再丢失 Key 多选 / 渠道级的渠道上下文，前端日志能渲染
	// 完整的 "@ 渠道名(Key1, Key2)" 而非空 channel_id。
	ChannelID         string         `json:"channel_id,omitempty"`
	ChannelIDs        []string       `json:"channel_ids,omitempty"`
	ChannelBaseURL    string         `json:"channel_base_url,omitempty"`
	// ChannelName 渠道名称快照：Key 被删除后仍能显示「@渠道名(Unknown)」。
	ChannelName       string         `json:"channel_name,omitempty"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        *time.Time     `json:"finished_at,omitempty"`
	// FirstByteAt 流式尝试收到上游响应头的时刻（TTFB）。仅流式 attempt 有值，
	// 运行中由 model-gateway 写入，前端据此展示"等待响应 Xs → 输出中 Ys"。
	FirstByteAt       *time.Time     `json:"first_byte_at,omitempty"`
	Result            string         `json:"result"`
	FailureClass      string         `json:"failure_class,omitempty"`
	StatusCode        int            `json:"status_code,omitempty"`
	// ErrorMessage 上游错误摘要（解析后的 message 字段或网络错误一行说明）：
	// 短文本，便于列表和折叠面板首屏展示完整信息。
	ErrorMessage      string         `json:"error_message,omitempty"`
	// ErrorBody 上游原始错误响应体（截断 8KB），与 ErrorMessage 互补：ErrorMessage
	// 只承载一行摘要，ErrorBody 保留厂商返回的完整 JSON（code/msg/extError/usage 等），
	// 400/429/500 根因排查时不必再翻 slog 文件。前端折叠面板里 detail 渲染。
	ErrorBody         string         `json:"error_body,omitempty"`
	Duration          DurationMS     `json:"duration_ms"`
	Stream            bool           `json:"stream,omitempty"`
	PromptTokens      int            `json:"prompt_tokens,omitempty"`
	CompletionTokens  int            `json:"completion_tokens,omitempty"`
	CachedTokens      int            `json:"cached_tokens,omitempty"`
	// Metadata 结构化扩展信息（如视觉识别的 called_via_tool/tool/image_id/prompt）。
	// 序列化给前端：UI 据此渲染 MCP 工具调用标签；内容由各插件写入，应只放展示级字段。
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// TokenUsage 描述一次上游响应里的 usage 字段，四项均为 OpenAI 标准键名。
// TotalTokens = prompt_tokens + completion_tokens（火山引擎免费额度按 total 扣减）。
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type RouteFinish struct {
	RequestID         string
	FinishedAt        time.Time
	Result            string
	FinalModel        string
	// Final 三种粒度（与 AggregateTarget / RouteAttempt 对齐）：ChannelID 单 Key 兼容、
	// ChannelIDs Key 多选、ChannelBaseURL 渠道级。聚合目标被拦截（proxyRejectedLog）
	// 时不再丢掉多选/渠道级渠道上下文，前端最终目标列渲染 "@ 渠道名(Key1, Key2)"。
	FinalChannelID    string
	FinalChannelIDs   []string
	FinalChannelBaseURL string
	// FinalChannelName 最终渠道名称快照：Key 被删除后仍能显示「@渠道名(Unknown)」。
	FinalChannelName  string
	HTTPStatus        int
	Duration          DurationMS
	// ErrorMessage 失败行一行摘要（与 RouteAttempt.ErrorMessage 同源）。
	ErrorMessage      string
	// ErrorBody 最后一次渠道尝试的上游原始响应体（截断 8KB）。Finish 阶段锁定，
	// 写入 route_requests.error_body 后 list 行也能直接展示，detail /api/route-logs/{id}
	// 也会按 fail-over 顺序保留每个 attempt 的 raw body 供逐渠道对比。
	ErrorBody         string
	Stream            bool
	PromptTokens      int
	CompletionTokens  int
	CachedTokens      int
}

type RouteLogFilter struct {
	Model         string
	ChannelID     string
	Result        string
	StartedAfter  *time.Time
	StartedBefore *time.Time
	Limit         int
}

type RouteRequestView struct {
	RequestID            string         `json:"request_id"`
	RequestedModel       string         `json:"requested_model"`
	VirtualModel         string         `json:"virtual_model,omitempty"`
	StartedAt            time.Time      `json:"started_at"`
	FinishedAt           *time.Time     `json:"finished_at,omitempty"`
	Result               string         `json:"result"`
	FinalModel           string         `json:"final_model,omitempty"`
	FinalChannelID       string         `json:"final_channel_id,omitempty"`
	FinalChannelIDs      []string       `json:"final_channel_ids,omitempty"`
	FinalChannelBaseURL  string         `json:"final_channel_base_url,omitempty"`
	FinalChannelName     string         `json:"final_channel_name,omitempty"`
	HTTPStatus           int            `json:"http_status,omitempty"`
	Duration             DurationMS     `json:"duration_ms"`
	ErrorMessage         string         `json:"error_message,omitempty"`
	// ErrorBody 最后一次渠道尝试的上游原始响应体（截断 8KB），
	// 与 Attempts[*].ErrorBody 同源；list 视图无需逐条展开就能拿到完整 JSON 错误。
	ErrorBody            string         `json:"error_body,omitempty"`
	Stream               bool           `json:"stream,omitempty"`
	PromptTokens         int            `json:"prompt_tokens,omitempty"`
	CompletionTokens     int            `json:"completion_tokens,omitempty"`
	CachedTokens         int            `json:"cached_tokens,omitempty"`
	Attempts             []RouteAttempt `json:"attempts,omitempty"`
}

type RouteLog interface {
	Start(context.Context, RouteRequest) error
	Attempt(context.Context, RouteAttempt) (int64, error)
	Finish(context.Context, RouteFinish) error
	List(context.Context, RouteLogFilter) ([]RouteRequestView, error)
	Detail(context.Context, string) (RouteRequestView, error)
	Clear(context.Context, time.Time) error
	// SelfHeal 兜底：用于"Start 写 running 但 Finish 异常中断"的卡死记录。
	// 当 result='running' 且 finished_at 为空 且距 started_at 超过 threshold 时，
	// 把 finished_at 设为 now、duration_ms 设为 now-started_at、result 设为
	// stream_interrupted 并写一条明确的 error_message，返回的 Detail 会拿到完整字段。
	// 其它情况 no-op。错误返回仅作日志参考，不影响主路径。
	SelfHeal(ctx context.Context, requestID string, threshold time.Duration) error
}
