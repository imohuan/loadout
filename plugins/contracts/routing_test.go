package contracts

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRouteAttemptJSONFieldNames guards the wire format consumed by the
// frontend (frontend/src/lib/types.ts -> RouteAttempt). Drift here will make
// attempts render as "-" in the forwarding log UI.
func TestRouteAttemptJSONFieldNames(t *testing.T) {
	previous := int64(7)
	finished := time.Date(2026, 8, 17, 13, 4, 7, 0, time.UTC)
	dur := DurationMS(2 * time.Second)
	attempt := RouteAttempt{
		RequestID:         "req-1",
		PreviousAttemptID: &previous,
		StepNo:            1,
		Action:            "首次尝试",
		Model:             "claude-haiku-4-5-20251001",
		ChannelID:         "b89c685dac7402fd",
		StartedAt:         time.Date(2026, 8, 17, 13, 4, 5, 0, time.UTC),
		FinishedAt:        &finished,
		Result:            "skipped",
		FailureClass:      "balance",
		StatusCode:        502,
		ErrorMessage:      "model disabled",
		Duration:          dur,
		Metadata:          map[string]any{"foo": "bar"},
	}

	raw, err := json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal RouteAttempt: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal RouteAttempt: %v", err)
	}

	wantKeys := []string{
		"request_id", "previous_attempt_id", "step_no", "action", "model",
		"channel_id", "started_at", "finished_at", "result", "failure_class",
		"status_code", "error_message", "duration_ms",
	}
	for _, key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected JSON key %q in RouteAttempt, got: %v", key, keys(decoded))
		}
	}
	forbidden := []string{
		"RequestID", "PreviousAttemptID", "StepNo", "Action", "Model",
		"ChannelID", "StartedAt", "FinishedAt", "Result", "FailureClass",
		"StatusCode", "ErrorMessage", "Duration", "Metadata",
		"Stream", "PromptTokens", "CompletionTokens", "CachedTokens",
	}
	for _, key := range forbidden {
		if _, ok := decoded[key]; ok {
			t.Errorf("unexpected PascalCase JSON key %q in RouteAttempt", key)
		}
	}

	if got := decoded["duration_ms"]; got != float64(2000) {
		t.Errorf("duration_ms: want 2000, got %v", got)
	}
	if decoded["status_code"].(float64) != 502 {
		t.Errorf("status_code: want 502, got %v", decoded["status_code"])
	}
}

func TestRouteAttemptOmitEmptyUsage(t *testing.T) {
	attempt := RouteAttempt{
		RequestID: "req-1", StepNo: 1, Action: "首次尝试", Model: "m",
		StartedAt: time.Now(), Duration: DurationMS(time.Second),
	}
	raw, err := json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 零值字段（stream=false / tokens=0）必须被 omitempty 省略，避免污染无 usage 的 attempt 行。
	for _, key := range []string{"stream", "prompt_tokens", "completion_tokens", "cached_tokens"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("expected key %q to be omitted when zero, got %v", key, decoded[key])
		}
	}

	// 非零 token 必须输出，方便 UI 直接读取。
	attempt.Stream = true
	attempt.PromptTokens = 84
	attempt.CompletionTokens = 30
	attempt.CachedTokens = 12
	raw, _ = json.Marshal(attempt)
	decoded = map[string]any{}
	_ = json.Unmarshal(raw, &decoded)
	if decoded["stream"] != true {
		t.Errorf("stream: want true, got %v", decoded["stream"])
	}
	if got, _ := decoded["prompt_tokens"].(float64); got != 84 {
		t.Errorf("prompt_tokens: want 84, got %v", got)
	}
	if got, _ := decoded["completion_tokens"].(float64); got != 30 {
		t.Errorf("completion_tokens: want 30, got %v", got)
	}
	if got, _ := decoded["cached_tokens"].(float64); got != 12 {
		t.Errorf("cached_tokens: want 12, got %v", got)
	}
}

func TestRouteAttemptDurationMSMillis(t *testing.T) {
	cases := []struct {
		d    DurationMS
		want int64
	}{
		{DurationMS(0), 0},
		{DurationMS(1500 * time.Millisecond), 1500},
		{DurationMS(2 * time.Second), 2000},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c.d)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var v int64
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if v != c.want {
			t.Errorf("DurationMS(%v): want %d ms, got %d ms", time.Duration(c.d), c.want, v)
		}
	}
}

func TestRouteRequestViewDurationMS(t *testing.T) {
	view := RouteRequestView{
		RequestID:      "req-1",
		RequestedModel: "gpt-4o",
		StartedAt:      time.Now(),
		Result:         "success",
		Duration:       DurationMS(3 * time.Second),
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := decoded["duration_ms"]; got != float64(3000) {
		t.Errorf("duration_ms: want 3000, got %v", got)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
