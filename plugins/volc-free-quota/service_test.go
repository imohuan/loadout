package volcfreequota

import (
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
)

func TestNormalizeModelName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"豆包·Doubao-pro-32k", "doubao-pro-32k"},
		{"方舟 Doubao 1.5 lite 32k", "doubao1-5lite32k"},
		{"", ""},
		{"doubao-1-5-pro-32k-250115", "doubao-1-5-pro-32k-250115"},
		{"豆包大模型·Doubao-lite-4k(2025-03-01)", "doubao-lite-4k2025-03-01"},
		{"Doubao_Seed_2.1_pro_data_collaboration", "doubao-seed-2-1-pro-data-collaboration"},
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

// newTestService 构造内存库 Service（含迁移），测试本地递减/耗尽判定用。
func newTestService(t *testing.T) *Service {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(database, st, nil)
	// 账号指纹必须用真实计算值：accountIDForChannel 按 AK 算 SHA256 指纹。
	aid := accountID("AKxxx")
	// 预置一条渠道 + 配额配置 + 额度记录，绕过 refreshOne 的火山 SDK 调用。
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO channels(id, name, base_url, created_at, updated_at) VALUES ('ch1', 'ark1', 'https://ark.cn-beijing.volces.com/api/v3', 'now', 'now')`)
	mustExec(`INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES ('ch1', 'AKxxx', ?, 'cipher', 1, 1, 'now')`, aid)
	// v15 起扣减/拦截锚点是 volc_quota_packages.model（configuration_code 提取名）。
	mustExec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
	       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, synced_at)
		VALUES (?, 'inst-1', 'ark_bd', '豆包·Doubao-pro-32k', 'Doubao_Pro_32k_data_collaboration', 'Doubao-pro-32k协作奖励计划资源包',
		        'doubao-pro-32k', 2000000, 2000000, 0, 2000000, 2000000, 'Tokens', 'Effective', 'now')`, aid)
	return svc
}

func TestDecrementLocalRemaining(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// 第一次扣 500k，剩 1500k。
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 500000)
	var remaining int64
	var status string
	if err := svc.db.QueryRow(`SELECT local_remaining, status FROM volc_quota_packages WHERE account_id=? AND model= 'doubao-pro-32k'`, aid).Scan(&remaining, &status); err != nil {
		t.Fatal(err)
	}
	if remaining != 1500000 {
		t.Errorf("第一次扣减后 local_remaining = %d, want 1500000", remaining)
	}
	if status != "Effective" {
		t.Errorf("第一次扣减后 status = %q, want Effective", status)
	}
	// 第二次扣 1600k，超过剩余 → 归零 + UsedUp。
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 1600000)
	if err := svc.db.QueryRow(`SELECT local_remaining, status FROM volc_quota_packages WHERE account_id=? AND model= 'doubao-pro-32k'`, aid).Scan(&remaining, &status); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("超额扣减后 local_remaining = %d, want 0", remaining)
	}
	if status != "UsedUp" {
		t.Errorf("超额扣减后 status = %q, want UsedUp", status)
	}
}

