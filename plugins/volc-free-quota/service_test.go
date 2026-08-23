package volcfreequota

import (
	"context"
	"strings"
	"sync"
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
	// cipher 用真实加密：部分测试（TestRefreshInFlight）需要 refreshOne 的 Decrypt 成功。
	cipher, _ := st.Encrypt("secret")
	mustExec(`INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES ('ch1', 'AKxxx', ?, ?, 1, 1, 'now')`, aid, cipher)
	// 预置 channel_models：syncModelStatesByAggregate 的 channelsAPIModels 需要非空才能匹配判定。
	mustExec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at)
		VALUES ('ch1', 'doubao-1-5-pro-32k-250115', 'probe', 1, 'now', 'now')`)
	mustExec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at)
		VALUES ('ch1', 'deepseek-v4-flash-ga-260731', 'probe', 1, 'now', 'now')`)
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

// TestCheckAllCandidatesExhaustedAggregatesAcrossPackages 回归：同模型多包时，
// 耗尽判据 = 聚合剩余 SUM(local_remaining)，不是任一包（避免"一个包剩 5000 其余 200 万"误拦）。
func TestCheckAllCandidatesExhaustedAggregatesAcrossPackages(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := svc.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 同 model 两包：余额 500 和 400000。
	for _, c := range []struct{ inst string; bal int64 }{
		{"inst-x", 500}, {"inst-y", 400000},
	} {
		mustExec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
		       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, synced_at)
			VALUES (?, ?, 'ark_bd', 'x', 'DeepSeek_V4_flash_0731_data_collaboration_resource_pack', 'pack', 'deepseek-v4-flash-0731',
			        ?, ?, 0, ?, ?, 'Tokens', 'Effective', 'now')`,
			aid, c.inst, c.bal, c.bal, c.bal, c.bal)
	}
	// 聚合剩余 = 500 + 400000 = 400500，远超阈值 → 不拦截。
	exhausted, allExhausted, err := svc.checkAllCandidatesExhausted([]string{"ch1"}, "deepseek-v4-flash-ga-260731")
	if err != nil {
		t.Fatal(err)
	}
	if exhausted || allExhausted {
		t.Errorf("聚合剩余充足时应放行, got exhausted=%v allExhausted=%v", exhausted, allExhausted)
	}
	// 把大包扣光：聚合剩余 = 500 + 0 = 500 > 默认阈值 0 → 仍不拦（小包还有余量）。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 400000)
	exhausted, allExhausted, err = svc.checkAllCandidatesExhausted([]string{"ch1"}, "deepseek-v4-flash-ga-260731")
	if err != nil {
		t.Fatal(err)
	}
	if exhausted || allExhausted {
		t.Errorf("聚合剩余 500 > 阈值 0 时应放行, got exhausted=%v allExhausted=%v", exhausted, allExhausted)
	}
	// 小包也扣光：聚合剩余 = 0 ≤ 阈值 0 → 拦截。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 500)
	exhausted, allExhausted, err = svc.checkAllCandidatesExhausted([]string{"ch1"}, "deepseek-v4-flash-ga-260731")
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted || !allExhausted {
		t.Errorf("聚合剩余归零时应拦截, got exhausted=%v allExhausted=%v", exhausted, allExhausted)
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

// insertPackage 测试辅助：插入一个资源包行（initial_total/local_remaining 同值）。
func insertPackage(t *testing.T, svc *Service, aid, inst, model, status string, bal int64) {
	t.Helper()
	_, err := svc.db.Exec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
		       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, synced_at)
		VALUES (?, ?, 'ark_bd', 'x', 'code', 'pack', ?, ?, ?, 0, ?, ?, 'Tokens', ?, 'now')`,
		aid, inst, model, bal, bal, bal, bal, status)
	if err != nil {
		t.Fatal(err)
	}
}

