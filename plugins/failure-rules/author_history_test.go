package failurerules

import (
	"context"
	"strings"
	"testing"
)

// TestAuthorPassesCumulativeHistoryToAI 锁定用户明确要求的一项能力：
// 「生成 ai 规则之后是否进行了回测？只有回测通过才结束……
//
//	否则将继续提交给 ai 进行对话（历史记录给他）生成下一次的规则」。
//
// 所以第 N 轮的提示词里必须带上**前面每一轮**尝试过什么、为什么没通过，
// 而不只是上一轮那一句原因。否则 AI 会在同一类错解上反复打转。
func TestAuthorPassesCumulativeHistoryToAI(t *testing.T) {
	database, err := openMemory(t)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	samples := NewSamplesStore(database)
	authors := NewAuthorStore(database)
	drafts := NewStore(database)

	sm, err := samples.CreateBuiltin(ctx, Sample{ID: "sm-hist", StatusCode: 429, Message: "rate limited"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := authors.Create(ctx, sm.ID, "stub-model", 4)
	if err != nil {
		t.Fatal(err)
	}

	var prompts []string
	// 前三轮各给一个「匹配不上 429」的错解（条件写 599），第四轮收敛。
	chatFn := func(p string) (string, error) {
		prompts = append(prompts, p)
		if len(prompts) < 4 {
			return `{"name":"错解A","match":{"all":[{"field":"status_code","op":"eq","value":599}]},"action":{"verdict":"ignore"},"reason":"x"}`, nil
		}
		return `{"name":"对了解","match":{"all":[{"field":"status_code","op":"eq","value":429}]},"action":{"verdict":"cooldown","recover":"fixed","cooldown_seconds":60},"reason":"限速"}`, nil
	}

	e := NewEngine(database, nil)
	out, err := e.authorRunWith(ctx, chatFn, drafts, authors, sess, sm)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "done" {
		t.Fatalf("第 4 轮应收敛，实际 %q（error=%s）", out.Status, out.Error)
	}
	if len(prompts) != 4 {
		t.Fatalf("应调用 4 轮，实际 %d", len(prompts))
	}

	// 第 4 轮的提示词必须能看到前 3 轮的历史（每一轮都要出现）。
	p4 := prompts[3]
	for _, want := range []string{"错解A", "第 1 轮：", "第 2 轮：", "第 3 轮："} {
		if !strings.Contains(p4, want) {
			t.Fatalf("第 4 轮提示词缺少历史片段 %q；实际提示词尾部：\n%s", want, p4)
		}
	}
	// 第 1 轮没有历史，不该凭空出现「历次尝试」段落。
	if strings.Contains(prompts[0], "历次尝试") {
		t.Fatalf("第 1 轮不该带历史段落：\n%s", prompts[0])
	}
}
