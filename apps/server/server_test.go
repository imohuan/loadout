package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/config"
	"loadout/core/store"
	fakellm "loadout/testkit/fake-llm"
)

// newTestEnv 组装测试环境：临时 store + 覆盖 config 的派生路径，返回 httptest server。
func newTestEnv(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}

	// 覆盖 config 的派生目录，避免污染真实 home。
	tmp := t.TempDir()
	prev := struct{ pwd, skills, cache string }{
		config.AdminPasswordFile, config.SkillsDir, config.VisionCacheDir,
	}
	config.AdminPasswordFile = filepath.Join(tmp, "admin-password")
	config.SkillsDir = filepath.Join(tmp, "skills")
	config.VisionCacheDir = filepath.Join(tmp, "vision-cache")
	t.Cleanup(func() {
		config.AdminPasswordFile = prev.pwd
		config.SkillsDir = prev.skills
		config.VisionCacheDir = prev.cache
	})

	asm, handler, err := assemble(slog.Default(), st)
	if err != nil {
		t.Fatalf("assemble 失败: %v", err)
	}
	t.Cleanup(asm.Unload)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// 首启密码写入 config.AdminPasswordFile（assemble 里 EnsureFirstRun 完成）。
	pwd, err := os.ReadFile(config.AdminPasswordFile)
	if err != nil {
		t.Fatalf("读取首启密码失败: %v", err)
	}
	return ts, strings.TrimSpace(string(pwd))
}

// TestStaticIndex 验证管理后台静态资源返回 HTML。
func TestStaticIndex(t *testing.T) {
	ts, _ := newTestEnv(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / 状态码 = %d，期望 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Loadout") {
		t.Fatalf("静态页面不含 Loadout: %q", string(body))
	}
}

// TestAPIRequiresSession 验证管理 API 需登录。
func TestAPIRequiresSession(t *testing.T) {
	ts, _ := newTestEnv(t)

	resp, err := http.Get(ts.URL + "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("未登录访问 /api/overview 状态码 = %d，期望 401", resp.StatusCode)
	}
}

// TestLoginFlow 验证登录成功返回 Cookie，之后可访问受保护端点。
func TestLoginFlow(t *testing.T) {
	ts, pwd := newTestEnv(t)

	// 错误密码 → 401。
	resp, err := http.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"wrong"}`))
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("错误密码登录状态码 = %d，期望 401", resp.StatusCode)
	}

	// 正确密码 → 200 + Set-Cookie。
	resp, err = http.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"`+pwd+`"}`))
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("正确密码登录状态码 = %d，期望 200: %s", resp.StatusCode, body)
	}
	cookie := resp.Header.Get("Set-Cookie")
	resp.Body.Close()
	if cookie == "" {
		t.Fatal("登录未返回 Set-Cookie")
	}

	// 带 Cookie 访问 overview → 200。
	req, _ := http.NewRequest("GET", ts.URL+"/api/overview", nil)
	req.Header.Set("Cookie", cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("带 cookie 访问失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("带 cookie 访问 overview 状态码 = %d，期望 200: %s", resp.StatusCode, body)
	}
}

// TestV1RequiresSkKey 验证 /v1 端点需 sk- key。
func TestV1RequiresSkKey(t *testing.T) {
	ts, _ := newTestEnv(t)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("无 key 访问 /v1 状态码 = %d，期望 401", resp.StatusCode)
	}
}

// TestMCPEndpointExists 验证 /mcp/$smart 端点存在（不 404）。
func TestMCPEndpointExists(t *testing.T) {
	ts, _ := newTestEnv(t)

	resp, err := http.Post(ts.URL+"/mcp/$smart", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /mcp/$smart 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		t.Fatal("/mcp/$smart 返回 404，端点未挂载")
	}
}

// loginCookie 登录并返回会话 Cookie。
func loginCookie(t *testing.T, ts *httptest.Server, pwd string) string {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"`+pwd+`"}`))
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("登录状态码 = %d: %s", resp.StatusCode, body)
	}
	return resp.Header.Get("Set-Cookie")
}

// doJSON 发一个带会话 Cookie 的 JSON 请求，返回响应与 body。
func doJSON(t *testing.T, ts *httptest.Server, cookie, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

// TestOverviewPluginCount 验证概览页返回真实插件数量（10）。
func TestOverviewPluginCount(t *testing.T) {
	ts, pwd := newTestEnv(t)
	cookie := loginCookie(t, ts, pwd)

	resp, data := doJSON(t, ts, cookie, "GET", "/api/overview", "")
	if resp.StatusCode != 200 {
		t.Fatalf("overview 状态码 = %d", resp.StatusCode)
	}
	var ov map[string]any
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatalf("解析 overview 失败: %v", err)
	}
	if n, ok := ov["plugins"].(float64); !ok || int(n) != 10 {
		t.Fatalf("overview.plugins = %v，期望 10", ov["plugins"])
	}
}

// TestChannelTestEndpoint 端到端验证「测试模型」端点：用 fake-llm 作渠道，测试连通并返回 pong。
func TestChannelTestEndpoint(t *testing.T) {
	ts, pwd := newTestEnv(t)
	cookie := loginCookie(t, ts, pwd)

	// 起 fake-llm 作为上游渠道。
	fake, fakeURL := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"id":"cmpl-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"pong"}}]}`)

	// 创建渠道指向 fake-llm。
	resp, data := doJSON(t, ts, cookie, "POST", "/api/channels",
		`{"name":"fake","base_url":"`+fakeURL+`/v1","api_key":"sk-test","enabled":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("创建渠道失败: %s", data)
	}
	var ch struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &ch); err != nil || ch.ID == "" {
		t.Fatalf("渠道响应无 id: %s", data)
	}

	// 测试模型连通性。
	resp, data = doJSON(t, ts, cookie, "POST", "/api/channels/test",
		`{"id":"`+ch.ID+`","model":"gpt-4o"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("测试端点状态码 = %d: %s", resp.StatusCode, data)
	}
	var tr struct {
		OK    bool   `json:"ok"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		t.Fatalf("解析测试结果失败: %v", err)
	}
	if !tr.OK || tr.Reply != "pong" {
		t.Fatalf("测试结果异常: ok=%v reply=%q，原始=%s", tr.OK, tr.Reply, data)
	}
}
