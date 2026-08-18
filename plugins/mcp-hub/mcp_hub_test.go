package mcphub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/config"
	"loadout/core/mcpkit"
	"loadout/core/store"
	"loadout/plugins/types"
	fakemcp "loadout/testkit/fake-mcp"
)

// hubEnv 是一次测试的完整环境：服务、store、技能仓库目录、两个假上游。
type hubEnv struct {
	svc     *Service
	st      *store.Store
	repoDir string
	f1      *fakemcp.FakeMCP
	f2      *fakemcp.FakeMCP
}

// newHubEnv 组装测试环境：临时 store + 技能仓库 + 两个 fake-mcp 上游（各 2 工具，其一同名）。
func newHubEnv(t *testing.T) *hubEnv {
	t.Helper()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("创建技能仓库目录失败: %v", err)
	}
	prevSkills := config.SkillsDir
	config.SkillsDir = repoDir
	t.Cleanup(func() { config.SkillsDir = prevSkills })

	f1 := fakemcp.New("github", []fakemcp.Tool{
		{Name: "search_code", Description: "搜索代码", InputSchema: map[string]any{"type": "object"}, Result: "github search result"},
		{Name: "list_repos", Description: "列出仓库", InputSchema: map[string]any{"type": "object"}, Result: "github repos"},
	})
	f2 := fakemcp.New("weather", []fakemcp.Tool{
		{Name: "search_code", Description: "天气搜索", InputSchema: map[string]any{"type": "object"}, Result: "weather search result"},
		{Name: "forecast", Description: "天气预报", InputSchema: map[string]any{"type": "object"}, Result: "weather forecast"},
	})
	t.Cleanup(f1.Close)
	t.Cleanup(f2.Close)

	servers := []types.MCPServer{
		{ID: "srv-github", Name: "github", Transport: types.TransportHTTP, URL: f1.URL(), Enabled: true},
		{ID: "srv-weather", Name: "weather", Transport: types.TransportHTTP, URL: f2.URL(), Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("写入 mcp_servers.json 失败: %v", err)
	}

	return &hubEnv{
		svc:     NewService(st, slog.Default(), nil),
		st:      st,
		repoDir: repoDir,
		f1:      f1,
		f2:      f2,
	}
}

// mkSkill 在仓库目录造一个技能，内含带 frontmatter 的 SKILL.md。
func mkSkill(t *testing.T, repoDir, name string) {
	t.Helper()
	dir := filepath.Join(repoDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录 %s 失败: %v", name, err)
	}
	body := "---\nname: " + name + "\ndescription: 网页设计技能\n---\n# 正文\nbody of " + name
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md（%s）失败: %v", name, err)
	}
}

// hasTool 判断工具列表里是否存在指定调用名。
func hasTool(tools []ToolEntry, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// toolNames 返回工具列表里的调用名集合。
func toolNames(tools []ToolEntry) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		out[t.Name] = true
	}
	return out
}

// TestBuildIndexCollects 验证 BuildIndex 收集 4 个工具（原始名，不前缀）、分类数量正确、version 递增。
func TestBuildIndexCollects(t *testing.T) {
	env := newHubEnv(t)

	idx, err := env.svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}
	if len(idx.Tools) != 4 {
		t.Fatalf("工具数量 = %d，期望 4，实际：%v", len(idx.Tools), toolNames(idx.Tools))
	}

	names := toolNames(idx.Tools)
	// BuildIndex 缓存用原始名（冲突前缀改为 resolveTools 按视图计算）。
	if !names["search_code"] || !names["list_repos"] || !names["forecast"] {
		t.Fatalf("缓存应含原始名工具，实际：%v", names)
	}
	if names["github_search_code"] || names["weather_search_code"] {
		t.Fatalf("BuildIndex 缓存不应加冲突前缀，实际：%v", names)
	}
	if idx.Version < 1 {
		t.Fatalf("index_version = %d，期望 ≥ 1", idx.Version)
	}

	if len(idx.Categories) != 2 {
		t.Fatalf("分类数量 = %d，期望 2（github、weather）", len(idx.Categories))
	}

	// 再次构建 → index_version 递增。
	idx2, err := env.svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("第二次 BuildIndex 失败: %v", err)
	}
	if idx2.Version != idx.Version+1 {
		t.Fatalf("index_version 未递增：第一次 %d，第二次 %d", idx.Version, idx2.Version)
	}
}

