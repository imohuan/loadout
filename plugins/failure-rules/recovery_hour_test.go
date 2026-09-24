package failurerules

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestDailyResetHourZeroIsMidnight 锁定「0 点」是合法值。
//
// 旧实现是 int + omitempty +「if hour == 0 { hour = 12 }」，把「明天 0 点恢复」
// 静默改成「明天中午 12 点」——用户设 0 点却永远等到中午，还以为定时恢复坏了。
func TestDailyResetHourZeroIsMidnight(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 30, 0, 0, beijingTZ)

	got, timed := nextRecoveryWithEvidence(
		Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: hourPtrValue(0)},
		Evidence{}, nil, now)
	if !timed {
		t.Fatal("daily 应产出定时恢复")
	}
	want := time.Date(2026, 9, 25, 0, 0, 0, 0, beijingTZ)
	if !got.Equal(want) {
		t.Fatalf("daily_reset_hour=0 应恢复于 %s，实际 %s", want, got)
	}
}

// TestDailyResetHourUnsetDefaultsNoon 没设置时仍按中午 12 点兜底。
func TestDailyResetHourUnsetDefaultsNoon(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 30, 0, 0, beijingTZ)
	got, _ := nextRecoveryWithEvidence(
		Action{Verdict: VerdictDisableKey, Recover: "daily"},
		Evidence{}, nil, now)
	want := time.Date(2026, 9, 25, 12, 0, 0, 0, beijingTZ)
	if !got.Equal(want) {
		t.Fatalf("未设置 daily_reset_hour 应默认 12 点，实际 %s", got)
	}
}

// TestDailyResetHourAllHours 0..23 每个整点都要落到对应时刻。
func TestDailyResetHourAllHours(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 30, 0, 0, beijingTZ)
	for h := 0; h <= 23; h++ {
		got, _ := nextRecoveryWithEvidence(
			Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: hourPtrValue(h)},
			Evidence{}, nil, now)
		if got.In(beijingTZ).Hour() != h {
			t.Errorf("daily_reset_hour=%d 恢复点却是 %s", h, got.In(beijingTZ))
		}
	}
}

// TestDailyResetHourJSONZeroRoundTrip 0 必须在 JSON 往返后存活。
//
// int + omitempty 会把 0 当作空值丢掉：前端设 0 点、存进库又变回「没设置」，
// 恢复点从 0 点漂回 12 点。
func TestDailyResetHourJSONZeroRoundTrip(t *testing.T) {
	a := Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: hourPtrValue(0)}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var back Action
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("反序列化失败 (%s): %v", b, err)
	}
	if back.DailyResetHour == nil || *back.DailyResetHour != 0 {
		t.Fatalf("0 点应在 JSON 往返后保留，实际 %v（json=%s）", back.DailyResetHour, b)
	}

	// 未设置时不写字段，读回来是 nil。
	noHour, _ := json.Marshal(Action{Verdict: VerdictDisableKey, Recover: "daily"})
	var backNo Action
	if err := json.Unmarshal(noHour, &backNo); err != nil {
		t.Fatal(err)
	}
	if backNo.DailyResetHour != nil {
		t.Fatalf("未设置时不应产生值，实际 %v（json=%s）", backNo.DailyResetHour, noHour)
	}
}

// TestValidateDailyResetHourRange 恢复点只能是 0..23。
func TestValidateDailyResetHourRange(t *testing.T) {
	base := RuleInput{
		Name:   "t",
		Action: Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: hourPtrValue(0)},
		Match:  Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: 429}}},
	}
	if err := validateInput(base); err != nil {
		t.Fatalf("0 点必须合法，实际报错: %v", err)
	}
	base.Action.DailyResetHour = nil
	if err := validateInput(base); err != nil {
		t.Fatalf("未设置必须合法，实际报错: %v", err)
	}
	for _, h := range []int{-1, 24, 99} {
		in := base
		in.Action.DailyResetHour = hourPtrValue(h)
		if err := validateInput(in); err == nil {
			t.Errorf("daily_reset_hour=%d 应被拒绝", h)
		}
	}
}

// TestExecutorWritesMidnightRecovery 端到端：写库那一刻 0 点就是次日 0 点。
func TestExecutorWritesMidnightRecovery(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := database.Exec("INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES ('c','C','http://c',1,0,'now','now')"); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	x := NewExecutor(database, nil)
	if _, err := x.Execute(context.Background(), ActionContext{
		Evidence: Evidence{ChannelID: "c", Model: "m"},
		Decision: Decision{Verdict: VerdictDisableKey},
		Action:   Action{Verdict: VerdictDisableKey, Recover: "daily", DailyResetHour: hourPtrValue(0)},
	}); err != nil {
		t.Fatal(err)
	}
	var untilStr string
	if err := database.QueryRow("SELECT disabled_until FROM model_states WHERE channel_id='c' AND model='m'").Scan(&untilStr); err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, untilStr)
	if err != nil {
		t.Fatalf("解析 disabled_until 失败 (%s): %v", untilStr, err)
	}
	if h := until.In(beijingTZ).Hour(); h != 0 {
		t.Fatalf("写库的恢复点应是 0 点，实际 %d 点（%s）", h, until.In(beijingTZ))
	}
	if m := until.In(beijingTZ).Minute(); m != 0 {
		t.Fatalf("恢复点应是整点，实际 %d 分", m)
	}
}

// TestParseTimeFlexUnixSeconds 秒级时间戳必须解析成真实时刻。
//
// 旧写法是 n > 10^12 —— Go 里 ^ 是异或，10^12 == 6，10^9 == 3。
// 于是任何大于 6 的数字都被当成毫秒戳：一亿七千万这种秒级戳落到 1970 年，
// 恢复点变成过去 → 等于立刻恢复，「按文案时刻恢复」彻底失效。
func TestParseTimeFlexUnixSeconds(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, beijingTZ)
	got, ok := parseTimeFlex("1789000000", now)
	if !ok {
		t.Fatal("秒级时间戳应能解析")
	}
	want := time.Unix(1789000000, 0).UTC()
	if !got.UTC().Equal(want) {
		t.Fatalf("秒级时间戳解析错误：want=%s got=%s", want.Format(time.RFC3339), got.UTC().Format(time.RFC3339))
	}
}

// TestParseTimeFlexUnixMillis 毫秒时间戳走毫秒分支。
func TestParseTimeFlexUnixMillis(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, beijingTZ)
	got, ok := parseTimeFlex("1789000000123", now)
	if !ok {
		t.Fatal("毫秒时间戳应能解析")
	}
	want := time.UnixMilli(1789000000123).UTC()
	if !got.UTC().Equal(want) {
		t.Fatalf("毫秒时间戳解析错误：want=%s got=%s", want.Format(time.RFC3339), got.UTC().Format(time.RFC3339))
	}
}