// TestAggregateLocalRemaining 聚合函数：多包求和、initial_total=0 排除、过期状态排除。
func TestAggregateLocalRemaining(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// newTestService 已预置 doubao-pro-32k 200 万；再插同 model 包 + 异 model 包。
	insertPackage(t, svc, aid, "inst-b", "doubao-pro-32k", "Effective", 500000)
	insertPackage(t, svc, aid, "inst-c", "deepseek-v4-flash-0731", "UsedUp", 0)
	insertPackage(t, svc, aid, "inst-d", "deepseek-v4-flash-0731", "Effective", 300000)
	insertPackage(t, svc, aid, "inst-e", "glm-5-2", "Expired", 900000)   // 过期排除
	insertPackage(t, svc, aid, "inst-f", "kimi-k2", "Effective", 100000) // initial_total=0 → 排除
	_, _ = svc.db.Exec(`UPDATE volc_quota_packages SET initial_total = 0 WHERE instance_no = 'inst-f'`)
	// 已过期包（expiry_time < now，status 仍 Effective）：运行期到期后 billing 不再返回，
	// 本地行残留正余额——必须按 expiry_time 排除，否则永久撑住聚合（code review P1）。
	_, _ = svc.db.Exec(`INSERT INTO volc_quota_packages(account_id, instance_no, product, product_name, configuration_code,
	       configuration_name, model, total_amount, available_amount, used_amount, initial_total, local_remaining, unit, status, expiry_time, synced_at)
		VALUES (?, 'inst-expired', 'ark_bd', 'x', 'code', 'pack', 'doubao-pro-32k',
		        900000, 900000, 0, 900000, 900000, 'Tokens', 'Effective', '2026-08-01T00:00:00Z', 'now')`, aid)

	agg := svc.aggregateLocalRemaining(aid)
	if agg["doubao-pro-32k"] != 2500000 { // 200万 + 50万
		t.Errorf("doubao-pro-32k 聚合 = %d, want 2500000", agg["doubao-pro-32k"])
	}
	if agg["deepseek-v4-flash-0731"] != 300000 { // 0 + 30万
		t.Errorf("deepseek-v4-flash-0731 聚合 = %d, want 300000", agg["deepseek-v4-flash-0731"])
	}
	if _, ok := agg["glm-5-2"]; ok {
		t.Error("Expired 状态的包不应计入聚合")
	}
	if _, ok := agg["kimi-k2"]; ok {
		t.Error("initial_total=0 的包不应计入聚合")
	}
	if agg["doubao-pro-32k"] != 2500000 {
		t.Errorf("doubao-pro-32k 聚合 = %d, want 2500000（含过期包 90 万则错误）", agg["doubao-pro-32k"])
	}
}

// beijingTime 构造指定北京时间的 time.Time（转 UTC 存储，比较用）。
func beijingTime(y int, mo time.Month, d, h, mi int) time.Time {
	beijing := time.FixedZone("Asia/Shanghai", 8*3600)
	return time.Date(y, mo, d, h, mi, 0, 0, beijing).UTC()
}

