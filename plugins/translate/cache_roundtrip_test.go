package translate

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSaveGetCachedRoundTrip 回归测试：save 后必须能被 getCached 查到。
// 曾因 translations.hash 无 UNIQUE 约束导致 save() 的 ON CONFLICT(hash) 抛错被静默忽略，
// 译文始终未落库，lookup 全部返回 null。
func TestSaveGetCachedRoundTrip(t *testing.T) {
	dbsql, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer dbsql.Close()
	if err := migrate(dbsql); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s := &Service{db: dbsql}
	now := time.Now()
	tr := Translation{
		Hash:           hashText("Add review comment to the pending review."),
		SourceText:     "Add review comment to the pending review.",
		TranslatedText: "向待处理评审添加评审意见。",
		SourceType:     SourceMCP,
		SourceID:       "github/add_comment",
		TargetLang:     "zh-CN",
		Model:          "hy3",
		Type:           TypeTranslate,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	// 1) 落库必须成功（ON CONFLICT(hash) 在 UNIQUE 索引存在时生效）
	if err := s.save(t.Context(), tr); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	// 2) 二次保存（更新路径）也不报错
	tr.TranslatedText = "向待处理评审添加评审意见（修订）"
	if err := s.save(t.Context(), tr); err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	// 3) 能查到译文
	got, ok, err := s.getCached(t.Context(), tr.Hash, "zh-CN", TypeTranslate)
	if err != nil {
		t.Fatalf("getCached error: %v", err)
	}
	if !ok {
		t.Fatal("expected cached translation found, got none")
	}
	if got != "向待处理评审添加评审意见（修订）" {
		t.Fatalf("unexpected cached text: %q", got)
	}
}
