package adminapi

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loadout/core/auth"
	"loadout/core/config"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	"loadout/plugins/skills"
	"loadout/plugins/types"
	unifyai "loadout/plugins/unifyai"
	fakemcp "loadout/testkit/fake-mcp"
)

// newTestServer 用临时目录组装完整服务：store、admin-auth（首启建 admin 账号）、
// gateway-keys、skills，并启动 httptest.Server。返回服务器、store 与初始密码。
func newTestServer(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })

	authSvc := adminauth.NewService(st, slog.Default())
	if _, err := authSvc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取初始密码: %v", err)
	}

	keys := gatewaykeys.NewManager(st)
	skillSvc := skills.NewService(st, slog.Default(), t.TempDir(), t.TempDir())
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil, unifyai.NewService(slog.Default()))

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return ts, st, string(pw)
}

// apiReq 向测试服务器发请求；body 非 nil 时按 JSON 编码，cookie 非 nil 时附带。
func apiReq(t *testing.T, ts *httptest.Server, method, path string, body any, cookie *http.Cookie) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应: %v", err)
	}
	return resp, data
}

// login 用指定密码登录并返回会话 Cookie。
func login(t *testing.T, ts *httptest.Server, password string) *http.Cookie {
	t.Helper()
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": password}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录期望 200，实际 %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("登录响应缺少会话 Cookie")
	return nil
}

// TestLoginAndSession 验证登录成功写 Cookie、密码错误 401、无 Cookie 访问 session 端点 401。
func TestLoginAndSession(t *testing.T) {
	ts, _, pw := newTestServer(t)

	// 正确密码 → 200 且 Set-Cookie 存在。
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": pw}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正确密码登录期望 200，实际 %d", resp.StatusCode)
	}
	foundCookie := false
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			foundCookie = true
			if !c.HttpOnly || c.Path != "/" {
				t.Errorf("会话 Cookie 应为 HttpOnly 且 Path=/，实际 %+v", c)
			}
		}
	}
	if !foundCookie {
		t.Fatal("登录响应未设置会话 Cookie")
	}

	// 错误密码 → 401。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": "wrong-password"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密码登录期望 401，实际 %d", resp.StatusCode)
	}

	// 无 Cookie 访问 session 端点 → 401。
	resp, _ = apiReq(t, ts, http.MethodGet, "/api/channels", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 Cookie 访问 session 端点期望 401，实际 %d", resp.StatusCode)
	}
}

// TestSSOLogin 验证桌面端免登录链路：短效 JWT 换完整会话 Cookie，且新 Cookie 能访问受保护端点。
func TestSSOLogin(t *testing.T) {
	ts, st, _ := newTestServer(t)

	// 模拟桌面端：用同一 store 的 secret 签 30 秒短效 token。
	token, err := auth.SignToken(st.SecretKey(), "admin", 30*time.Second)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/sso/login", map[string]string{"token": token}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("有效 token 换会话期望 200，实际 %d", resp.StatusCode)
	}
	var ssoCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			ssoCookie = c
		}
	}
	if ssoCookie == nil {
		t.Fatal("SSO 登录响应未设置会话 Cookie")
	}

	// 换到的 Cookie 必须能访问 session 端点（/api/channels）。
	resp, _ = apiReq(t, ts, http.MethodGet, "/api/channels", nil, ssoCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSO 换到的 Cookie 访问受保护端点期望 200，实际 %d", resp.StatusCode)
	}

	// 无效 token → 401。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/sso/login", map[string]string{"token": "bad.token.value"}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无效 token 期望 401，实际 %d", resp.StatusCode)
	}

	// 缺失 token → 400。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/sso/login", map[string]string{}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 token 期望 400，实际 %d", resp.StatusCode)
	}
}