// TestToolViewRouting 验证 $smart / 单 MCP / 分组三种路由方式的工具视图。
func TestToolViewRouting(t *testing.T) {
	env := newHubEnv(t)

	// 注册技能 + 造 SKILL.md。
	if err := env.st.Write(types.FileSkills, []types.Skill{
		{Name: "web-design", Source: "vercel-labs/agent-skills", InstalledAt: "2026-01-01T00:00:00Z", Version: "main"},
	}); err != nil {
		t.Fatalf("写入 skills.json 失败: %v", err)
	}
	mkSkill(t, env.repoDir, "web-design")

	// 分组勾选 github 的 list_repos。
	if err := env.st.Write(types.FileGroups, []types.Group{
		{Name: "group1", Tools: []types.GroupTool{{ServerID: "srv-github", ToolName: "list_repos"}}},
	}); err != nil {
		t.Fatalf("写入 groups.json 失败: %v", err)
	}

	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	// $smart 默认返回全部工具（含技能）。
	smart, err := env.svc.ToolView("/mcp/$smart")
	if err != nil {
		t.Fatalf("ToolView($smart) 失败: %v", err)
	}
	if len(smart) != 5 {
		t.Fatalf("$smart 视图工具数 = %d，期望 5（github 2 + weather 2 + 技能 1）", len(smart))
	}
	if !hasTool(smart, "web-design") {
		t.Fatalf("$smart 视图应包含技能 web-design，实际: %v", toolNames(smart))
	}

	// $smart 指定分组 group1 → 仅该分组勾选工具。
	grouped, err := env.svc.resolveTools("/mcp/$smart", "group1")
	if err != nil {
		t.Fatalf("resolveTools($smart, group1) 失败: %v", err)
	}
	if len(grouped) != 1 || grouped[0].Name != "list_repos" {
		t.Fatalf("$smart(group1) 视图 = %+v，期望只含 list_repos", grouped)
	}

	// $smart 指定不存在的分组 → 报错。
	if _, err := env.svc.resolveTools("/mcp/$smart", "nope"); err == nil {
		t.Fatal("$smart 指定不存在分组应报错，实际返回 nil")
	}

	// /mcp/github 只含该上游工具。
	gh, err := env.svc.ToolView("/mcp/github")
	if err != nil {
		t.Fatalf("ToolView(github) 失败: %v", err)
	}
	if len(gh) != 2 {
		t.Fatalf("github 视图工具数 = %d，期望 2", len(gh))
	}
	for _, tool := range gh {
		if tool.ServerID != "srv-github" {
			t.Fatalf("github 视图混入了其他上游工具：%+v", tool)
		}
	}

	// /mcp/group1 只含勾选工具。
	g, err := env.svc.ToolView("/mcp/group1")
	if err != nil {
		t.Fatalf("ToolView(group1) 失败: %v", err)
	}
	if len(g) != 1 || g[0].Name != "list_repos" {
		t.Fatalf("group1 视图 = %+v，期望只含 list_repos", g)
	}

	// 未知端点报错。
	if _, err := env.svc.ToolView("/mcp/nope"); err == nil {
		t.Fatal("未知端点应返回错误，实际返回 nil")
	}
}

