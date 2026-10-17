package requestlog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loadout/plugins/types"
)

// TestKeepBase64RouteConfigPersistsToSnapshot 锁住「保留原始 base64」的路由级配置：
// 同一份带图片的请求，路由关闭时 body 里是 [image: …] 占位符，开启时原样保留 data URI，
// 而两种情况下 sk- 密钥都必须打码。
func TestKeepBase64RouteConfigPersistsToSnapshot(t *testing.T) {
	base64Body := "data:image/png;base64,iVBORw0KGgo="

	for _, tc := range []struct {
		name     string
		keep     bool
		wantData bool
	}{
		{name: "默认打码", keep: false, wantData: false},
		{name: "保留原图", keep: true, wantData: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
			if err != nil {
				t.Fatal(err)
			}
			defer reqDB.Close()
			svc := testService(t, []types.CapabilityRoute{{
				Models:               []string{"*"},
				Capability:           capabilityName,
				Route:                types.RouteProxy,
				RequestLogKeepBase64: tc.keep,
			}})
			svc.reqDB = reqDB

			pipe := testPipe("req-base64")
			if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
				t.Fatal(err)
			}
			uuid, _ := pipe.Metadata[metadataKey].(string)
			if uuid == "" {
				t.Fatal("metadata missing request log id")
			}

			d, err := svc.Detail(context.Background(), uuid)
			if err != nil {
				t.Fatal(err)
			}
			var snap requestSnapshot
			if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
				t.Fatal(err)
			}

			// 密钥任何口径下都不能落库原文。
			if strings.Contains(snap.Body, "sk-secret-xyz") {
				t.Fatalf("body still contains raw sk- secret: %s", snap.Body)
			}
			if !strings.Contains(snap.Body, "sk-***") {
				t.Fatalf("body missing sk-*** redaction: %s", snap.Body)
			}

			if tc.wantData {
				if !strings.Contains(snap.Body, base64Body) {
					t.Fatalf("keep_base64=true 时 base64 应原样保留，实际 body: %s", snap.Body)
				}
				if strings.Contains(snap.Body, "[image:") {
					t.Fatalf("keep_base64=true 时不应出现占位符: %s", snap.Body)
				}
			} else {
				if strings.Contains(snap.Body, base64Body) {
					t.Fatalf("keep_base64=false 时 base64 不应保留: %s", snap.Body)
				}
				if !strings.Contains(snap.Body, "[image: image/png, 8B]") {
					t.Fatalf("body missing image placeholder: %s", snap.Body)
				}
			}
		})
	}
}

// TestResponseKeepsBase64FromAttemptMetadata 锁住收尾事件复用同一口径：
// before-attempt 写入的 keepBase64Key 决定响应体是否打码（不能请求保留、响应打码）。
func TestResponseKeepsBase64FromAttemptMetadata(t *testing.T) {
	svc := testService(t, nil)
	pipe := testPipe("req-resp")
	pipe.Metadata[keepBase64Key] = true
	if !svc.redactOptions(pipe).KeepBase64 {
		t.Fatal("redactOptions 应读取 metadata 的 keepBase64Key")
	}

	pipe.Metadata[keepBase64Key] = false
	if svc.redactOptions(pipe).KeepBase64 {
		t.Fatal("keepBase64Key=false 时不应保留 base64")
	}

	// 未写 metadata（未命中路由）时默认打码。
	empty := testPipe("req-none")
	delete(empty.Metadata, keepBase64Key)
	if svc.redactOptions(empty).KeepBase64 {
		t.Fatal("metadata 缺失时应默认打码")
	}
}

// TestKeepBase64MatchesAggregateVirtualModel 回归：聚合模型（虚拟名 debug）在
// before-upstream 被改写成真实模型后，request_log 的路由匹配仍要认虚拟名。
// 否则「debug + 保留 base64」的专项规则永远命不中，只会落到下面的 * 兜底规则
// （不保留），表现为开关配了却不生效。
func TestKeepBase64MatchesAggregateVirtualModel(t *testing.T) {
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()

	svc := testService(t, []types.CapabilityRoute{
		{
			Models:               []string{"debug"},
			Capability:           capabilityName,
			Route:                types.RouteProxy,
			RequestLogKeepBase64: true,
		},
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	})
	svc.reqDB = reqDB

	pipe := testPipe("req-virtual-keep")
	pipe.Request.Model = "deepseek-v4.1-flash" // 聚合已改写为真实模型
	pipe.Metadata["__virtual_model"] = "debug" // 虚拟名只留在 metadata
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)
	if uuid == "" {
		t.Fatal("metadata missing request log id")
	}

	d, err := svc.Detail(context.Background(), uuid)
	if err != nil {
		t.Fatal(err)
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.Body, "data:image/png;base64,") {
		t.Fatalf("虚拟模型命中的保留 base64 规则未生效，实际 body: %s", snap.Body)
	}
	if strings.Contains(snap.Body, "[image:") {
		t.Fatalf("保留 base64 时不应出现占位符: %s", snap.Body)
	}
}