// TestModelTestProxy 验证模型测试代理：/api/test/models 与 /api/test/chat 都在后台转发上游，
// base_url 原样拼接（不自动补 /v1，地址完全由用户决定）。
func TestModelTestProxy(t *testing.T) {
	// 假上游：同时提供 /v1/models 与 /v1/chat/completions。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi from upstream"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 获取模型列表（base_url 带 /v1，按原样拼接 /models）。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/test/models",
		map[string]string{"base_url": upstream.URL + "/v1"}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/test/models 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var modelsRes struct {
		Models []string `json:"models"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(data, &modelsRes); err != nil {
		t.Fatalf("解析 models 响应: %v", err)
	}
	if modelsRes.Error != "" {
		t.Fatalf("models 不应返回错误: %s", modelsRes.Error)
	}
	if len(modelsRes.Models) != 2 || modelsRes.Models[0] != "gpt-4o" {
		t.Fatalf("models 结果不符: %+v", modelsRes.Models)
	}

	// 非流式 chat 代理。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/test/chat", map[string]any{
		"base_url": upstream.URL + "/v1",
		"model":    "gpt-4o",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/test/chat 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "hi from upstream") {
		t.Fatalf("chat 响应未透传上游内容: %s", data)
	}
	if resp.Header.Get("X-Request-Id") == "" {
		t.Fatal("chat 响应应携带 X-Request-Id")
	}
	// 测试请求不写转发日志：访问摘要经 X-Test-Log 头（base64 JSON）回带。
	header := resp.Header.Get("X-Test-Log")
	if header == "" {
		t.Fatal("chat 响应应携带 X-Test-Log 访问摘要")
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("X-Test-Log 不是合法 base64: %v", err)
	}
	var summary struct {
		RequestID      string `json:"request_id"`
		RequestedModel string `json:"requested_model"`
		Result         string `json:"result"`
		DurationMS     int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("X-Test-Log 不是合法 JSON: %v", err)
	}
	if summary.RequestID == "" || summary.RequestedModel != "gpt-4o" || summary.Result != "success" || summary.DurationMS < 0 {
		t.Fatalf("X-Test-Log 摘要字段不符: %+v", summary)
	}
}

// TestModelTestSuffixAndKeyPriority 验证测试代理：suffix_mode 决定上游路径
// （chat/gpt/claude → /chat/completions、/responses、/messages），选中渠道时
// 自定义 api_key 优先于渠道存储的 key（渠道 key 兜底）。
func TestModelTestSuffixAndKeyPriority(t *testing.T) {
	type hit struct{ path, auth string }
	var mu sync.Mutex
	var hits []hit
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, hit{path: r.URL.Path, auth: r.Header.Get("Authorization")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	ts, st, pw := newTestServer(t)
	cipher, err := st.Encrypt("channel-key")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := st.Write(types.FileChannels, []types.Channel{{
		ID: "ch-test", Name: "test", BaseURL: upstream.URL, APIKeyCipher: cipher,
	}}); err != nil {
		t.Fatalf("Write channels: %v", err)
	}
	cookie := login(t, ts, pw)

	send := func(extra map[string]any) {
		t.Helper()
		body := map[string]any{
			"channel_id": "ch-test",
			"model":      "m",
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		}
		for k, v := range extra {
			body[k] = v
		}
		resp, data := apiReq(t, ts, http.MethodPost, "/api/test/chat", body, cookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chat 期望 200，实际 %d: %s", resp.StatusCode, data)
		}
	}

	// 1) 仅渠道：渠道 key 兜底，默认 /chat/completions。
	send(nil)
	// 2) 自定义 key 优先于渠道 key。
	send(map[string]any{"api_key": "custom-key"})
	// 3) gpt 模式 → /responses。
	send(map[string]any{"suffix_mode": "gpt"})
	// 4) claude 模式 → /messages。
	send(map[string]any{"suffix_mode": "claude"})

	mu.Lock()
	defer mu.Unlock()
	// 渠道 base_url 不带 /v1，路径按原样拼接（不自动补 v1）。
	want := []hit{
		{path: "/chat/completions", auth: "Bearer channel-key"},
		{path: "/chat/completions", auth: "Bearer custom-key"},
		{path: "/responses", auth: "Bearer channel-key"},
		{path: "/messages", auth: "Bearer channel-key"},
	}
	if len(hits) != len(want) {
		t.Fatalf("上游请求数期望 %d，实际 %d: %+v", len(want), len(hits), hits)
	}
	for i, w := range want {
		if hits[i] != w {
			t.Errorf("第 %d 次请求期望 %+v，实际 %+v", i+1, w, hits[i])
		}
	}
}

// TestBuildTestPayloadBySuffix 验证 buildTestPayload 按后缀模式转换请求体：
// chat 原样透传；gpt（/responses）messages→input、text→input_text、image_url 拍平；
// claude（/messages）system 抽顶层、图片转 source、max_tokens 必填。
func TestBuildTestPayloadBySuffix(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "你是助手"},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "看图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
		}},
	}
	base := func(mode string) testChatRequest {
		return testChatRequest{testTarget: testTarget{SuffixMode: mode}, Model: "m", Messages: messages, Stream: true}
	}

	// chat：原样透传。
	chat := buildTestPayload(base("chat"))
	if _, ok := chat["messages"]; !ok {
		t.Fatal("chat 模式应保留 messages 字段")
	}
	if _, ok := chat["input"]; ok {
		t.Fatal("chat 模式不应有 input 字段")
	}

	// gpt：messages → input，文本/图片块改写。
	gpt := buildTestPayload(base("gpt"))
	if _, ok := gpt["messages"]; ok {
		t.Fatal("gpt 模式不应有 messages 字段")
	}
	input, ok := gpt["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("gpt 模式 input 期望 2 条（system 保留），实际 %#v", gpt["input"])
	}
	userContent := input[1]["content"].([]any)
	textBlock := userContent[0].(map[string]any)
	if textBlock["type"] != "input_text" {
		t.Errorf("gpt 文本块期望 input_text，实际 %v", textBlock["type"])
	}
	imgBlock := userContent[1].(map[string]any)
	if imgBlock["type"] != "input_image" || imgBlock["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("gpt 图片块应拍平为 input_image + 字符串 url，实际 %#v", imgBlock)
	}

	// claude：system 抽顶层、图片转 source、max_tokens 补全。
	claude := buildTestPayload(base("claude"))
	if claude["system"] != "你是助手" {
		t.Errorf("claude system 期望 '你是助手'，实际 %#v", claude["system"])
	}
	if claude["max_tokens"] != 4096 {
		t.Errorf("claude max_tokens 默认期望 4096，实际 %v", claude["max_tokens"])
	}
	cmsgs := claude["messages"].([]map[string]any)
	if len(cmsgs) != 1 {
		t.Fatalf("claude messages 期望 1 条（system 已抽出），实际 %d", len(cmsgs))
	}
	cblocks := cmsgs[0]["content"].([]any)
	b64 := cblocks[1].(map[string]any)
	src := b64["source"].(map[string]any)
	if b64["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != "aGVsbG8=" {
		t.Errorf("claude data URI 图片应转 base64 source，实际 %#v", b64)
	}
	urlBlock := cblocks[2].(map[string]any)
	urlSrc := urlBlock["source"].(map[string]any)
	if urlSrc["type"] != "url" || urlSrc["url"] != "https://example.com/a.png" {
		t.Errorf("claude http 图片应转 url source，实际 %#v", urlBlock)
	}
	// 显式 max_tokens 应生效。
	explicit := base("claude")
	tokens := 1000
	explicit.MaxTokens = &tokens
	if got := buildTestPayload(explicit)["max_tokens"]; got != 1000 {
		t.Errorf("claude 显式 max_tokens 期望 1000，实际 %v", got)
	}
}

// TestModelHealthList 验证聚合模型健康状态可通过管理 API 读取。
func TestModelHealthList(t *testing.T) {
	ts, st, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	want := []types.ModelHealth{{
		Model:     "deepseek-v4-pro@channel-1",
		Status:    "cooling",
		LastError: "上游返回 503",
	}}
	if err := st.Write(types.FileModelHealth, want); err != nil {
		t.Fatalf("写入健康状态: %v", err)
	}

	resp, data := apiReq(t, ts, http.MethodGet, "/api/model-health", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/model-health 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var got []types.ModelHealth
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("解析健康状态: %v", err)
	}
	if len(got) != 1 || got[0].Model != want[0].Model || got[0].LastError != want[0].LastError {
		t.Fatalf("健康状态不符: %+v", got)
	}
}

// TestChannelsCRUD 验证渠道增删改查，以及 api_key 只以密文落盘、列表不回明文。
func TestChannelsCRUD(t *testing.T) {
	ts, st, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 初始为空。
	resp, data := apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/channels 期望 200，实际 %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "[]") {
		t.Fatalf("初始渠道列表应为空数组，实际 %s", data)
	}

	// 创建一条渠道（含明文 api_key）。
	const secret = "sk-secret-abc"
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":     "本地 NewAPI",
		"base_url": "http://127.0.0.1:3001/v1",
		"api_key":  secret,
		"enabled":  true,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/channels 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var created types.Channel
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}
	if created.ID == "" {
		t.Fatal("创建渠道应返回非空 id")
	}
	if created.APIKeyCipher == "" {
		t.Fatal("创建渠道应返回非空 api_key_cipher")
	}
	if created.APIKeyCipher == secret {
		t.Fatal("api_key 不应以明文落盘")
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("创建响应不应包含明文 api_key")
	}

	// 落盘密文可解密还原明文。
	plain, err := st.Decrypt(created.APIKeyCipher)
	if err != nil {
		t.Fatalf("解密 api_key_cipher 失败: %v", err)
	}
	if plain != secret {
		t.Fatalf("解密结果 = %q，期望 %q", plain, secret)
	}

	// 列表接口不回明文。
	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if strings.Contains(string(data), secret) {
		t.Fatal("渠道列表不应包含明文 api_key")
	}
	var list []types.Channel
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("渠道列表数量/内容不符: %+v", list)
	}

	// PUT 更新名称生效。
	resp, _ = apiReq(t, ts, http.MethodPut, "/api/channels/"+created.ID, map[string]any{
		"name":     "改名渠道",
		"base_url": "http://127.0.0.1:3001/v1",
		"enabled":  false,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/channels/{id} 期望 200，实际 %d", resp.StatusCode)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	if len(list) != 1 || list[0].Name != "改名渠道" || list[0].Enabled {
		t.Fatalf("PUT 后渠道内容不符: %+v", list)
	}

	// DELETE 生效。
	resp, _ = apiReq(t, ts, http.MethodDelete, "/api/channels/"+created.ID, nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/channels/{id} 期望 200，实际 %d", resp.StatusCode)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("DELETE 后渠道列表应为空，实际 %+v", list)
	}
}

// TestSKKey 验证创建 sk- key 返回完整 key 且仅此一次，列表里不再含完整 key。
func TestSKKey(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/keys/sk", map[string]any{
		"name":   "本机调用",
		"models": []string{"*"},
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/keys/sk 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var created struct {
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}
	if !strings.HasPrefix(created.Key, "sk-") {
		t.Fatalf("返回的 key 应以 sk- 开头，实际 %q", created.Key)
	}

	// 列表聚合接口不含完整 key。
	_, data = apiReq(t, ts, http.MethodGet, "/api/keys", nil, cookie)
	if strings.Contains(string(data), created.Key) {
		t.Fatal("GET /api/keys 不应包含完整 key")
	}
	if !strings.Contains(string(data), created.Prefix) {
		t.Fatalf("GET /api/keys 应包含 key 前缀 %q", created.Prefix)
	}
}

// TestChangePassword 验证改密后旧密码登录失败、新密码成功。
func TestChangePassword(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	const newPw = "newpass123!"
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/change-password", map[string]string{
		"old": pw,
		"new": newPw,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/change-password 期望 200，实际 %d", resp.StatusCode)
	}

	// 旧密码登录失败。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": pw}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("改密后旧密码登录期望 401，实际 %d", resp.StatusCode)
	}

	// 新密码登录成功。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/login",
		map[string]string{"username": "admin", "password": newPw}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改密后新密码登录期望 200，实际 %d", resp.StatusCode)
	}
}

// TestMCPServersCRUD 验证 MCP 服务器增删改查（含 headers/env 持久化）、
// 连接测试（test）与工具枚举（mcp-tools）。
func TestMCPServersCRUD(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	f := fakemcp.New("github", []fakemcp.Tool{
		{Name: "search_code", Description: "搜索代码", InputSchema: map[string]any{"type": "object"}},
		{Name: "list_repos", Description: "列出仓库", InputSchema: map[string]any{"type": "object"}},
	})
	defer f.Close()

	// 创建（http + headers）。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/mcp-servers", map[string]any{
		"name":      "github",
		"transport": "http",
		"url":       f.URL(),
		"headers":   map[string]string{"Authorization": "Bearer token123"},
		"enabled":   true,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mcp-servers 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var created types.MCPServer
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}
	if created.ID == "" {
		t.Fatal("创建 MCP 应返回非空 id")
	}
	if created.Headers["Authorization"] != "Bearer token123" {
		t.Fatalf("headers 未正确持久化: %+v", created.Headers)
	}

	// 更新（改 name，加 env，改 headers）。
	resp, data = apiReq(t, ts, http.MethodPut, "/api/mcp-servers/"+created.ID, map[string]any{
		"name":      "gh2",
		"transport": "http",
		"url":       f.URL(),
		"env":       map[string]string{"FOO": "bar"},
		"headers":   map[string]string{"X-Api-Key": "abc"},
		"enabled":   true,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/mcp-servers/{id} 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var list []types.MCPServer
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("解析更新响应: %v", err)
	}
	if len(list) != 1 || list[0].Name != "gh2" || list[0].Env["FOO"] != "bar" || list[0].Headers["X-Api-Key"] != "abc" {
		t.Fatalf("更新结果不符: %+v", list)
	}

	// 连接测试：枚举工具。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/mcp-servers/test", map[string]any{
		"name": "probe", "transport": "http", "url": f.URL(),
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mcp-servers/test 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var probe struct {
		OK    bool `json:"ok"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("解析 test 响应: %v", err)
	}
	if !probe.OK || len(probe.Tools) != 2 {
		t.Fatalf("test 应 ok=true 且返回 2 个工具，实际 %s", data)
	}

	// 聚合工具枚举。
	resp, data = apiReq(t, ts, http.MethodGet, "/api/mcp-tools", nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/mcp-tools 期望 200，实际 %d", resp.StatusCode)
	}
	var servers []struct {
		ID    string `json:"id"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &servers); err != nil {
		t.Fatalf("解析 mcp-tools 响应: %v", err)
	}
	if len(servers) != 1 || len(servers[0].Tools) != 2 {
		t.Fatalf("mcp-tools 应返回 1 个 server、2 个工具，实际 %s", data)
	}

	// 删除。
	resp, _ = apiReq(t, ts, http.MethodDelete, "/api/mcp-servers", map[string]string{"id": created.ID}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/mcp-servers 期望 200，实际 %d", resp.StatusCode)
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/mcp-servers", nil, cookie)
	if !strings.Contains(string(data), "[]") {
		t.Fatalf("删除后 MCP 列表应为空数组，实际 %s", data)
	}
}

// TestSkillInstallHandler 验证安装路由接线：空 source 在真正下载前被拒绝（500）。
func TestSkillInstallHandler(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	resp, data := apiReq(t, ts, http.MethodPost, "/api/skills/install",
		map[string]string{"name": "x", "source": "", "version": ""}, cookie)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("空 source 期望 500，实际 %d: %s", resp.StatusCode, data)
	}
}

// TestSkillImportZipHandler 验证 multipart 上传 zip 导入并出现在技能列表。
func TestSkillImportZipHandler(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 构造 zip：单个 SKILL.md。
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatalf("zip.Create: %v", err)
	}
	if _, err := w.Write([]byte("# from-zip\n")); err != nil {
		t.Fatalf("zip 写入: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}

	// multipart 上传。
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "skill.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(zbuf.Bytes()); err != nil {
		t.Fatalf("写文件字段: %v", err)
	}
	if err := mw.WriteField("name", "from-zip"); err != nil {
		t.Fatalf("写 name 字段: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart 关闭: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/skills/import-zip", &body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("上传请求: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import-zip 期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	// 技能列表应包含 from-zip。
	_, data = apiReq(t, ts, http.MethodGet, "/api/skills", nil, cookie)
	if !strings.Contains(string(data), "from-zip") {
		t.Fatalf("技能列表应含 from-zip，实际 %s", data)
	}
}

// TestChannelProbe 验证 /api/channels/probe：新建传 base_url+api_key 直接探测；
// 编辑时 Key 不回显、只传 id，后台取已存 Key 参与探测；不落库。
func TestChannelProbe(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 假上游：/v1/models 返回模型列表。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
	}))
	t.Cleanup(upstream.Close)

	// 1) 新建探测：传 base_url + api_key。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/channels/probe", map[string]any{
		"base_url": upstream.URL + "/v1",
		"api_key":  "sk-test",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("新建 probe 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Models      []string `json:"models"`
		ModelsError string   `json:"models_error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("解析新建 probe 响应: %v", err)
	}
	if len(out.Models) != 2 || out.Models[0] != "gpt-4o" || out.Models[1] != "gpt-4o-mini" {
		t.Fatalf("新建 probe 期望 2 个模型，实际 %s", data)
	}
	if out.ModelsError != "" {
		t.Fatalf("新建 probe 不应有错误，实际 %s", out.ModelsError)
	}

	// 2) 编辑探测：先创建渠道（store 文件模式，保存时即探测成功），
	// 再只传 id + base_url（不回显 Key），后台应取已存 Key 完成探测。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":     "probe-channel",
		"base_url": upstream.URL + "/v1",
		"api_key":  "sk-stored",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建渠道期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("解析创建渠道响应: %v", err)
	}
	if created.ID == "" {
		t.Fatal("创建渠道应返回非空 id")
	}
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels/probe", map[string]any{
		"id":       created.ID,
		"base_url": upstream.URL + "/v1",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编辑 probe 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("解析编辑 probe 响应: %v", err)
	}
	if len(out.Models) != 2 || out.ModelsError != "" {
		t.Fatalf("编辑 probe 应按已存 Key 探测到 2 个模型，实际 %s", data)
	}

	// 3) 无效 id：应 404。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/channels/probe", map[string]any{
		"id":       "no-such-id",
		"base_url": upstream.URL + "/v1",
	}, cookie)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("无效 id probe 期望 404，实际 %d", resp.StatusCode)
	}

	// 4) 缺 base_url：应 400。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/channels/probe", map[string]any{
		"api_key": "sk-test",
	}, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 base_url probe 期望 400，实际 %d", resp.StatusCode)
	}
}