// TestStatusFlatAndCategory 验证 status 无参（≤ 阈值）扁平返回、带 category 返回该分类工具。
func TestStatusFlatAndCategory(t *testing.T) {
	env := newHubEnv(t)
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	// 无参：github 视图 2 工具 ≤ 阈值 → 扁平返回完整列表（不含 schema）。
	out, err := env.svc.Status("/mcp/github", nil)
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("Status 返回非 JSON 数组: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("扁平列表长度 = %d，期望 2", len(list))
	}
	for _, item := range list {
		if _, ok := item["name"]; !ok {
			t.Fatalf("扁平条目缺 name 字段: %v", item)
		}
		if _, ok := item["inputSchema"]; ok {
			t.Fatalf("扁平条目不应含 inputSchema: %v", item)
		}
	}

	// 带 category：返回该分类下所有工具。
	out, err = env.svc.Status("/mcp/github", map[string]any{"category": "github"})
	if err != nil {
		t.Fatalf("Status(category) 失败: %v", err)
	}
	var catList []map[string]any
	if err := json.Unmarshal([]byte(out), &catList); err != nil {
		t.Fatalf("Status(category) 返回非 JSON 数组: %v", err)
	}
	if len(catList) != 2 {
		t.Fatalf("分类 github 工具数 = %d，期望 2", len(catList))
	}

	// category 支持逗号分隔多个分类。
	out, err = env.svc.Status("/mcp/$smart", map[string]any{"category": "github, weather"})
	if err != nil {
		t.Fatalf("Status(多分类) 失败: %v", err)
	}
	var multi []map[string]any
	if err := json.Unmarshal([]byte(out), &multi); err != nil {
		t.Fatalf("Status(多分类) 返回非 JSON 数组: %v", err)
	}
	if len(multi) != 4 {
		t.Fatalf("多分类 github,weather 工具数 = %d，期望 4", len(multi))
	}

	// category = all 返回全部。
	out, err = env.svc.Status("/mcp/$smart", map[string]any{"category": "all"})
	if err != nil {
		t.Fatalf("Status(all) 失败: %v", err)
	}
	var all []map[string]any
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatalf("Status(all) 返回非 JSON 数组: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("category=all 工具数 = %d，期望 4", len(all))
	}
}

// TestStatusOverview 验证 status 无参（> 阈值）返回分类总览 + index_version。
func TestStatusOverview(t *testing.T) {
	env := newHubEnv(t)
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	prev := config.StatusFlatThreshold
	config.StatusFlatThreshold = 1
	defer func() { config.StatusFlatThreshold = prev }()

	out, err := env.svc.Status("/mcp/github", nil)
	if err != nil {
		t.Fatalf("Status 失败: %v", err)
	}
	var ov struct {
		Categories   []map[string]any `json:"categories"`
		IndexVersion int              `json:"index_version"`
	}
	if err := json.Unmarshal([]byte(out), &ov); err != nil {
		t.Fatalf("Status 返回非总览 JSON: %v", err)
	}
	if ov.IndexVersion < 1 {
		t.Fatalf("index_version = %d，期望 ≥ 1", ov.IndexVersion)
	}
	if len(ov.Categories) != 1 {
		t.Fatalf("分类数量 = %d，期望 1（github）", len(ov.Categories))
	}
}

