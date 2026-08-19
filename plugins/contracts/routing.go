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
}

type ChannelStatus struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
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
	ChannelID         string         `json:"channel_id,omitempty"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        *time.Time     `json:"finished_at,omitempty"`
	Result            string         `json:"result"`
	FailureClass      string         `json:"failure_class,omitempty"`
	StatusCode        int            `json:"status_code,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	Duration          DurationMS     `json:"duration_ms"`
	Stream            bool           `json:"stream,omitempty"`
	PromptTokens      int            `json:"prompt_tokens,omitempty"`
	CompletionTokens  int            `json:"completion_tokens,omitempty"`
	CachedTokens      int            `json:"cached_tokens,omitempty"`
	Metadata          map[string]any `json:"-"`
}

// TokenUsage 描述一次上游响应里的 usage 字段，三项均为 OpenAI 标准键名。
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type RouteFinish struct {
	RequestID        string
	FinishedAt       time.Time
	Result           string
	FinalModel       string
	FinalChannelID   string
	HTTPStatus       int
	Duration         DurationMS
	ErrorMessage     string
	Stream           bool
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
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
	RequestID      string         `json:"request_id"`
	RequestedModel string         `json:"requested_model"`
	VirtualModel   string         `json:"virtual_model,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
	Result         string         `json:"result"`
	FinalModel     string         `json:"final_model,omitempty"`
	FinalChannelID string         `json:"final_channel_id,omitempty"`
	HTTPStatus     int            `json:"http_status,omitempty"`
	Duration       DurationMS     `json:"duration_ms"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	PromptTokens   int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int          `json:"completion_tokens,omitempty"`
	CachedTokens   int            `json:"cached_tokens,omitempty"`
	Attempts       []RouteAttempt `json:"attempts,omitempty"`
}

type RouteLog interface {
	Start(context.Context, RouteRequest) error
	Attempt(context.Context, RouteAttempt) (int64, error)
	Finish(context.Context, RouteFinish) error
	List(context.Context, RouteLogFilter) ([]RouteRequestView, error)
	Detail(context.Context, string) (RouteRequestView, error)
	Clear(context.Context, time.Time) error
}
