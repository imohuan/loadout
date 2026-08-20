package volcfreequota

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeModelName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"豆包·Doubao-pro-32k", "doubao-pro-32k"},
		{"方舟 Doubao 1.5 lite 32k", "doubao1.5lite32k"},
		{"", ""},
		{"doubao-1-5-pro-32k-250115", "doubao-1-5-pro-32k-250115"},
		{"豆包大模型·Doubao-lite-4k(2025-03-01)", "doubao-lite-4k2025-03-01"},
	}
	for _, c := range cases {
		if got := normalizeModelName(c.in); got != c.want {
			t.Errorf("normalizeModelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCalcUsedAmount(t *testing.T) {
	cases := []struct {
		total, avail, want string
	}{
		{"100.00", "40.00", "60"},
		{"100", "100", "0"},
		{"10", "20", "0"},   // 异常数据：available > total → 0
		{"abc", "5", "0"},   // 解析失败 → 0
		{"", "", "0"},
	}
	for _, c := range cases {
		if got := calcUsedAmount(c.total, c.avail); got != c.want {
			t.Errorf("calcUsedAmount(%q, %q) = %q, want %q", c.total, c.avail, got, c.want)
		}
	}
}

func TestLooksLikeArkFreePackage(t *testing.T) {
	cases := []struct {
		name string
		pkg  rawPackage
		want bool
	}{
		{"product 含 ark", rawPackage{Product: "ark_free_tokens", ProductName: "Doubao-pro-32k", Status: "Effective"}, true},
		{"product_name 含 豆包", rawPackage{Product: "xxx", ProductName: "豆包大模型", Status: "Effective"}, true},
		{"product_name 含 doubao", rawPackage{Product: "xxx", ProductName: "Doubao 1.5 pro", Status: "Effective"}, true},
		{"已用完也算", rawPackage{Product: "ark", Status: "UsedUp"}, true},
		{"过期跳过", rawPackage{Product: "ark", Status: "Expired"}, false},
		{"非方舟", rawPackage{Product: "ecs", ProductName: "云服务器", Status: "Effective"}, false},
	}
	for _, c := range cases {
		if got := c.pkg.looksLikeArkFreePackage(); got != c.want {
			t.Errorf("%s: looksLikeArkFreePackage() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMatchQuotaToAPIModels(t *testing.T) {
	s := &Service{}
	apiModels := []string{
		"doubao-1-5-pro-32k-250115",
		"doubao-1-5-lite-32k-250115",
		"deepseek-v3-250324",
	}
	// product_name 归一化 "doubao-pro-32k" 应命中 "doubao-1-5-pro-32k-250115"（双向包含）。
	got := s.matchQuotaToAPIModels("doubao-pro-32k", apiModels)
	if len(got) != 1 || got[0] != "doubao-1-5-pro-32k-250115" {
		t.Errorf("matchQuotaToAPIModels(doubao-pro-32k) = %v, want [doubao-1-5-pro-32k-250115]", got)
	}
	// 无交集 → 空。
	if got := s.matchQuotaToAPIModels("gpt-4o", apiModels); len(got) != 0 {
		t.Errorf("matchQuotaToAPIModels(gpt-4o) = %v, want []", got)
	}
	// 同规格不同版本（128k vs 32k）→ 不应命中。
	if got := s.matchQuotaToAPIModels("doubao-pro-128k", apiModels); len(got) != 0 {
		t.Errorf("matchQuotaToAPIModels(doubao-pro-128k) = %v, want []", got)
	}
	// 空 apiModels → nil。
	if got := s.matchQuotaToAPIModels("doubao-pro-32k", nil); got != nil {
		t.Errorf("matchQuotaToAPIModels with nil = %v, want nil", got)
	}
}

func TestShareSignificantToken(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"doubao-pro-32k", "doubao-1-5-pro-32k-250115", true},   // 共享 32k
		{"doubao-lite-4k", "doubao-1-5-lite-4k-250115", true},   // 共享 4k
		{"doubao-pro-32k", "doubao-1-5-pro-128k-250115", false}, // 规格不同
		{"gpt-4o", "doubao-1-5-pro-32k-250115", false},          // 完全不相关
		{"pro-32k", "pro-32k", true},
	}
	for _, c := range cases {
		if got := shareSignificantToken(c.a, c.b); got != c.want {
			t.Errorf("shareSignificantToken(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUntilNextDayMidnightLocal(t *testing.T) {
	got := untilNextDayMidnightLocal()
	now := time.Now()
	// 结果必须严格晚于当前时间，且与"明天零点"的本地时区表示一致。
	if !got.After(now) {
		t.Errorf("untilNextDayMidnightLocal() = %v, 应晚于当前时间 %v", got, now)
	}
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Errorf("untilNextDayMidnightLocal() = %v, 应为本地 0 点整", got)
	}
	// 与 now 的差值在 (0, 48h] 内。
	d := got.Sub(now)
	if d <= 0 || d > 48*time.Hour {
		t.Errorf("untilNextDayMidnightLocal() 距现在 %v，超出合理范围", d)
	}
}

func TestNormalizeModelNameLowerCase(t *testing.T) {
	// 归一化输出必须全部小写（匹配逻辑依赖）。
	for _, in := range []string{"DOUBAO-PRO-32K", "豆包·Doubao-pro-32k", "Doubao Lite 4k"} {
		got := normalizeModelName(in)
		if strings.ToLower(got) != got {
			t.Errorf("normalizeModelName(%q) = %q 未全部小写", in, got)
		}
	}
}

func TestNewVolcBillingClientDefaults(t *testing.T) {
	c := newVolcBillingClient(nil)
	if c.region != volcBillingRegion {
		t.Errorf("region = %q, want %q", c.region, volcBillingRegion)
	}
	// 默认 Status 过滤必须只含 Effective/UsedUp（这两个状态才有额度判断价值）。
	if len(c.statusFilter) != 2 || c.statusFilter[0] != "Effective" || c.statusFilter[1] != "UsedUp" {
		t.Errorf("statusFilter = %v, want [Effective UsedUp]", c.statusFilter)
	}
	// 限流器速率 <= 接口上限 10 QPS。
	if c.limiter == nil {
		t.Fatal("limiter 未初始化")
	}
	if got := c.limiter.Limit(); got > 10 {
		t.Errorf("limiter rate = %v, 应 <= 10 (接口 QPS 上限)", got)
	}
	// 翻页上限必须小（防无限翻页打爆接口）：默认 10 页 = 200 条，覆盖免费模型数量级足够。
	if c.maxPages != 10 {
		t.Errorf("maxPages = %d, want 10", c.maxPages)
	}
	if c.maxPages > 20 {
		t.Errorf("maxPages = %d, 超过用户要求的 20 页上限", c.maxPages)
	}
}