// TestChannelNameSync 验证渠道名称（channel_name）：
// 1) 创建渠道显式传 channel_name；
// 2) 同 Base URL 添加 Key 时不传则继承组名；
// 3) 编辑任一 Key 改渠道名 → 同 Base URL 全部 Key 同步；不同 Base URL 不受影响。
func TestChannelNameSync(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 假上游：/v1/models 返回空模型列表，创建渠道不报错即可。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(upstream.Close)

	// 创建渠道 1（含渠道名）。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":         "key-a",
		"channel_name": "主力 NewAPI",
		"base_url":     upstream.URL + "/v1",
		"api_key":      "sk-a",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建渠道 1 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var ch1 struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		ChannelName string `json:"channel_name"`
		BaseURL     string `json:"base_url"`
	}
	if err := json.Unmarshal(data, &ch1); err != nil {
		t.Fatalf("解析渠道 1: %v", err)
	}
	if ch1.ChannelName != "主力 NewAPI" {
		t.Fatalf("渠道 1 渠道名应为「主力 NewAPI」，实际 %q", ch1.ChannelName)
	}

	// 同 Base URL 添加 Key 2（不传 channel_name，应继承）。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":     "key-b",
		"base_url": upstream.URL + "/v1",
		"api_key":  "sk-b",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("添加 Key 2 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var ch2 struct {
		ID          string `json:"id"`
		ChannelName string `json:"channel_name"`
	}
	if err := json.Unmarshal(data, &ch2); err != nil {
		t.Fatalf("解析 Key 2: %v", err)
	}
	if ch2.ChannelName != "主力 NewAPI" {
		t.Fatalf("Key 2 应继承渠道名「主力 NewAPI」，实际 %q", ch2.ChannelName)
	}

	// 不同 Base URL 的渠道 3。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name":         "key-c",
		"channel_name": "备用 Volc",
		"base_url":     "https://volc.example.com/v1",
		"api_key":      "sk-c",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建渠道 3 期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	// 编辑 Key 1：渠道名改为「升级 NewAPI」→ Key1/Key2 同步，Key3 不动。
	resp, data = apiReq(t, ts, http.MethodPut, "/api/channels/"+ch1.ID, map[string]any{
		"name":         "key-a",
		"channel_name": "升级 NewAPI",
		"base_url":     upstream.URL + "/v1",
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编辑 Key 1 期望 200，实际 %d: %s", resp.StatusCode, data)
	}

	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	var channels []struct {
		ID          string `json:"id"`
		ChannelName string `json:"channel_name"`
		BaseURL     string `json:"base_url"`
	}
	if err := json.Unmarshal(data, &channels); err != nil {
		t.Fatalf("解析渠道列表: %v", err)
	}
	byID := map[string]string{}
	baseByID := map[string]string{}
	for _, c := range channels {
		byID[c.ID] = c.ChannelName
		baseByID[c.ID] = c.BaseURL
	}
	if byID[ch1.ID] != "升级 NewAPI" {
		t.Fatalf("Key 1 渠道名应为「升级 NewAPI」，实际 %q", byID[ch1.ID])
	}
	if byID[ch2.ID] != "升级 NewAPI" {
		t.Fatalf("Key 2 应同步为「升级 NewAPI」，实际 %q", byID[ch2.ID])
	}
	if byID[""] != "" && len(channels) == 3 {
		for _, c := range channels {
			if c.BaseURL != baseByID[ch1.ID] && c.ID != ch1.ID && c.ID != ch2.ID && c.ChannelName != "备用 Volc" {
				t.Fatalf("Key 3 不应被同步，实际 %q", c.ChannelName)
			}
		}
	}
}

