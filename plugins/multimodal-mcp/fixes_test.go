package multimodalmcp

import (
	"context"
	"strings"
	"testing"

	"loadout/core/store"
)

// newConfigService 构造带真实 store（临时目录）的 Service，并保存给定配置。
func newConfigService(t *testing.T, cfg *MultimodalConfig) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := st.Write(FileMultimodalConfig, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return &Service{st: st, gw: &fakeRecogForwarder{}}
}

// TestCheckEndpointEnabledDisabled 验证：端点总开关关闭时，工具调用被拒绝。
func TestCheckEndpointEnabledDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	cfg.Tools[0].Model = "doubao-seed-2-1-pro-260628" // 图片工具配模型
	s := newConfigService(t, cfg)

	_, err := s.understandImage(context.Background(), map[string]any{"image": "https://example.com/a.png"})
	if err == nil {
		t.Fatal("端点未启用时应报错")
	}
	if !strings.Contains(err.Error(), "未启用") {
		t.Errorf("错误应提及未启用，got: %v", err)
	}
}

// TestCheckEndpointEnabledActive 验证：端点总开关开启且工具已配置模型时，请求正常走网关。
func TestCheckEndpointEnabledActive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Tools[0].Enabled = true
	cfg.Tools[0].Model = "doubao-seed-2-1-pro-260628"
	s := newConfigService(t, cfg)
	fw := s.gw.(*fakeRecogForwarder)
	fw.respBody = []byte(`{"choices":[{"message":{"content":"识别结果"}}]}`)

	_, err := s.understandImage(context.Background(), map[string]any{"image": "https://example.com/a.png"})
	if err != nil {
		t.Fatalf("端点启用且图片走 URL 时不应报错，got: %v", err)
	}
	if fw.gotPath != "chat/completions" {
		t.Errorf("path = %q, want chat/completions", fw.gotPath)
	}
}

// TestCheckEndpointEnabledToolDisabled 验证：端点开启但工具被禁用/未配模型时，工具调用报错。
func TestCheckEndpointEnabledToolDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	// 图片工具保持 enabled 但 Model 为空 → modelFor 报错。
	s := newConfigService(t, cfg)

	_, err := s.understandImage(context.Background(), map[string]any{"image": "https://example.com/a.png"})
	if err == nil {
		t.Fatal("工具未配置模型时应报错")
	}
	if !strings.Contains(err.Error(), "未配置可用模型") {
		t.Errorf("错误应提及未配置模型，got: %v", err)
	}
}