// TestGet 验证 get 按 tools 批量返回 schema、技能 get 返回 SKILL.md 正文。
func TestGet(t *testing.T) {
	env := newHubEnv(t)

	if err := env.st.Write(types.FileSkills, []types.Skill{
		{Name: "web-design", Source: "vercel-labs/agent-skills", InstalledAt: "2026-01-01T00:00:00Z", Version: "main"},
	}); err != nil {
		t.Fatalf("写入 skills.json 失败: %v", err)
	}
	mkSkill(t, env.repoDir, "web-design")
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	// 按 tools 批量取完整 schema。
	out, err := env.svc.Get("/mcp/github", map[string]any{"tools": []string{"search_code", "list_repos"}})
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("Get 返回非 JSON 数组: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("Get 条目数 = %d，期望 2", len(list))
	}
	for _, item := range list {
		if _, ok := item["inputSchema"]; !ok {
			t.Fatalf("Get 条目缺 inputSchema: %v", item)
		}
	}

	// 技能 get 返回 SKILL.md 全文（含 frontmatter 描述）。
	out, err = env.svc.Get("/mcp/$smart", map[string]any{"tools": []string{"web-design"}})
	if err != nil {
		t.Fatalf("Get(技能) 失败: %v", err)
	}
	var skills []map[string]any
	if err := json.Unmarshal([]byte(out), &skills); err != nil {
		t.Fatalf("Get(技能) 返回非 JSON 数组: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("技能条目数 = %d，期望 1", len(skills))
	}
	body, ok := skills[0]["body"].(string)
	if !ok || !strings.Contains(body, "网页设计技能") {
		t.Fatalf("技能 get 未返回 SKILL.md 正文: %v", skills[0])
	}

	// tools 传单个字符串（而非数组）时兼容：只返回该工具。
	out, err = env.svc.Get("/mcp/github", map[string]any{"tools": "list_repos"})
	if err != nil {
		t.Fatalf("Get(单字符串 tools) 失败: %v", err)
	}
	var single []map[string]any
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		t.Fatalf("Get(单字符串) 返回非 JSON 数组: %v", err)
	}
	if len(single) != 1 || single[0]["name"] != "list_repos" {
		t.Fatalf("Get(单字符串) 应只返回 list_repos，实际: %v", single)
	}

	// get 不再有 category 参数；原始名匹配跨来源同名时返回全部（带前缀名）。
	out, err = env.svc.Get("/mcp/$smart", map[string]any{"tools": []string{"search_code"}})
	if err != nil {
		t.Fatalf("Get(原始名 search_code) 失败: %v", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("Get(原始名) 返回非 JSON 数组: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("Get(原始名 search_code) 应返回 2 个（跨来源同名），实际: %v", raw)
	}

	// 用 status 返回的前缀名精确获取单个。
	out, err = env.svc.Get("/mcp/$smart", map[string]any{"tools": []string{"github_search_code"}})
	if err != nil {
		t.Fatalf("Get(前缀名 github_search_code) 失败: %v", err)
	}
	var pref []map[string]any
	if err := json.Unmarshal([]byte(out), &pref); err != nil {
		t.Fatalf("Get(前缀名) 返回非 JSON 数组: %v", err)
	}
	if len(pref) != 1 || pref[0]["name"] != "github_search_code" {
		t.Fatalf("Get(前缀名 github_search_code) 应只返回 1 个，实际: %v", pref)
	}

	// get 缺少 tools 时报错。
	if _, err := env.svc.Get("/mcp/github", map[string]any{}); err == nil {
		t.Fatal("Get 缺少 tools 应报错，实际返回 nil")
	}

	// 「分类_工具名」组合：github_list_repos 显式限定分类（即使工具没加前缀也能查）。
	out, err = env.svc.Get("/mcp/$smart", map[string]any{"tools": []string{"github_list_repos"}})
	if err != nil {
		t.Fatalf("Get(分类_工具名) 失败: %v", err)
	}
	var combo []map[string]any
	if err := json.Unmarshal([]byte(out), &combo); err != nil {
		t.Fatalf("Get(分类_工具名) 返回非 JSON 数组: %v", err)
	}
	if len(combo) != 1 || combo[0]["source"] != "github" || combo[0]["name"] != "list_repos" {
		t.Fatalf("Get(分类_工具名) 应只返回 github 的 list_repos，实际: %v", combo)
	}
}

// TestInvoke 验证 invoke 转发到 fake-mcp（记录 name+args）并返回结果；技能 invoke 返回正文。
func TestInvoke(t *testing.T) {
	env := newHubEnv(t)
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	out, err := env.svc.Invoke(context.Background(), "/mcp/github", map[string]any{
		"tool":      "list_repos",
		"arguments": map[string]any{"owner": "acme"},
	})
	if err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	var res invokeResp
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("Invoke 返回非预期 JSON: %v", err)
	}
	if res.IsError {
		t.Fatal("Invoke 返回了错误结果")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "github repos") {
		t.Fatalf("Invoke 结果内容不符: %+v", res.Content)
	}

	calls := env.f1.Calls()
	if len(calls) != 1 {
		t.Fatalf("fake-mcp 记录调用数 = %d，期望 1", len(calls))
	}
	if calls[0].Name != "list_repos" {
		t.Fatalf("fake-mcp 记录的工具名 = %q，期望 list_repos（原始名）", calls[0].Name)
	}
	if calls[0].Args["owner"] != "acme" {
		t.Fatalf("fake-mcp 记录的参数 = %v，期望 owner=acme", calls[0].Args)
	}

	// 技能 invoke：直接返回 SKILL.md 全文，不执行。
	if err := env.st.Write(types.FileSkills, []types.Skill{
		{Name: "web-design", Source: "vercel-labs/agent-skills", InstalledAt: "2026-01-01T00:00:00Z", Version: "main"},
	}); err != nil {
		t.Fatalf("写入 skills.json 失败: %v", err)
	}
	mkSkill(t, env.repoDir, "web-design")
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("第二次 BuildIndex 失败: %v", err)
	}

	out, err = env.svc.Invoke(context.Background(), "/mcp/$smart", map[string]any{"tool": "web-design"})
	if err != nil {
		t.Fatalf("Invoke(技能) 失败: %v", err)
	}
	var skillRes invokeResp
	if err := json.Unmarshal([]byte(out), &skillRes); err != nil {
		t.Fatalf("Invoke(技能) 返回非预期 JSON: %v", err)
	}
	if len(skillRes.Content) == 0 || !strings.Contains(skillRes.Content[0].Text, "网页设计技能") {
		t.Fatalf("Invoke(技能) 未返回 SKILL.md 正文: %+v", skillRes)
	}
}