// TestChannelMoveGroupName 验证编辑 Key 时改 Base URL（挪组）→ 渠道名跟随新组，
// 不残留旧组名；同组一致不变量不被破坏。
func TestChannelMoveGroupName(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(upstream.Close)

	// 组 A（名字 X）。
	_, data := apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name": "a1", "channel_name": "组A", "base_url": upstream.URL + "/v1", "api_key": "sk",
	}, cookie)
	var a1 struct {
		ID          string `json:"id"`
		ChannelName string `json:"channel_name"`
	}
	_ = json.Unmarshal(data, &a1)

	// 组 B（名字 Y）。
	_, data = apiReq(t, ts, http.MethodPost, "/api/channels", map[string]any{
		"name": "b1", "channel_name": "组B", "base_url": upstream.URL + "/v2", "api_key": "sk",
	}, cookie)
	var b1 struct {
		ID          string `json:"id"`
		ChannelName string `json:"channel_name"`
	}
	_ = json.Unmarshal(data, &b1)

	// 把 a1 挪到组 B：编辑时前端会回传旧组名「组A」，但后端应改用新组名「组B」。
	_, data = apiReq(t, ts, http.MethodPut, "/api/channels/"+a1.ID, map[string]any{
		"name": "a1", "channel_name": "组A", "base_url": upstream.URL + "/v2",
	}, cookie)
	if !strings.Contains(string(data), "ok") {
		t.Fatalf("挪组编辑期望成功，实际 %s", data)
	}

	_, data = apiReq(t, ts, http.MethodGet, "/api/channels", nil, cookie)
	var list []struct {
		ID          string `json:"id"`
		ChannelName string `json:"channel_name"`
		BaseURL     string `json:"base_url"`
	}
	_ = json.Unmarshal(data, &list)
	byID := map[string]struct{ name, base string }{}
	for _, c := range list {
		byID[c.ID] = struct{ name, base string }{c.ChannelName, c.BaseURL}
	}
	if byID[a1.ID].name != "组B" {
		t.Fatalf("挪组后 a1 渠道名应跟随新组「组B」，实际 %q", byID[a1.ID].name)
	}
	if byID[b1.ID].name != "组B" {
		t.Fatalf("组B 原成员 b1 渠道名不应被改，实际 %q", byID[b1.ID].name)
	}
}

