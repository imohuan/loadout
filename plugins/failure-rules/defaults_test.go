package failurerules

import (
	"context"
	"testing"
)

// TestCreateDraftDeduplicates 回归：同一条 AI 判定反复触发（并发失败 / 缓存过期）时，
// 不能把内容雷同的草稿堆成一串——判据是「模型 + 匹配条件 + 动作」完全相同则复用。
func TestCreateDraftDeduplicates(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(database)
	ctx := context.Background()
	in := RuleInput{
		Name: "AI: 参数错误", Priority: 150, Model: "glm-5.3-flash",
		Match:  Match{All: []Condition{{Field: "status_code", Op: "eq", Value: 400}, {Field: "body_code", Op: "eq", Value: "11133"}}},
		Action: Action{Verdict: VerdictIgnore},
	}
	first, err := s.CreateDraft(ctx, in, "glm-5.3-flash", `{"verdict":"ignore"}`)
	if err != nil {
		t.Fatalf("first draft: %v", err)
	}
	second, err := s.CreateDraft(ctx, in, "glm-5.3-flash", `{"verdict":"ignore"}`)
	if err != nil {
		t.Fatalf("second draft: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("同样内容应复用同一条草稿，实际 %s vs %s", first.ID, second.ID)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM failure_rules WHERE source='ai'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("草稿应只有 1 条，实际 %d", n)
	}
}

// TestRestoreDefaultsReinsertsAndKeepsUserRules 回归「恢复默认规则」：
// 用户删掉的默认规则要重新插回，用户自建规则不受影响。
func TestRestoreDefaultsReinsertsAndKeepsUserRules(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(database)
	ctx := context.Background()

	// 用户自建规则（source=manual，非 seed id）。
	if _, err := s.Create(ctx, RuleInput{
		Name: "用户自建", Priority: 200,
		Match:  Match{Any: []Condition{{Field: "status_code", Op: "eq", Value: 599}}},
		Action: Action{Verdict: VerdictIgnore},
	}); err != nil {
		t.Fatal(err)
	}
	// 模拟用户删除一条默认规则 + 改坏另一条的优先级。
	if err := s.Delete(ctx, "seed-007"); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.Get(ctx, "seed-007")
	if err == nil {
		t.Fatalf("seed-007 应已被删除，却仍存在: %+v", deleted)
	}

	if _, err := s.RestoreDefaults(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// 默认规则回来了。
	if _, err := s.Get(ctx, "seed-007"); err != nil {
		t.Fatalf("seed-007 应被重新插回: %v", err)
	}
	// 默认规则总数正确。
	var seeds int
	if err := database.QueryRow(`SELECT COUNT(*) FROM failure_rules WHERE id LIKE 'seed-%'`).Scan(&seeds); err != nil {
		t.Fatal(err)
	}
	if seeds != len(DefaultRuleIDs) {
		t.Fatalf("默认规则应 %d 条，实际 %d", len(DefaultRuleIDs), seeds)
	}
	// 用户规则没被动。
	var userRules int
	if err := database.QueryRow(`SELECT COUNT(*) FROM failure_rules WHERE id LIKE 'rule-%'`).Scan(&userRules); err != nil {
		t.Fatal(err)
	}
	if userRules != 1 {
		t.Fatalf("用户自建规则应保留 1 条，实际 %d", userRules)
	}
}