// TestToolStateSwitch 验证 tools_state 关某工具后 BuildIndex 不再含它。
func TestToolStateSwitch(t *testing.T) {
	env := newHubEnv(t)

	if err := env.st.Write(types.FileToolsState, []types.ToolState{
		{ServerID: "srv-github", ToolName: "list_repos", Enabled: false},
	}); err != nil {
		t.Fatalf("写入 tools_state.json 失败: %v", err)
	}

	idx, err := env.svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}
	if hasTool(idx.Tools, "list_repos") {
		t.Fatalf("被 tools_state 关闭的 list_repos 仍出现在索引: %v", toolNames(idx.Tools))
	}
	if len(idx.Tools) != 3 {
		t.Fatalf("关闭一个工具后数量 = %d，期望 3", len(idx.Tools))
	}
}

// TestMCPServerSwitch 验证 mcp_servers.enabled=false 后该上游工具消失。
func TestMCPServerSwitch(t *testing.T) {
	env := newHubEnv(t)

	servers := []types.MCPServer{
		{ID: "srv-github", Name: "github", Transport: types.TransportHTTP, URL: env.f1.URL(), Enabled: true},
		{ID: "srv-weather", Name: "weather", Transport: types.TransportHTTP, URL: env.f2.URL(), Enabled: false},
	}
	if err := env.st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("写入 mcp_servers.json 失败: %v", err)
	}

	idx, err := env.svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}
	names := toolNames(idx.Tools)
	// weather 关闭后 search_code 不再同名冲突，应恢复原名（不带头缀）。
	if !names["search_code"] || !names["list_repos"] {
		t.Fatalf("github 工具缺失，实际：%v", names)
	}
	if names["weather_search_code"] || names["forecast"] {
		t.Fatalf("被关闭的 weather 上游工具仍存在，实际：%v", names)
	}
}

