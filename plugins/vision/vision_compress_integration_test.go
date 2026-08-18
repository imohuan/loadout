package vision

import (
	"context"
	"strings"
	"testing"

	"loadout/core/config"
	modelgateway "loadout/plugins/model-gateway"
	fakellm "loadout/testkit/fake-llm"
)

// TestCallVisionCompressesDataURI 验证 callVision 把超限 data URI 压缩后再上送：
// fake-llm 记录的请求里 image_url 应是压缩后的短 URI，而不是原始大图。
func TestCallVisionCompressesDataURI(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)
	withCompressConfig(t, 1, 64, 90, 25*1024*1024) // 小阈值触发压缩

	raw := makeNoisePNG(128, 128) // > maxEdge 64，必被压缩
	orig := dataURI("image/png", raw)

	ch := modelgateway.ResolvedChannel{ID: "t", Name: "test", BaseURL: url + "/v1"}
	text, err := svc.callVision(context.Background(), []string{orig}, "qwen-vl-max", []modelgateway.ResolvedChannel{ch}, nil)
	if err != nil {
		t.Fatalf("callVision 出错: %v", err)
	}
	if text != "ok" {
		t.Fatalf("应返回 fake 默认响应 ok，实际 %q", text)
	}

	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("未收到任何视觉请求")
	}
	content, ok := reqs[0].Messages[0]["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("请求 content 结构异常: %v", reqs[0].Messages[0])
	}
	part, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] 类型异常: %T", content[0])
	}
	imgURL, ok := part["image_url"].(map[string]any)["url"].(string)
	if !ok {
		t.Fatalf("image_url.url 类型异常: %v", part)
	}
	if imgURL == orig {
		t.Fatal("上送的 image_url 未被压缩")
	}
	if len(imgURL) >= len(orig) {
		t.Fatalf("压缩后的 URI 应更短: %d >= %d", len(imgURL), len(orig))
	}
}

// TestCallVisionKeepsRemoteURL 验证远程 URL 透传：不走压缩，原样上送。
func TestCallVisionKeepsRemoteURL(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)

	remote := "https://example.com/big.png"
	ch := modelgateway.ResolvedChannel{ID: "t", Name: "test", BaseURL: url + "/v1"}
	if _, err := svc.callVision(context.Background(), []string{remote}, "qwen-vl-max", []modelgateway.ResolvedChannel{ch}, nil); err != nil {
		t.Fatalf("callVision 出错: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("未收到任何视觉请求")
	}
	content := reqs[0].Messages[0]["content"].([]any)
	part := content[0].(map[string]any)
	imgURL := part["image_url"].(map[string]any)["url"].(string)
	if imgURL != remote {
		t.Fatalf("远程 URL 应原样透传: %q != %q", imgURL, remote)
	}
}

// TestCallVisionUsesBuiltinPrompt 验证提示词 fallback：env 为空时用内置模板。
func TestCallVisionUsesBuiltinPrompt(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)
	withCompressConfig(t, 1, 64, 90, 25*1024*1024)

	ch := modelgateway.ResolvedChannel{ID: "t", Name: "test", BaseURL: url + "/v1"}
	if _, err := svc.callVision(context.Background(), []string{"data:image/png;base64,iVBORw0KGgo="}, "qwen-vl-max", []modelgateway.ResolvedChannel{ch}, nil); err != nil {
		t.Fatalf("callVision 出错: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) == 0 {
		t.Fatal("未收到任何视觉请求")
	}
	content := reqs[0].Messages[0]["content"].([]any)
	last := content[len(content)-1].(map[string]any)
	text, ok := last["text"].(string)
	if !ok {
		t.Fatalf("末尾 text 分段异常: %v", last)
	}
	if !strings.Contains(text, "【摘要】") || !strings.Contains(text, "【不确定】") {
		t.Fatalf("请求未携带内置结构化提示词，实际: %q", text)
	}
}

// TestCallVisionTooLargeFails 验证超限图片是硬性失败：callVision 直接报错，
// 而不是 warn 后原样上送（code review 阻断项 #5）。
func TestCallVisionTooLargeFails(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()

	svc, _ := newTestService(t)
	withCompressConfig(t, 1, 64, 90, 1000) // 上限 1000 字节

	raw := makeNoisePNG(64, 64) // > 1000 字节
	orig := dataURI("image/png", raw)

	ch := modelgateway.ResolvedChannel{ID: "t", Name: "test", BaseURL: url + "/v1"}
	_, err := svc.callVision(context.Background(), []string{orig}, "qwen-vl-max", []modelgateway.ResolvedChannel{ch}, nil)
	if err == nil {
		t.Fatal("超限图片应使 callVision 失败")
	}
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("超限图片不应上送上游，实际收到 %d 个请求", len(reqs))
	}
}

// TestCallVisionOverMaxImages 验证张数上限。
func TestCallVisionOverMaxImages(t *testing.T) {
	svc, _ := newTestService(t)
	old := config.VisionMaxImages
	config.VisionMaxImages = 2
	t.Cleanup(func() { config.VisionMaxImages = old })

	imgs := []string{
		"data:image/png;base64,aGVsbG8=",
		"data:image/png;base64,d29ybGQ=",
		"data:image/png;base64,Zm9v",
	}
	ch := modelgateway.ResolvedChannel{ID: "t", Name: "test"}
	if _, err := svc.callVision(context.Background(), imgs, "qwen-vl-max", []modelgateway.ResolvedChannel{ch}, nil); err == nil {
		t.Fatal("超过图片张数上限应报错")
	}
}