// TestComputeLocalRemaining 余额校准纯函数（账本级，不做模型级裁决）。
func TestComputeLocalRemaining(t *testing.T) {
	now := beijingTime(2026, 8, 23, 10, 0) // 北京时间 2026-08-23 10:00（UTC 02:00）
	cases := []struct {
		name       string
		oldInit    int64
		oldLocal   int64
		oldSynced  time.Time
		avail      int64
		want       int64
	}{
		{"首次写入=avail", 0, 0, beijingTime(2026, 8, 23, 0, 0), 500000, 500000},
		{"今天扣完+avail有值→保持0", 500000, 0, beijingTime(2026, 8, 23, 9, 30), 500000, 0},
		{"昨天扣完+avail有值→记avail", 500000, 0, beijingTime(2026, 8, 22, 22, 0), 30000, 30000},
		{"昨天扣完+avail=0→保持0", 500000, 0, beijingTime(2026, 8, 22, 22, 0), 0, 0},
		{"下降校准（billing权威）", 500000, 400000, beijingTime(2026, 8, 23, 9, 0), 380000, 380000},
		{"上升保持（滞后回升防回弹）", 500000, 400000, beijingTime(2026, 8, 23, 9, 0), 500000, 400000},
	}
	for _, c := range cases {
		got := computeLocalRemaining(c.oldInit, c.oldLocal, c.oldSynced, c.avail, now)
		if got != c.want {
			t.Errorf("%s: computeLocalRemaining = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestComputeLocalRemainingBeijingBoundary 北京时间跨天边界：23:59 今天 / 00:01 明天。
func TestComputeLocalRemainingBeijingBoundary(t *testing.T) {
	// 场景：本地余额 0，syncedAt 北京时间 8-22 23:59（UTC 8-22 15:59）。
	// now = 北京时间 8-23 00:01（UTC 8-22 16:01）——按北京时间看已跨天，应恢复。
	now := beijingTime(2026, 8, 23, 0, 1)
	oldSynced := beijingTime(2026, 8, 22, 23, 59)
	if got := computeLocalRemaining(500000, 0, oldSynced, 500000, now); got != 500000 {
		t.Errorf("23:59扣光+次日00:01刷新 应恢复, got %d", got)
	}
	// 反向：now = 8-22 23:59（北京时间），syncedAt = 8-22 23:30 同一北京日 → 不恢复。
	now2 := beijingTime(2026, 8, 22, 23, 59)
	if got := computeLocalRemaining(500000, 0, beijingTime(2026, 8, 22, 23, 30), 500000, now2); got != 0 {
		t.Errorf("同一北京日 不应恢复, got %d", got)
	}
}

// TestUntilNextDayRecovery 冷却兜底时间：次日 12:00（北京时间）。
func TestUntilNextDayRecovery(t *testing.T) {
	got := untilNextDayRecovery()
	now := time.Now()
	if !got.After(now) {
		t.Errorf("untilNextDayRecovery() = %v 应晚于当前时间 %v", got, now)
	}
	beijing := got.In(beijingTZ)
	if beijing.Hour() != 12 || beijing.Minute() != 0 || beijing.Second() != 0 {
		t.Errorf("untilNextDayRecovery() 北京时间为 %v, 应为次日 12:00", beijing)
	}
	d := got.Sub(now)
	if d <= 0 || d > 48*time.Hour {
		t.Errorf("untilNextDayRecovery() 距现在 %v，超出合理范围 (0,48h]", d)
	}
}

// fakeBillingClient 测试用 billingFetcher：返回固定包或阻塞，不触发真实网络。
type fakeBillingClient struct {
	pkgs  []rawPackage
	err   error
	delay time.Duration
}

func (f *fakeBillingClient) FetchPackages(ctx context.Context, accessKey, secretKey string) ([]rawPackage, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.pkgs, f.err
}

// TestSyncModelStatesByAggregate 聚合三态：用完→禁用（冷却）、有余额→不动、恢复→解除。
func TestSyncModelStatesByAggregate(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// 初始 doubao-pro-32k 聚合 200 万 > 45 万 → 不禁用。
	if got := svc.syncModelStatesByAggregate(context.Background(), aid); len(got) != 0 {
		t.Errorf("初始聚合 200 万不应禁用, got %v", got)
	}
	// 扣光 → 聚合 0 → 禁用（写冷却）。
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 2000000)
	got := svc.syncModelStatesByAggregate(context.Background(), aid)
	if len(got) != 1 || got[0] != "doubao-1-5-pro-32k-250115" {
		t.Errorf("聚合归零应禁用 doubao-1-5-pro-32k-250115, got %v", got)
	}
	var status, cls string
	if err := svc.db.QueryRow(`SELECT status, last_failure_class FROM model_states WHERE channel_id='ch1' AND model='doubao-1-5-pro-32k-250115'`).Scan(&status, &cls); err != nil {
		t.Fatal(err)
	}
	if status != "cooling" || cls != "free_quota_exhausted" {
		t.Errorf("禁用状态 = %s/%s, want cooling/free_quota_exhausted", status, cls)
	}
}

// TestSyncModelStatesByAggregateMultiModel 多 quota model 映射同一 API 模型：合并 SUM 判定。
func TestSyncModelStatesByAggregateMultiModel(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// deepseek-v4-flash（45 万）+ deepseek-v4-flash-0731（200 万）都映射到 deepseek-v4-flash-ga-260731。
	insertPackage(t, svc, aid, "inst-f1", "deepseek-v4-flash", "Effective", 450000)
	insertPackage(t, svc, aid, "inst-f2", "deepseek-v4-flash-0731", "Effective", 2000000)
	// 合并聚合 245 万 > 45 万 → 不禁用。
	if got := svc.syncModelStatesByAggregate(context.Background(), aid); len(got) != 0 {
		t.Errorf("合并 245 万不应禁用, got %v", got)
	}
	// 扣 200 万（flash-0731 组归零）→ 聚合 45 万 = 中间态（0 < 45万 <= 45万）→ 不动不禁用。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 2000000)
	if got := svc.syncModelStatesByAggregate(context.Background(), aid); len(got) != 0 {
		t.Errorf("聚合 45 万中间态不应禁用, got %v", got)
	}
	// 再扣 45 万 → 聚合 0 → 禁用。
	svc.decrementLocalRemaining(aid, "deepseek-v4-flash-ga-260731", 450000)
	got := svc.syncModelStatesByAggregate(context.Background(), aid)
	if len(got) != 1 || got[0] != "deepseek-v4-flash-ga-260731" {
		t.Errorf("聚合归零应禁用 deepseek-v4-flash-ga-260731, got %v", got)
	}
}

// TestSyncModelStatesByAggregateRevive 恢复判定：free_quota_exhausted 冷却 + 聚合 >45 万
// → 解除；upstream_error（真实故障）不被解除（限定冷却来源，防与 model-health 振荡）；
// manual_enabled=0（用户手动禁用）不被解除。
func TestSyncModelStatesByAggregateRevive(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	// --- 场景 1：free_quota_exhausted 冷却 + 额度恢复（>45 万）→ 解除 ---
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 2000000) // 扣光 doubao-pro-32k
	svc.syncModelStatesByAggregate(context.Background(), aid)
	// 恢复：直接更新包余额为 500 万（模拟跨天恢复后 refreshOne 写入）。
	if _, err := svc.db.Exec(`UPDATE volc_quota_packages SET local_remaining=5000000, initial_total=5000000 WHERE account_id=? AND instance_no='inst-1'`, aid); err != nil {
		t.Fatal(err)
	}
	svc.syncModelStatesByAggregate(context.Background(), aid)
	var status, cls string
	var failCount int64
	if err := svc.db.QueryRow(`SELECT status, last_failure_class, fail_count FROM model_states WHERE channel_id='ch1' AND model='doubao-1-5-pro-32k-250115'`).
		Scan(&status, &cls, &failCount); err != nil {
		t.Fatal(err)
	}
	if status != "available" || cls != "" || failCount != 0 {
		t.Errorf("free_quota_exhausted 冷却应解除: status=%s cls=%q fail_count=%d, want available/''/0", status, cls, failCount)
	}
	// --- 场景 2：upstream_error（真实故障）不被免费额度 re-enable 解除（限定冷却来源，防振荡） ---
	insertPackage(t, svc, aid, "inst-r1", "deepseek-v4-flash-0731", "Effective", 300000)
	insertPackage(t, svc, aid, "inst-r2", "deepseek-v4-flash-0731", "Effective", 200000)
	if _, err := svc.db.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, last_error, last_failure_class, fail_count, updated_at)
		VALUES ('ch1', 'deepseek-v4-flash-ga-260731', 1, 'cooling', '2026-08-24T12:00:00Z', 'upstream 5xx', 'upstream_error', 3, 'now')`); err != nil {
		t.Fatal(err)
	}
	svc.syncModelStatesByAggregate(context.Background(), aid)
	if err := svc.db.QueryRow(`SELECT status FROM model_states WHERE channel_id='ch1' AND model='deepseek-v4-flash-ga-260731'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cooling" {
		t.Errorf("upstream_error 冷却不应被免费额度 re-enable 解除, status=%s, want cooling", status)
	}
	// --- 场景 3：manual_enabled=0（用户手动禁用）不被解除 ---
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 5000000) // 再扣光
	svc.syncModelStatesByAggregate(context.Background(), aid)
	if _, err := svc.db.Exec(`UPDATE model_states SET manual_enabled=0 WHERE channel_id='ch1' AND model='doubao-1-5-pro-32k-250115'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE volc_quota_packages SET local_remaining=5000000 WHERE account_id=? AND instance_no='inst-1'`, aid); err != nil {
		t.Fatal(err)
	}
	svc.syncModelStatesByAggregate(context.Background(), aid)
	if err := svc.db.QueryRow(`SELECT status FROM model_states WHERE channel_id='ch1' AND model='doubao-1-5-pro-32k-250115'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cooling" {
		t.Errorf("manual_enabled=0 的冷却不应被解除, status=%s, want cooling", status)
	}
}

// TestRefreshOneSyncedAtPreserved（rev2 审计 B，P0 回归）：synced_at 只首次写入，
// 刷新保留、扣减更新——保证跨天判定（模型复活）不被 15min 定时刷新污染。
func TestRefreshOneSyncedAtPreserved(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	svc.client = &fakeBillingClient{pkgs: []rawPackage{
		{InstanceNo: "inst-new", Product: "ark_bd", ProductName: "豆包·Doubao-pro-32k",
			ConfigurationCode: "Doubao_Pro_32k_data_collaboration", ConfigurationName: "pack",
			TotalAmount: "500000", AvailableAmount: "500000", Unit: "Tokens", Status: "Effective"},
	}}
	cfg := Config{ChannelID: "ch1", AccountID: aid, AccessKey: "AKxxx", Enabled: true, ForceBlock: true}
	// 第一次刷新：首次写入。
	if _, err := svc.refreshOne(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var synced1 string
	if err := svc.db.QueryRow(`SELECT synced_at FROM volc_quota_packages WHERE instance_no='inst-new'`).Scan(&synced1); err != nil {
		t.Fatal(err)
	}
	// 第二次刷新（等 1.1s 保证时间戳不同）：synced_at 必须保留首次值。
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.refreshOne(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var synced2 string
	if err := svc.db.QueryRow(`SELECT synced_at FROM volc_quota_packages WHERE instance_no='inst-new'`).Scan(&synced2); err != nil {
		t.Fatal(err)
	}
	if synced1 != synced2 {
		t.Errorf("synced_at 被刷新覆盖（P0）: 首次=%q 刷新后=%q, 应保留", synced1, synced2)
	}
	// 扣减 → synced_at 更新为扣减时间。inst-1 余额 200 万（newTestService 预置）先被扣，
	// 扣 2000100 才轮到 inst-new（50 万）被扣 100。
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 2000100)
	var synced3 string
	if err := svc.db.QueryRow(`SELECT synced_at FROM volc_quota_packages WHERE instance_no='inst-new'`).Scan(&synced3); err != nil {
		t.Fatal(err)
	}
	if synced3 == synced2 {
		t.Error("扣减后 synced_at 应更新")
	}
	// 扣减后再刷新：synced_at 保留扣减时间。
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.refreshOne(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var synced4 string
	if err := svc.db.QueryRow(`SELECT synced_at FROM volc_quota_packages WHERE instance_no='inst-new'`).Scan(&synced4); err != nil {
		t.Fatal(err)
	}
	if synced3 != synced4 {
		t.Errorf("扣减后刷新覆盖了 synced_at: 扣减=%q 刷新后=%q, 应保留扣减时间", synced3, synced4)
	}
}

// TestDecrementSkipsExpired 过期包不参与扣减（状态过滤，rev2 审计 C）。
func TestDecrementSkipsExpired(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	insertPackage(t, svc, aid, "inst-exp", "doubao-pro-32k", "Expired", 500000)
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 100000)
	var remaining int64
	if err := svc.db.QueryRow(`SELECT local_remaining FROM volc_quota_packages WHERE instance_no='inst-exp'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 500000 {
		t.Errorf("Expired 包不应被扣减, remaining=%d, want 500000", remaining)
	}
}

