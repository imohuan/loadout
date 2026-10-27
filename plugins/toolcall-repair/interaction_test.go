package toolcallrepair

import (
	"bytes"
	"testing"

	"loadout/plugins/types"
)

// 与其它改写 body 的能力共存：未命中路由回退时，不能把别的插件本轮的改写冲掉。
//
// 场景：force_stream 先把 body 改成 stream:true，随后本插件未命中（或 native）。
// 旧实现会用「本次首次看到的 body」无条件覆盖，导致 force_stream 的改写被回滚，
// 非流式请求打到只支持流式的上游 → 400。
func TestHandle_DoesNotClobberOtherPluginRewrite(t *testing.T) {
	svc, _ := newPlugin(t, nil) // 未配置 toolcall_repair
	body := []byte(`{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	pipe := pipeWith(body)
	// 模拟 force_stream 在本轮已把 stream 改成 true。
	rewritten := bytes.Replace(body, []byte(`"stream":false`), []byte(`"stream":true`), 1)
	pipe.Request.Body = rewritten

	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pipe.Request.Body, []byte(`"stream":true`)) {
		t.Fatalf("未命中路由时不应回滚其它插件的改写，实际: %s", pipe.Request.Body)
	}
}

// 命中后切到未配置渠道：必须撤销自己的改写，回到接手时的原文。
func TestHandle_FailoverUndoesOwnRewrite(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
		ChannelIDs: []string{"wb"},
	}})
	original := dirtyBody()
	pipe := pipeWith(original)
	pipe.Metadata["__current_channel"] = "wb"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pipe.Request.Body, original) {
		t.Fatal("命中渠道应改写")
	}
	pipe.Metadata["__current_channel"] = "other"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pipe.Request.Body, original) {
		t.Fatalf("切到未配置渠道应撤销本插件改写，实际: %s", pipe.Request.Body)
	}
}

// 命中后切到另一个也配置了的渠道：不能在上一次改写结果上叠加，只应处理原客户端的 body。
func TestHandle_FailoverRepairsFromOriginal(t *testing.T) {
	svc, _ := newPlugin(t, []types.CapabilityRoute{{
		Models: []string{"step-5-preview"}, Capability: capabilityName, Route: types.RouteProxy,
	}})
	original := dirtyBody()
	pipe := pipeWith(original)
	pipe.Metadata["__current_channel"] = "a"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), pipe.Request.Body...)
	pipe.Metadata["__current_channel"] = "b"
	if _, err := svc.HandleProxyBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pipe.Request.Body, first) {
		t.Fatalf("两个渠道都启用时应得到同一份补全结果，不叠加:\nfirst: %s\nsecond: %s", first, pipe.Request.Body)
	}
	// 保险：结果里不能出现重复补出来的声明（原有 1 条 + 合成 1 条 = 2 条）。
	if n := bytes.Count(pipe.Request.Body, []byte(`"name":"read_file"`)); n != 2 {
		t.Fatalf("不应重复补声明: %s", pipe.Request.Body)
	}
}