// TestEndpointServer 验证单 MCP / 分组端点直接暴露工具（而非 3 入口），未知端点报错。
func TestEndpointServer(t *testing.T) {
	env := newHubEnv(t)
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	srv, err := env.svc.EndpointServer("/mcp/github")
	if err != nil {
		t.Fatalf("EndpointServer(github) 失败: %v", err)
	}
	if srv == nil {
		t.Fatal("EndpointServer 返回 nil server")
	}

	// github 端点直接暴露该上游 2 个工具（视图内无跨来源同名，用原始名），而不是 status/get/invoke 3 个入口。
	tools := listServerTools(t, srv)
	if len(tools) != 2 {
		t.Fatalf("github 端点暴露 %d 个工具，期望 2，实际: %v", len(tools), tools)
	}
	if !tools["search_code"] || !tools["list_repos"] {
		t.Fatalf("github 端点应暴露 search_code/list_repos，实际: %v", tools)
	}
	if tools["status"] || tools["get"] || tools["invoke"] {
		t.Fatalf("github 端点不应暴露 3 入口工具，实际: %v", tools)
	}

	if _, err := env.svc.EndpointServer("/mcp/nope"); err == nil {
		t.Fatal("EndpointServer 未知端点应返回错误，实际返回 nil")
	}
}

// TestSmartEndpointServer 验证 $smart 端点仍固定暴露 3 个入口工具，且按分组解析视图。
func TestSmartEndpointServer(t *testing.T) {
	env := newHubEnv(t)

	if err := env.st.Write(types.FileGroups, []types.Group{
		{Name: "group1", Tools: []types.GroupTool{{ServerID: "srv-github", ToolName: "list_repos"}}},
	}); err != nil {
		t.Fatalf("写入 groups.json 失败: %v", err)
	}
	if _, err := env.svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex 失败: %v", err)
	}

	srv := env.svc.SmartEndpointServer("")
	tools := listServerTools(t, srv)
	if len(tools) != 3 || !tools["status"] || !tools["get"] || !tools["invoke"] {
		t.Fatalf("$smart 端点应固定暴露 status/get/invoke 3 工具，实际: %v", tools)
	}

	// 指定分组 group1：status 只返回 list_repos。
	grouped, err := env.svc.resolveTools("/mcp/$smart", "group1")
	if err != nil {
		t.Fatalf("resolveTools($smart, group1) 失败: %v", err)
	}
	if len(grouped) != 1 || grouped[0].Name != "list_repos" {
		t.Fatalf("$smart(group1) 视图 = %+v，期望只含 list_repos", grouped)
	}
}

// listServerTools 把 *mcp.Server 挂到 streamable HTTP 测试服务器，连接后 ListTools 返回工具名集合。
func listServerTools(t *testing.T, srv *mcp.Server) map[string]bool {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := mcpkit.NewUpstream(mcpkit.UpstreamConfig{Name: "probe", Transport: "http", URL: ts.URL})
	defer up.Close()

	infos, err := up.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]bool{}
	for _, info := range infos {
		out[info.Name] = true
	}
	return out
}

// TestDedupTools 验证去重：RawName+Description+InputSchema 完全相同的只保留第一个。
func TestDedupTools(t *testing.T) {
	schema := map[string]any{"type": "object"}
	tools := []ToolEntry{
		{Name: "a_search", RawName: "search", Description: "搜索", Category: "a", InputSchema: schema},
		{Name: "b_search", RawName: "search", Description: "搜索", Category: "b", InputSchema: schema},    // 与 a 完全相同（仅来源不同）
		{Name: "c_search", RawName: "search", Description: "另一种搜索", Category: "c", InputSchema: schema}, // 描述不同，不去重
	}
	got := dedupTools(tools)
	if len(got) != 2 {
		t.Fatalf("去重后应剩 2 个，实际 %d", len(got))
	}
	if got[0].Name != "a_search" || got[1].Name != "c_search" {
		t.Fatalf("去重结果不符: %+v", got)
	}
}