// TestRefreshInFlight 并发 Refresh：第二个返回"刷新进行中"（in-flight 锁）。
func TestRefreshInFlight(t *testing.T) {
	svc := newTestService(t)
	svc.client = &fakeBillingClient{delay: 500 * time.Millisecond}
	var wg sync.WaitGroup
	var secondErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = svc.Refresh(context.Background(), "")
	}()
	time.Sleep(100 * time.Millisecond) // 确保第一个已进入并占锁
	go func() {
		defer wg.Done()
		_, secondErr = svc.Refresh(context.Background(), "")
	}()
	wg.Wait()
	if secondErr == nil || !strings.Contains(secondErr.Error(), "刷新进行中") {
		t.Errorf("并发第二个 Refresh 应返回「刷新进行中」, got %v", secondErr)
	}
}

// TestSyncModelStatesByAggregateReviveThreshold 45 万恢复门槛边界：严格大于才恢复。
func TestSyncModelStatesByAggregateReviveThreshold(t *testing.T) {
	svc := newTestService(t)
	aid := accountID("AKxxx")
	svc.decrementLocalRemaining(aid, "doubao-1-5-pro-32k-250115", 2000000)
	svc.syncModelStatesByAggregate(context.Background(), aid)
	check := func(bal int64, want string) {
		t.Helper()
		if _, err := svc.db.Exec(`UPDATE volc_quota_packages SET local_remaining=?, initial_total=? WHERE account_id=? AND instance_no='inst-1'`, bal, bal, aid); err != nil {
			t.Fatal(err)
		}
		svc.syncModelStatesByAggregate(context.Background(), aid)
		var status string
		var disabledUntil *string
		if err := svc.db.QueryRow(`SELECT status, disabled_until FROM model_states WHERE channel_id='ch1' AND model='doubao-1-5-pro-32k-250115'`).Scan(&status, &disabledUntil); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Errorf("余额 %d → status=%s, want %s", bal, status, want)
		}
		if want == "available" && disabledUntil != nil {
			t.Errorf("恢复后 disabled_until 应为 NULL, got %v", *disabledUntil)
		}
	}
	check(449999, "cooling")  // < 45 万：中间态不动（保持冷却）
	check(450000, "cooling")  // = 45 万：中间态不动（严格大于才恢复）
	check(450001, "available") // > 45 万：恢复
}

// TestDisableModelForFreeQuota 重复禁用 fail_count 不增长、manual_enabled 不被覆盖。
func TestDisableModelForFreeQuota(t *testing.T) {
	svc := newTestService(t)
	until := untilNextDayRecovery()
	for i := 0; i < 2; i++ {
		if err := svc.disableModelForFreeQuota(context.Background(), "ch1", "m1", until); err != nil {
			t.Fatal(err)
		}
	}
	var failCount, manualEnabled int64
	var status string
	if err := svc.db.QueryRow(`SELECT fail_count, manual_enabled, status FROM model_states WHERE channel_id='ch1' AND model='m1'`).
		Scan(&failCount, &manualEnabled, &status); err != nil {
		t.Fatal(err)
	}
	if failCount != 0 {
		t.Errorf("重复禁用 fail_count 应保持 0, got %d", failCount)
	}
	if manualEnabled != 1 || status != "cooling" {
		t.Errorf("manual_enabled=%d status=%s, want 1/cooling", manualEnabled, status)
	}
}