// TestDecrementLocalRemainingSharesAcrossPackages 回归：同 model 多个资源包时，
// 一次请求的 tokens 必须按余额分摊（先扣大的，扣到 0 再扣下一个），
// 不能每个包都扣全量（P1 修复前会重复扣减）。
func TestDecrementLocalRemainingSharesAcrossPackages(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := svc.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 同 model（deepseek-v4-flash-0731）两个包，余额分别为 300 和 100。
	for _, c := range []struct{ inst string; bal int64 }{
		{"inst-a", 300}, {"inst-b", 100},
	} {
		mustExec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
		       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, synced_at)
			VALUES (?, ?, 'ark_bd', 'x', 'DeepSeek_V4_flash_0731_data_collaboration_resource_pack', 'pack', 'deepseek-v4-flash-0731',
			        ?, ?, 0, ?, ?, 'Tokens', 'Effective', 'now')`,
			aid, c.inst, c.bal, c.bal, c.bal, c.bal)
	}
	// 一次请求扣 250：先扣余额大的 inst-a(300→50)，inst-b 不动（100）。
	// 分摊策略=优先扣余额大的包，扣完还有剩余才动下一个。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 250)
	var a, b int64
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id=? AND instance_no='inst-a'`, aid).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id=? AND instance_no='inst-b'`, aid).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a != 50 {
		t.Errorf("inst-a local_remaining = %d, want 50（应扣 250）", a)
	}
	if b != 100 {
		t.Errorf("inst-b local_remaining = %d, want 100（inst-a 未扣完不动它）", b)
	}
	// 总额守恒：300+100-250 = 150 = 50+100。
	if a+b != 150 {
		t.Errorf("总额不守恒: a+b=%d, want 150", a+b)
	}
	// 再扣 100：此时 inst-b(100) 余额最大 → 先扣它 100 归零 UsedUp，inst-a(50) 不动。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 100)
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id=? AND instance_no='inst-a'`, aid).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id=? AND instance_no='inst-b'`, aid).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a != 50 {
		t.Errorf("第二次后 inst-a local_remaining = %d, want 50（非最大余额不动）", a)
	}
	if b != 0 {
		t.Errorf("第二次后 inst-b local_remaining = %d, want 0（余额最大先扣完）", b)
	}
}

func TestCheckAllCandidatesExhaustedLocalFirst(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// 本地余额还有 → 不拦截。
	exhausted, allExhausted, err := svc.checkAllCandidatesExhausted([]string{"ch1"}, "doubao-1-5-pro-32k-250115")
	if err != nil {
		t.Fatal(err)
	}
	if exhausted || allExhausted {
		t.Errorf("本地余额未耗尽时应放行, got exhausted=%v allExhausted=%v", exhausted, allExhausted)
	}
	// 扣到 0 → 拦截。
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 2000000)
	exhausted, allExhausted, err = svc.checkAllCandidatesExhausted([]string{"ch1"}, "doubao-1-5-pro-32k-250115")
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted || !allExhausted {
		t.Errorf("本地余额耗尽时应拦截, got exhausted=%v allExhausted=%v", exhausted, allExhausted)
	}
	// 不匹配的 model 不受影响。
	exhausted, _, err = svc.checkAllCandidatesExhausted([]string{"ch1"}, "gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if exhausted {
		t.Error("不匹配的 model 不应判定耗尽")
	}
}

func TestHandleProxyUpstreamSucceededDecrements(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// 模拟一次成功响应：model 用真实 API 名，usage.total_tokens=200000。
	_, err := svc.HandleProxyUpstreamSucceeded(&modelgateway.ProxySuccessPayload{
		ChannelID: "ch1",
		Model:     "doubao-1-5-pro-32k-250115",
		Usage:     contracts.TokenUsage{TotalTokens: 200000},
	})
	if err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE account_id=? AND model= 'doubao-pro-32k'`, aid).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1800000 {
		t.Errorf("success 后 local_remaining = %d, want 1800000", remaining)
	}
	// 使用记录也应写入。
	var count int64
	if err := svc.db.QueryRow(`SELECT use_count FROM volc_quota_usage WHERE account_id=? AND model='doubao-1-5-pro-32k-250115'`, aid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("use_count = %d, want 1", count)
	}
}

func TestModelNameFromConfigCode(t *testing.T) {
	cases := []struct {
		code, product, want string
	}{
		{"DeepSeek_V4_flash_0731_data_collaboration_resource_pack", "ark_open_source_llm", "deepseek-v4-flash-0731"},
		{"Doubao_Seed3D_1.0_pack_free_infer", "ark_bd", "doubao-seed3d-1-0"},
		{"Doubao_Seed_2.1_pro_data_collaboration", "ark_bd", "doubao-seed-2-1-pro"},
		{"ym-rodin-gen2-free", "ark_ym_sanfang", "ym-rodin-gen2"},
		{"hitem3D-2.0-free", "ark_sm_sanfang", "hitem3d-2-0"},
		{"", "ark_bd", "ark-bd"},
	}
	for _, c := range cases {
		if got := modelNameFromConfigCode(c.code, c.product); got != c.want {
			t.Errorf("modelNameFromConfigCode(%q, %q) = %q, want %q", c.code, c.product, got, c.want)
		}
	}
}

func TestSameDateToken(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0731", "260731", true},   // code 月日 ↔ API 年月日
		{"260731", "0731", true},   // 反向
		{"0731", "0731", true},     // 同 4 位
		{"260731", "260731", true}, // 同 6 位
		{"32", "128", false},       // 非日期不相等
		{"0731", "260801", false},  // 不同日
		{"260731", "260801", false},
	}
	for _, c := range cases {
		if got := sameDateToken(c.a, c.b); got != c.want {
			t.Errorf("sameDateToken(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMatchOneConfigCodeToAPIModel(t *testing.T) {
	cases := []struct {
		quota, api string // quota=code 提取名(已归一化), api=请求模型名(未归一化)
		want       bool
	}{
		{"deepseek-v4-flash-0731", "deepseek-v4-flash-ga-260731", true}, // 日期归一化 + ga 修饰
		{"deepseek-v4-flash-0731", "deepseek-v4-flash-260731", true},    // 日期归一化
		{"deepseek-v4-flash", "deepseek-v4-flash-ga-260731", true},      // 双向包含
		{"glm-5-2", "glm-5-2-250922", true},                             // 双向包含
		{"doubao-seed-2-1-pro", "doubao-seed-2-1-pro-260628", true},     // 双向包含
		{"gpt-4o", "deepseek-v4-flash-ga-260731", false},                // 完全无关
		{"doubao-pro-32k", "doubao-pro-128k", false},                    // 不同规格
	}
	for _, c := range cases {
		if got := matchOne(c.quota, c.api); got != c.want {
			t.Errorf("matchOne(%q, %q) = %v, want %v", c.quota, c.api, got, c.want)
		}
	}
}