// TestGroupChangeInvalidate 验证分组勾选变更后，Invalidate 使重新构建的端点视图反映最新勾选。
func TestGroupChangeInvalidate(t *testing.T) {
	env := newHubEnv(t)

	if err := env.st.Write(types.FileGroups, []types.Group{
		{Name: "group1", Tools: []types.GroupTool{{ServerID: "srv-github", ToolName: "list_repos"}}},
	}); err != nil {
		t.Fatalf("写入 groups.json 失败: %v", err)
	}

	// 首次构建：group1 只暴露 list_repos。
	srv1, err := env.svc.EndpointServer("/mcp/group1")
	if err != nil {
		t.Fatalf("EndpointServer(group1) 失败: %v", err)
	}
	tools1 := listServerTools(t, srv1)
	if !tools1["list_repos"] || tools1["search_code"] {
		t.Fatalf("初始 group1 应只暴露 list_repos，实际: %v", tools1)
	}

	// 修改分组勾选为 search_code，并失效缓存（模拟 admin-api 变更后通知）。
	if err := env.st.Write(types.FileGroups, []types.Group{
		{Name: "group1", Tools: []types.GroupTool{{ServerID: "srv-github", ToolName: "search_code"}}},
	}); err != nil {
		t.Fatalf("重写 groups.json 失败: %v", err)
	}
	env.svc.Invalidate()

	// 重新构建：group1 应只暴露 search_code（视图内唯一，不加前缀）。
	srv2, err := env.svc.EndpointServer("/mcp/group1")
	if err != nil {
		t.Fatalf("Invalidate 后 EndpointServer(group1) 失败: %v", err)
	}
	tools2 := listServerTools(t, srv2)
	if !tools2["search_code"] || tools2["list_repos"] {
		t.Fatalf("变更后 group1 应只暴露 search_code，实际: %v", tools2)
	}
}

// TestEndpoints 验证 Endpoints 列出所有端点路径（enabled 上游 + 分组 + $smart）。
func TestEndpoints(t *testing.T) {
	env := newHubEnv(t)

	if err := env.st.Write(types.FileGroups, []types.Group{
		{Name: "group1", Tools: []types.GroupTool{{ServerID: "srv-github", ToolName: "list_repos"}}},
	}); err != nil {
		t.Fatalf("写入 groups.json 失败: %v", err)
	}

	eps, err := env.svc.Endpoints()
	if err != nil {
		t.Fatalf("Endpoints 失败: %v", err)
	}
	got := map[string]bool{}
	for _, e := range eps {
		got[e] = true
	}
	for _, want := range []string{"/mcp/github", "/mcp/weather", "/mcp/group1", "/mcp/$smart"} {
		if !got[want] {
			t.Fatalf("Endpoints 缺 %s，实际：%v", want, eps)
		}
	}
}

// TestBuildIndexToleratesDeadUpstream 验证 BuildIndex 容错：某个 enabled 上游
// 连接失败时跳过该上游（记日志），不 abort 整个索引——否则 $smart / 分组端点
// 会因单个坏上游整体不可用（且不埋点）。
func TestBuildIndexToleratesDeadUpstream(t *testing.T) {
	env := newHubEnv(t)

	// 追加一个指向不存在端口的坏 server（HTTP 连接必然失败）。
	var list []types.MCPServer
	if err := env.st.Read(types.FileMCPServers, &list); err != nil {
		t.Fatalf("read servers: %v", err)
	}
	list = append(list, types.MCPServer{ID: "srv-dead", Name: "dead", Transport: types.TransportHTTP, URL: "http://127.0.0.1:39999/mcp", Enabled: true})
	if err := env.st.Write(types.FileMCPServers, list); err != nil {
		t.Fatalf("write servers: %v", err)
	}

	idx, err := env.svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex 应容忍坏上游成功，实际失败: %v", err)
	}
	// 好上游工具应全部存在（github 2 + weather 2），坏上游工具不存在。
	if len(idx.Tools) != 4 {
		t.Fatalf("索引工具数 = %d，期望 4（坏上游被跳过）", len(idx.Tools))
	}
	for _, name := range []string{"search_code", "list_repos", "forecast"} {
		if !hasTool(idx.Tools, name) {
			t.Fatalf("索引应包含 %s（来自健康上游），实际缺失", name)
		}
	}
	if hasTool(idx.Tools, "dead_tool") {
		t.Fatal("坏上游的工具不应出现在索引中")
	}
}
