package unifyai

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEnrichAgainstRealRegistry 用真实的 Loadout 渠道模型名验证匹配覆盖率。
// 渠道库不存在时跳过。
func TestEnrichAgainstRealRegistry(t *testing.T) {
	db := os.ExpandEnv("$USERPROFILE/.loadout/loadout.db")
	raw, err := os.ReadFile(db)
	if err != nil {
		t.Skip("本机无 loadout.db，跳过")
	}
	if _, err := os.Stat(metadataCachePath()); err != nil {
		t.Skip("本机无 OpenRouter 元数据缓存，跳过")
	}
	byID, bySlug := openRouterContextIndex()

	// 从渠道库里捞出形如 "Loadout/<模型名>" 的候选模型名（v2 前缀命名）。
	re := regexp.MustCompile(`Loadout/([A-Za-z0-9\.\-_:]{2,64})`)
	seen := map[string]bool{}
	var names []string
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		n := m[1]
		if seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
		if len(names) >= 40 {
			break
		}
	}
	if len(names) == 0 {
		t.Skip("渠道库里没有 Loadout/ 前缀模型名，跳过")
	}
	hit, miss := 0, []string{}
	for _, n := range names {
		if _, ok := lookupOpenRouterContext(byID, bySlug, n); ok {
			hit++
		} else {
			miss = append(miss, n)
		}
	}
	t.Logf("OpenRouter 命中 %d/%d；未命中：%s", hit, len(names), strings.Join(miss, ", "))
}
