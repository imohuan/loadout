package vision

import (
	"testing"

	"loadout/core/config"
)

// TestResolveHistoryImagesCacheHit 缓存命中返回描述文本（只读，不触发视觉模型）。
func TestResolveHistoryImagesCacheHit(t *testing.T) {
	svc, _ := newTestService(t)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	key := md5Hex("http://img/a.png|qwen-vl-max|" + visionCacheVersion)
	if err := svc.writeCache(key, "图中是一只猫"); err != nil {
		t.Fatalf("预写缓存失败: %v", err)
	}

	got := svc.resolveHistoryImages([]string{"http://img/a.png"}, "qwen-vl-max")
	if got != "图中是一只猫" {
		t.Fatalf("缓存命中应返回描述，实际 %q", got)
	}
}

// TestResolveHistoryImagesCacheMiss 缓存 miss 返回占位符，绝不调视觉模型。
func TestResolveHistoryImagesCacheMiss(t *testing.T) {
	svc, _ := newTestService(t)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	got := svc.resolveHistoryImages([]string{"http://img/unknown.png"}, "qwen-vl-max")
	if got != historyPlaceholder {
		t.Fatalf("缓存 miss 应返回占位符 %q，实际 %q", historyPlaceholder, got)
	}
}

// TestResolveHistoryImagesPlaceholderMode placeholder 模式不读缓存，一律占位符。
func TestResolveHistoryImagesPlaceholderMode(t *testing.T) {
	svc, _ := newTestService(t)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()
	oldMode := config.VisionHistoryMode
	config.VisionHistoryMode = "placeholder"
	defer func() { config.VisionHistoryMode = oldMode }()

	key := md5Hex("http://img/a.png|qwen-vl-max|" + visionCacheVersion)
	if err := svc.writeCache(key, "有缓存也用占位符"); err != nil {
		t.Fatalf("预写缓存失败: %v", err)
	}

	got := svc.resolveHistoryImages([]string{"http://img/a.png"}, "qwen-vl-max")
	if got != historyPlaceholder {
		t.Fatalf("placeholder 模式应一律占位符，实际 %q", got)
	}
}

// TestResolveHistoryImagesMulti 多图一组一个缓存 key：命中返回组合描述，miss 一个占位符。
func TestResolveHistoryImagesMulti(t *testing.T) {
	svc, _ := newTestService(t)
	oldEnabled := config.VisionCacheEnabled
	config.VisionCacheEnabled = true
	defer func() { config.VisionCacheEnabled = oldEnabled }()

	imgs := []string{"http://img/a.png", "http://img/b.png"}
	key := md5Hex("http://img/a.png\nhttp://img/b.png|qwen-vl-max|" + visionCacheVersion)
	if err := svc.writeCache(key, "两张图综合描述"); err != nil {
		t.Fatalf("预写缓存失败: %v", err)
	}

	got := svc.resolveHistoryImages(imgs, "qwen-vl-max")
	if got != "两张图综合描述" {
		t.Fatalf("多图组缓存命中应返回组合描述，实际 %q", got)
	}

	miss := svc.resolveHistoryImages([]string{"http://img/a.png", "http://img/c.png"}, "qwen-vl-max")
	if miss != historyPlaceholder {
		t.Fatalf("多图组 miss 应返回占位符，实际 %q", miss)
	}
}
