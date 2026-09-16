package unifyai

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCatalogModels 读 OpenRouter 元数据缓存，返回「加载配置」下拉需要的模型清单：
// 每个条目带上下文、最大输出、视觉、推理四项，供前端一键回填虚拟模型的模型配置。
func TestCatalogModels(t *testing.T) {
	dir := t.TempDir()
	content := `[
	  {"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","context":1048576,"output":384000,"vision":true,"reasoning":true},
	  {"id":"openai/gpt-4o","name":"GPT-4o","context":128000,"output":16384,"vision":true,"reasoning":false},
	  {"id":"text-only/x","name":"Text Only","context":32000,"output":4096,"vision":false,"reasoning":false}
	]`
	path := filepath.Join(dir, "openrouter-models.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := metadataCachePath
	metadataCachePath = func() string { return path }
	defer func() { metadataCachePath = old }()

	svc := NewService(nil)
	models := svc.CatalogModels()
	if len(models) != 3 {
		t.Fatalf("模型数 = %d, want 3: %+v", len(models), models)
	}
	first := models[0]
	if first.ID != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("id = %q", first.ID)
	}
	if first.Name != "DeepSeek V4.1 Flash" {
		t.Errorf("name = %q", first.Name)
	}
	if first.Context != 1048576 {
		t.Errorf("context = %d, want 1048576", first.Context)
	}
	if first.Output != 384000 {
		t.Errorf("output = %d, want 384000", first.Output)
	}
	if !first.Vision || !first.Reasoning {
		t.Errorf("能力标记错误: vision=%v reasoning=%v", first.Vision, first.Reasoning)
	}
	// 第二、三条的能力为 false 也必须原样返回，前端靠它决定勾不勾。
	if models[1].Reasoning {
		t.Errorf("gpt-4o reasoning 应为 false")
	}
	if models[2].Vision {
		t.Errorf("text-only vision 应为 false")
	}
}

// TestCatalogModelsMissingCache 缓存缺失时返回空列表且不报错（页面可用，提示先刷新元数据）。
func TestCatalogModelsMissingCache(t *testing.T) {
	old := metadataCachePath
	metadataCachePath = func() string { return filepath.Join(t.TempDir(), "missing.json") }
	defer func() { metadataCachePath = old }()

	svc := NewService(nil)
	if got := svc.CatalogModels(); len(got) != 0 {
		t.Fatalf("缓存缺失应返回空列表, got %+v", got)
	}
}

// TestCatalogModelsSkipsJunkEntries 缓存里的空 id 条目要跳过（前端下拉不该出现空行）。
func TestCatalogModelsSkipsJunkEntries(t *testing.T) {
	dir := t.TempDir()
	content := `[
	  {"id":"","name":"Empty","context":1000},
	  {"id":"ok/model","name":"Fine","context":2000,"output":100,"vision":false,"reasoning":false}
	]`
	path := filepath.Join(dir, "openrouter-models.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := metadataCachePath
	metadataCachePath = func() string { return path }
	defer func() { metadataCachePath = old }()

	svc := NewService(nil)
	models := svc.CatalogModels()
	if len(models) != 1 || models[0].ID != "ok/model" {
		t.Fatalf("应只保留有效条目, got %+v", models)
	}
}
