package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"loadout/plugins/types"
)

// TestProxyForwardResolveErrorNotDisguised 回归：候选渠道解析失败（DB 查询出错等
// 内部错误）必须把真实原因透出（channel_resolve_error / 500），绝不能伪装成
// "没有可用渠道支持模型"——旧实现吞掉 resolveChannels 的 error 返回空列表，
// SQLite 锁死之类的基础设施故障被报成了配置问题，用户完全看不到真实原因。
func TestProxyForwardResolveErrorNotDisguised(t *testing.T) {
	svc, st := newTestService(t)
	// 直接把渠道文件写成非法 JSON：st.Read 反序列化失败 → resolveChannels
	// 返回 error（模拟 DB/数据文件读不出来的内部错误）。
	// store.New 返回的 Service 未暴露目录，用 Write 合法数据后再覆写磁盘文件。
	if err := st.Write(types.FileChannels, []types.Channel{}); err != nil {
		t.Fatalf("初始化渠道文件失败: %v", err)
	}
	// 找到 store 目录：写一个哨兵文件再读路径不可行，直接用 t.TempDir 约定——
	// store.New(t.TempDir()) 的目录就是 store 的根。这里借 types.FileChannels
	// 相对路径由 store 内部管理，必须直接改文件；从 st 无目录 getter 可用时用之。
	// store.Store 提供 Dir()（core/store 读取侧有 Dir()），取目录覆写。
	dir := st.Dir()
	if dir == "" {
		t.Fatal("store dir empty")
	}
	if err := writeFile(dir+"/"+types.FileChannels, []byte("{invalid json")); err != nil {
		t.Fatalf("写坏渠道文件失败: %v", err)
	}

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, 期望 500（内部错误不能伪装成 502 no_available_channel）", rr.Code)
	}
	resp := rr.Body.String()
	if strings.Contains(resp, "no_available_channel") {
		t.Fatalf("响应仍伪装成 no_available_channel: %s", resp)
	}
	if !strings.Contains(resp, "channel_resolve_error") {
		t.Fatalf("响应缺少 channel_resolve_error 真实错误类型: %s", resp)
	}
	if !strings.Contains(resp, "渠道解析失败") {
		t.Fatalf("响应缺少真实原因描述: %s", resp)
	}
}

// writeFile 简单写文件辅助（测试内联，避免额外依赖）。
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