// TestModelTestBuiltinSkKey 验证「Loadout 自带」模式：传 sk_key_hash，
// 后端按哈希解析出自建 SK key 明文，配 base_url 调用上游（HTTP 直调自家网关路径）。
func TestModelTestBuiltinSkKey(t *testing.T) {
	// 自带模式的 base_url 由后端按请求 Host 自动补全，所以测试服务器必须
	// 同时挂 admin API 与 /v1/models（模拟自家网关同一地址）。
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	old := config.AdminPasswordFile
	config.AdminPasswordFile = filepath.Join(dir, "admin-password")
	t.Cleanup(func() { config.AdminPasswordFile = old })
	authSvc := adminauth.NewService(st, slog.Default())
	if _, err := authSvc.EnsureFirstRun(); err != nil {
		t.Fatalf("EnsureFirstRun: %v", err)
	}
	pw, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取初始密码: %v", err)
	}
	keys := gatewaykeys.NewManager(st)
	skillSvc := skills.NewService(st, slog.Default(), t.TempDir(), t.TempDir())
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil, unifyai.NewService(slog.Default()))

	combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 自家网关 /v1/models：校验 Bearer sk- 明文，返回模型列表。
		if r.URL.Path == "/v1/models" {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"m1"},{"id":"m2"}]}`)
			return
		}
		svc.Handler().ServeHTTP(w, r)
	})
	ts := httptest.NewServer(combined)
	t.Cleanup(ts.Close)
	cookie := login(t, ts, string(pw))

	// 创建 SK key。
	resp, data := apiReq(t, ts, http.MethodPost, "/api/keys/sk", map[string]any{"name": "本机"}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("创建 SK key 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &created); err != nil || !strings.HasPrefix(created.Key, "sk-") {
		t.Fatalf("创建 SK key 应返回 sk- 开头明文，实际 %s", data)
	}

	// 取 hash（列表接口不回传明文/密文）。
	_, data = apiReq(t, ts, http.MethodGet, "/api/keys", nil, cookie)
	var keysList struct {
		SKKeys []struct {
			Hash   string `json:"hash"`
			Cipher string `json:"cipher"`
		} `json:"sk_keys"`
	}
	if err := json.Unmarshal(data, &keysList); err != nil || len(keysList.SKKeys) != 1 {
		t.Fatalf("SK key 列表应 1 条，实际 %s", data)
	}
	if keysList.SKKeys[0].Cipher != "" {
		t.Fatal("列表接口不应回传 cipher 密文")
	}
	hash := keysList.SKKeys[0].Hash

	// 只传 sk_key_hash（不传 base_url）→ 后端按 r.Host 构造 base_url + 解密明文，
	// 请求自家 /v1/models 应命中 combined 的假网关并返回 2 个模型。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/test/models", map[string]any{
		"sk_key_hash": hash,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("自带模式 test/models 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Models []string `json:"models"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if out.Error != "" || len(out.Models) != 2 {
		t.Fatalf("自带模式应按解密后的 key 探测到 2 个模型，实际 %s", data)
	}

	// 相对路径 base_url（/v1）→ 后端按 r.Host 补全，同样命中。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/test/models", map[string]any{
		"base_url":    "/v1",
		"sk_key_hash": hash,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("相对路径 base_url 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Error != "" || len(out.Models) != 2 {
		t.Fatalf("相对路径 base_url 应探测到 2 个模型，实际 %s", data)
	}

	// 完整 URL base_url（同 host）→ 直接使用，命中。
	resp, data = apiReq(t, ts, http.MethodPost, "/api/test/models", map[string]any{
		"base_url":    ts.URL + "/v1",
		"sk_key_hash": hash,
	}, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("完整 URL base_url 期望 200，实际 %d: %s", resp.StatusCode, data)
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Error != "" || len(out.Models) != 2 {
		t.Fatalf("完整 URL base_url 应探测到 2 个模型，实际 %s", data)
	}

	// 无效 hash → 400。
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/test/models", map[string]any{
		"sk_key_hash": "deadbeef",
	}, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("无效 hash 期望 400，实际 %d", resp.StatusCode)
	}

	// 删除 key 后 hash 失效 → 400。
	var listForDelete struct {
		SKKeys []struct {
			ID string `json:"id"`
		} `json:"sk_keys"`
	}
	_, data = apiReq(t, ts, http.MethodGet, "/api/keys", nil, cookie)
	_ = json.Unmarshal(data, &listForDelete)
	if len(listForDelete.SKKeys) != 1 {
		t.Fatalf("应有 1 条 SK key，实际 %d", len(listForDelete.SKKeys))
	}
	resp, _ = apiReq(t, ts, http.MethodDelete, "/api/keys/sk/"+listForDelete.SKKeys[0].ID, nil, cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除 SK key 期望 200，实际 %d", resp.StatusCode)
	}
	resp, _ = apiReq(t, ts, http.MethodPost, "/api/test/models", map[string]any{
		"sk_key_hash": hash,
	}, cookie)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("已删除 key 的 hash 期望 400，实际 %d", resp.StatusCode)
	}
}
