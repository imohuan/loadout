package failurerules

import (
	"testing"
	"time"
)

// TestCaptureAndTemplate 锁定用户要求的通用提取机制：
// 正则条件里用捕获组（如 (\d{2})），动作字段里用 $1 引用捕获值。
// 与上一版写死的「提取时间开关」不同——这是一套通用的模板展开，
// 任何数值/时间/文本都能这么取。
func TestCaptureAndTemplate(t *testing.T) {
	ev := Evidence{StatusCode: 429, Message: `上游返回错误(429) {"code":6004,"msg":"您的使用量已超出频率限制，将在 2026-09-23 15:48:27 UTC+8 重置"}`}

	// 1) 捕获：正则的捕获组从文案里取出值。
	re, err := compileMessageRegex(`将在 (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC\+8`)
	if err != nil {
		t.Fatal("正则编译失败")
	}
	captures := captureGroups(re, ev.Message)
	if len(captures) < 2 || captures[1] != "2026-09-23 15:48:27" {
		t.Fatalf("应捕获到时间，实际 %v", captures)
	}

	// 2) 模板：$1 展开成捕获值。
	if got := expandTemplate("恢复时间 $1", captures); got != "恢复时间 2026-09-23 15:48:27" {
		t.Fatalf("模板展开错误，实际 %q", got)
	}
	if got := expandTemplate("冷却 $1 秒", []string{"retry after 300 seconds", "300"}); got != "冷却 300 秒" {
		t.Fatalf("模板展开错误，实际 %q", got)
	}
	// $0 = 整个匹配；不存在的组引用展开为空串（而不是字面 $2）。
	if got := expandTemplate("[$0][$2]", []string{"A", "B"}); got != "[A][]" {
		t.Fatalf("$0/$2 展开错误，实际 %q", got)
	}
}

// TestCaptureCooldownSeconds 直接还原用户场景：正则抓「冷却秒数」，
// 动作的 cooldown_seconds 用 $1 引用。
func TestCaptureCooldownSeconds(t *testing.T) {
	ev := Evidence{Message: "rate limited, retry after 300 seconds"}
	conds := []Condition{{Field: "message_text", Op: "regex", Value: `retry after (\d+) seconds`}}
	cs := compileConditions(conds)
	if !evalCondition(&cs[0], ev) {
		t.Fatal("正则条件应命中")
	}
	// 引擎求值后应把捕获值带回（lastCaptures 写回条件结构）。
	cap := cs[0].lastCaptures
	if len(cap) < 2 || cap[1] != "300" {
		t.Fatalf("应捕获到 300（$1），实际 %v", cap)
	}
}

// TestNextRecoveryWithCaptures 恢复时间的通用提取：
// cooldown_seconds 字段填模板「$1」，命中时捕获 300 → 冷却 300 秒。
func TestNextRecoveryWithCaptures(t *testing.T) {
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, beijingTZ)
	ev := Evidence{Message: "rate limited, retry after 300 seconds"}
	a := Action{Verdict: VerdictCooldown, Recover: "fixed", CooldownSecondsTemplate: "$1"}
	got, timed := nextRecoveryWithEvidence(a, ev, []string{"retry after 300 seconds", "300"}, now)
	if !timed {
		t.Fatal("应产出定时恢复")
	}
	want := now.Add(300 * time.Second)
	if !got.Equal(want) {
		t.Fatalf("应冷却到 %s（+300s），实际 %s", want, got)
	}
	// 捕获不到 → 回退 CooldownSeconds（模板展开为空时用默认 120）。
	ev2 := Evidence{Message: "no numbers here"}
	got2, _ := nextRecoveryWithEvidence(a, ev2, nil, now)
	if !got2.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("无捕获应回退默认 120s，实际 %s", got2)
	}
}

// TestRecoverUntilTemplate 恢复时刻也支持模板：
// recover 用 fixed 且 cooldown_seconds_template 填「$1 $2:$3:$4」这类拼接，
// 用户可以自己决定怎么组装时间（或干脆抓 Unix 时间戳）。
func TestRecoverUntilTemplate(t *testing.T) {
	ev := Evidence{Message: `将于 2026-09-23 15:48:27 UTC+8 重置`}
	a := Action{Verdict: VerdictCooldown, Recover: "fixed", RecoverAtTemplate: "$1"}
	got, timed := nextRecoveryWithEvidence(a, ev, []string{"2026-09-23 15:48:27 UTC+8", "2026-09-23 15:48:27"}, time.Date(2026, 9, 23, 0, 0, 0, 0, beijingTZ))
	if !timed {
		t.Fatal("应产出定时恢复")
	}
	want := time.Date(2026, 9, 23, 15, 48, 27, 0, beijingTZ)
	if !got.Equal(want) {
		t.Fatalf("恢复点应为 %s，实际 %s", want, got)
	}
}

// TestCaptureScopeGuards 捕获的安全护栏：
//   - 数值字段（cooldown_seconds）展开结果必须是数字，否则回退默认；
//   - 时间解析失败回退常规策略。
func TestCaptureScopeGuards(t *testing.T) {
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, beijingTZ)
	// 捕获到非数字 → 回退默认冷却。
	ev := Evidence{Message: "retry after abc seconds"}
	a := Action{Verdict: VerdictCooldown, Recover: "fixed", CooldownSecondsTemplate: "$1"}
	got, _ := nextRecoveryWithEvidence(a, ev, []string{"abc"}, now)
	if !got.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("非数字捕获应回退 120s，实际 %s", got)
	}
}
