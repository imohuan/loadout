package visionv2

import (
	"log/slog"
	"net/http"
	"testing"

	modelgateway "loadout/plugins/model-gateway"
)

// TestSubRequestGuardNoRecursion 防递归：三个 hook（BeforeAttempt/StreamChunk/AfterUpstream）
// 对带 __sub_request 标记的子请求（视觉识别/续流走 model-gateway 主链路时必然触发）
// 必须原样返回、不执行任何视觉改写/工具解析——否则视觉模型自身配了 vision 路由会死循环。
func TestSubRequestGuardNoRecursion(t *testing.T) {
	svc := NewService(nil, nil, slog.Default()) // 不注入 gateway：早退发生在其之前，走到网关即测试失败
	svc.cacheDir = t.TempDir()
	body := `{"model":"hy3","messages":[{"role":"user","content":[{"type":"text","text":"hi"},` +
		`{"type":"image_url","image_url":{"url":"` + tinyPNGDataURI + `"}}]}]}`

	pipe := proxyPipe("chat/completions", "hy3", body)
	pipe.Metadata["__sub_request"] = true

	// 1. 请求侧：BeforeUpstream（实际注册名 ProxyBeforeAttempt）
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatalf("HandleProxyBeforeUpstream 对子请求报错: %v", err)
	}
	if got, ok := out.(*modelgateway.ProxyPipeline); !ok || got != pipe {
		t.Fatalf("BeforeUpstream 应原样返回 pipe，实际 %T", out)
	}
	if string(pipe.Request.Body) != body {
		t.Fatalf("子请求 body 被改写: %s", pipe.Request.Body)
	}

	// 2. 流式：StreamChunk
	sp := &modelgateway.StreamChunkPayload{Pipe: pipe, Data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n")}
	out2, err := svc.HandleProxyStreamChunk(sp)
	if err != nil {
		t.Fatalf("HandleProxyStreamChunk 对子请求报错: %v", err)
	}
	if got, ok := out2.(*modelgateway.StreamChunkPayload); !ok || got != sp {
		t.Fatalf("StreamChunk 应原样返回 sp，实际 %T", out2)
	}

	// 3. 非流式：AfterUpstream
	ap := &modelgateway.AfterUpstreamPayload{Pipe: pipe, Response: &modelgateway.ProxyResponse{StatusCode: http.StatusOK}}
	out3, err := svc.HandleProxyAfterUpstream(ap)
	if err != nil {
		t.Fatalf("HandleProxyAfterUpstream 对子请求报错: %v", err)
	}
	if got, ok := out3.(*modelgateway.AfterUpstreamPayload); !ok || got != ap {
		t.Fatalf("AfterUpstream 应原样返回 ap，实际 %T", out3)
	}
}
