package failurerules

import (
	"strings"
	"testing"
	"time"
)

// TestExtractRecoverAtFromMessage 锁定用户要求的核心能力：
// 上游错误文案里明确给出「重置时间」时（如 code 6004 的限速消息），
// 规则应该能把时间**提取出来**交给动作，冷却到那个时刻，而不是拍脑袋 2 分钟。
func TestExtractRecoverAtFromMessage(t *testing.T) {
	// 用户实测的错误体：msg 里带「2026-09-23 15:48:27 UTC+8 重置」。
	ev := Evidence{
		StatusCode: 429,
		Message:    `上游返回错误(429) {"code":6004,"msg":"您的使用量已超出频率限制，将在 2026-09-23 15:48:27 UTC+8 重置，您也可以切换其他模型继续使用。","requestId":"18c7e216"}`,
	}
	got, ok := extractRecoverAt(ev.Message, time.Now())
	if !ok {
		t.Fatal("应从错误文案中提取到重置时间")
	}
	want := time.Date(2026, 9, 23, 15, 48, 27, 0, beijingTZ)
	if !got.Equal(want) {
		t.Fatalf("提取时间应为 %s，实际 %s", want, got)
	}
	// 文案里没有时间：不提取（ok=false），走规则本身的恢复策略。
	if _, ok := extractRecoverAt("quota exhausted", time.Now()); ok {
		t.Fatal("无时间文案不应误提取")
	}
	// 提取到的时间在过去（上游时钟漂移）：按无效处理，避免立刻恢复失去冷却意义。
	past := `将在 2020-01-01 00:00:00 UTC+8 重置`
	if _, ok := extractRecoverAt(past, time.Now()); ok {
		t.Fatal("过去时间不应生效")
	}
}

// TestExtractRecoverAtFormats 覆盖常见的时间写法（不同平台格式不一）。
func TestExtractRecoverAtFormats(t *testing.T) {
	cases := []struct{ msg, want string }{
		{"将于 2026-09-23 15:48:27 UTC+8 重置", "2026-09-23T15:48:27+08:00"},
		{"reset at 2026-09-23 15:48:27 GMT+8", "2026-09-23T15:48:27+08:00"},
		{"将于 2026-09-23 15:48 重置", "2026-09-23T15:48:00+08:00"},
		{"reset at 2026/09/23 15:48:27 UTC+8", "2026-09-23T15:48:27+08:00"},
	}
	for _, c := range cases {
		got, ok := extractRecoverAt(c.msg, time.Date(2026, 9, 23, 0, 0, 0, 0, beijingTZ))
		if !ok {
			t.Fatalf("%s: 应提取到时间", c.msg)
		}
		if got.Format(time.RFC3339) != c.want {
			t.Fatalf("%s: 应为 %s，实际 %s", c.msg, c.want, got.Format(time.RFC3339))
		}
	}
}

// TestActionWithExtractedTime 动作层面：规则声明「从文案提取时间」后，
// 恢复点必须等于提取到的时刻（而不是 now+cooldown）。
func TestActionWithExtractedTime(t *testing.T) {
	ev := Evidence{Message: `将在 2099-01-02 03:04:05 UTC+8 重置`}
	a := Action{Verdict: VerdictCooldown, Recover: "fixed", CooldownSeconds: 120, ExtractRecoverAt: true}
	got, timed := nextRecoveryWithEvidence(a, ev, time.Date(2099, 1, 1, 0, 0, 0, 0, beijingTZ))
	if !timed {
		t.Fatal("应产出定时恢复")
	}
	want := time.Date(2099, 1, 2, 3, 4, 5, 0, beijingTZ)
	if !got.Equal(want) {
		t.Fatalf("恢复点应为提取的 %s，实际 %s", want, got)
	}
	// 提取不到时回退 now+cooldown，规则照常工作。
	ev2 := Evidence{Message: "no time here"}
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, beijingTZ)
	got2, _ := nextRecoveryWithEvidence(a, ev2, now)
	if !got2.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("提取失败应回退 cooldown，实际 %s", got2)
	}
}

// TestReplaySurfacesActionParams 锁定用户要求：样本校验（dry-run）要能展示
// 命中后动作的参数——比如从文案提取的恢复时间、冷却秒数——而不只是一个 verdict。
func TestReplaySurfacesActionParams(t *testing.T) {
	got := replaySummaryOf(VerdictCooldown, "2099-01-02 03:04:05")
	if !strings.Contains(got, "2099-01-02 03:04:05") {
		t.Fatal("回放结果应包含格式化的恢复时间")
	}
}

// replaySummaryOf 复刻前端的参数展示逻辑（本包内自持一份用于测试口径）。
func replaySummaryOf(verdict, until string) string {
	return actionParamsText(verdict, until, 0)
}
