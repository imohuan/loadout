package adminapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/store"
	"loadout/plugins/admin-auth"
	"loadout/plugins/gateway-keys"
	"loadout/plugins/skills"
	"loadout/plugins/types"
)

// newTransferService 构造带 SQLite routing repository 的 Service（渠道/聚合走 DB）。
// 返回 Service、store 与技能仓库目录（skillSvc 的 repoDir）。
func newTransferService(t *testing.T) (*Service, *store.Store, string) {
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
	keys := gatewaykeys.NewManager(st)
	repoDir := t.TempDir()
	skillSvc := skills.NewService(st, slog.Default(), repoDir, t.TempDir())
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	routing, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	svc.SetRoutingServices(database, routing, nil, nil)
	return svc, st, repoDir
}

// writeSkillFile 在技能仓库目录创建技能文件（含 SKILL.md）。
func writeSkillFile(t *testing.T, repoDir, name, rel string, content []byte) {
	t.Helper()
	writeSkillFileMode(t, repoDir, name, rel, content, 0o644)
}

// writeSkillFileMode 同上，可指定文件权限位。
func writeSkillFileMode(t *testing.T, repoDir, name, rel string, content []byte, mode fs.FileMode) {
	t.Helper()
	path := filepath.Join(repoDir, name, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// seedTransferData 写入渠道（DB）、聚合（DB）、能力路由/MCP/预设（JSON）与技能文件。
func seedTransferData(t *testing.T, svc *Service, st *store.Store, repoDir string) {
	t.Helper()
	ctx := context.Background()
	cipher, err := st.Encrypt("sk-secret-key-123")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	now := "2026-01-01T00:00:00Z"
	channels := []db.Channel{
		{
			ID: "ch-1", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1",
			APIKeyCipher: cipher, ManualEnabled: true,
			CreatedAt: now, UpdatedAt: now,
			Models: []db.ChannelModel{{Model: "deepseek-chat", Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now}},
		},
		{
			ID: "ch-2", Name: "OpenAI", BaseURL: "https://api.openai.com/v1",
			ManualEnabled: true, CreatedAt: now, UpdatedAt: now,
		},
	}
	if err := svc.routing.ReplaceChannels(ctx, channels); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	aggregates := []db.Aggregate{
		{Name: "auto", Enabled: true, Targets: []db.AggregateTarget{{Model: "deepseek-chat", ChannelID: "ch-1"}}},
	}
	if err := svc.routing.ReplaceAggregates(ctx, aggregates); err != nil {
		t.Fatalf("seed aggregates: %v", err)
	}
	routes := []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: "vision", Route: "proxy", ViaOptions: []types.ViaOption{{ViaModel: "gpt-4o", ChannelID: "ch-2"}}},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("seed capability routes: %v", err)
	}
	servers := []types.MCPServer{
		{ID: "mcp-1", Name: "github", Transport: "http", URL: "https://mcp.example.com", Headers: map[string]string{"Authorization": "Bearer tok"}, Enabled: true},
	}
	if err := st.Write(types.FileMCPServers, servers); err != nil {
		t.Fatalf("seed mcp servers: %v", err)
	}
	groups := []types.Group{{Name: "dev", Tools: []types.GroupTool{{ServerID: "mcp-1", ToolName: "search"}}}}
	if err := st.Write(types.FileGroups, groups); err != nil {
		t.Fatalf("seed groups: %v", err)
	}
	presets := []types.Preset{{Name: "编程向", Skills: []string{"git-tools"}, Targets: []string{""}}}
	if err := st.Write(types.FilePresets, presets); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	if err := st.Write(types.FileSettings, types.Settings{ActivePreset: "编程向", DefaultModel: "deepseek-chat"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// 技能真实文件：git-tools（SKILL.md + 可执行 scripts/run.sh）+ 隐藏目录 .git 应被跳过。
	writeSkillFile(t, repoDir, "git-tools", "SKILL.md", []byte("---\nname: git-tools\ndescription: git 工具\n---\n\n# git-tools\n"))
	writeSkillFileMode(t, repoDir, "git-tools", "scripts/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	writeSkillFile(t, repoDir, "git-tools", ".git/config", []byte("ignored"))
	// 技能清单（与磁盘一致）。
	if err := st.Write(types.FileSkills, []types.Skill{{Name: "git-tools", Description: "git 工具"}}); err != nil {
		t.Fatalf("seed skills 清单: %v", err)
	}
}

// exportAll 调用导出端点，返回 zip 二进制与响应头。
func exportAll(t *testing.T, svc *Service) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/config/export",
		bytes.NewReader([]byte(`{"sections":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	svc.handleConfigExport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export 期望 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 期望 application/zip，实际 %s", ct)
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Fatal("缺少 Content-Disposition")
	}
	return rec.Body.Bytes()
}

func zipFiles(t *testing.T, data []byte) (map[string][]byte, map[string]fs.FileMode) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	files := map[string][]byte{}
	modes := map[string]fs.FileMode{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(rc)
		rc.Close()
		files[f.Name] = buf.Bytes()
		modes[f.Name] = f.Mode()
	}
	return files, modes
}

// multipartWithFile 构造带 file + modes 字段的 multipart 请求。
func multipartWithFile(t *testing.T, field, filename string, content []byte, modes string) *http.Request {
	t.Helper()
	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if modes != "" {
		_ = mw.WriteField("modes", modes)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/config/import", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestConfigExportBundle(t *testing.T) {
	svc, st, repoDir := newTransferService(t)
	seedTransferData(t, svc, st, repoDir)

	data := exportAll(t, svc)
	files, modes := zipFiles(t, data)

	for _, name := range []string{
		"loadout-config/manifest.json",
		"loadout-config/channels.json",
		"loadout-config/aggregates.json",
		"loadout-config/capability_routes.json",
		"loadout-config/mcp_servers.json",
		"loadout-config/mcp_groups.json",
		"loadout-config/presets.json",
		"loadout-config/settings.json",
		// 技能真实文件
		"loadout-config/skills/git-tools/SKILL.md",
		"loadout-config/skills/git-tools/scripts/run.sh",
	} {
		if files[name] == nil {
			t.Fatalf("zip 缺少 %s", name)
		}
	}
	// 隐藏目录/文件（.git）不应打包。
	if files["loadout-config/skills/git-tools/.git/config"] != nil {
		t.Fatal("zip 不应包含 .git 隐藏目录")
	}
	if string(files["loadout-config/skills/git-tools/SKILL.md"]) == "" {
		t.Fatal("技能 SKILL.md 内容为空")
	}
	// 可执行脚本的权限位应保留（Windows 无 Unix 权限位，跳过）。
	if runtime.GOOS != "windows" {
		if modes["loadout-config/skills/git-tools/scripts/run.sh"]&0o111 == 0 {
			t.Fatalf("run.sh 在 zip 中应保留可执行位，实际 %v", modes["loadout-config/skills/git-tools/scripts/run.sh"])
		}
	}

	// 渠道：api_key 明文导出。
	var channels []exportChannel
	if err := json.Unmarshal(files["loadout-config/channels.json"], &channels); err != nil {
		t.Fatalf("解析 channels.json: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("期望 2 个渠道，实际 %d", len(channels))
	}
	foundKey := false
	for _, ch := range channels {
		if ch.ID == "ch-1" && ch.APIKey == "sk-secret-key-123" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Fatal("渠道 ch-1 的 api_key 未以明文导出")
	}

	// 聚合：enabled 保留。
	var aggregates []exportAggregate
	if err := json.Unmarshal(files["loadout-config/aggregates.json"], &aggregates); err != nil {
		t.Fatalf("解析 aggregates.json: %v", err)
	}
	if len(aggregates) != 1 || aggregates[0].Name != "auto" || aggregates[0].Enabled == nil || !*aggregates[0].Enabled {
		t.Fatalf("聚合导出不符: %+v", aggregates)
	}

	// manifest。
	var manifest transferManifest
	if err := json.Unmarshal(files["loadout-config/manifest.json"], &manifest); err != nil {
		t.Fatalf("解析 manifest: %v", err)
	}
	if manifest.Format != transferFormat || manifest.Version != transferVersion {
		t.Fatalf("manifest 格式不符: %+v", manifest)
	}
	if len(manifest.Sections) != 5 {
		t.Fatalf("期望 5 个 section，实际 %d", len(manifest.Sections))
	}
}

func TestConfigImportPreviewAndApply(t *testing.T) {
	svc, st, repoDir := newTransferService(t)
	seedTransferData(t, svc, st, repoDir)

	// 1) 导出全部。
	data := exportAll(t, svc)

	// 2) 预览。
	previewReq := multipartWithFile(t, "file", "loadout-config.zip", data, "")
	previewRec := httptest.NewRecorder()
	svc.handleConfigImportPreview(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("preview 期望 200，实际 %d: %s", previewRec.Code, previewRec.Body.String())
	}
	var preview importPreview
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("解析 preview: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("preview 期望 valid")
	}
	keys := map[string]bool{}
	for _, section := range preview.Sections {
		keys[section.Key] = true
	}
	for _, key := range []string{"channels", "aggregates", "capability_routes", "mcp", "skills", "skills_files"} {
		if !keys[key] {
			t.Fatalf("preview 缺少 section %q", key)
		}
	}

	// 3) 导入到另一个实例：全部 overwrite。
	other, otherStore, otherRepo := newTransferService(t)
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data,
		`{"channels":"overwrite","aggregates":"overwrite","capability_routes":"overwrite","mcp":"overwrite","skills":"overwrite"}`)
	importRec := httptest.NewRecorder()
	other.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}
	var resp importResponse
	if err := json.Unmarshal(importRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 import 响应: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("import 无结果")
	}
	for _, r := range resp.Results {
		if len(r.Errors) > 0 {
			t.Fatalf("section %s 导入报错: %v", r.Key, r.Errors)
		}
	}
	// 技能文件结果存在且导入 1 个技能。
	foundSkillResult := false
	for _, r := range resp.Results {
		if r.Key == "skills_files" {
			foundSkillResult = true
			if r.Imported != 1 {
				t.Fatalf("期望导入 1 个技能，实际 %d", r.Imported)
			}
		}
	}
	if !foundSkillResult {
		t.Fatal("import 结果缺少 skills_files")
	}

	// 4) 验证导入结果。
	ctx := context.Background()
	channels, err := other.routing.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("导入后期望 2 个渠道，实际 %d", len(channels))
	}
	plain, err := otherStore.Decrypt(channels[0].APIKeyCipher)
	if err != nil || plain != "sk-secret-key-123" {
		t.Fatalf("导入后渠道密钥解密失败: %v / %q", err, plain)
	}

	aggregates, err := other.routing.ListAggregates(ctx)
	if err != nil {
		t.Fatalf("ListAggregates: %v", err)
	}
	if len(aggregates) != 1 || aggregates[0].Name != "auto" {
		t.Fatalf("导入后聚合不符: %+v", aggregates)
	}

	var servers []types.MCPServer
	if err := otherStore.Read(types.FileMCPServers, &servers); err != nil {
		t.Fatalf("读取 mcp_servers: %v", err)
	}
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("导入后 MCP 配置不符: %+v", servers)
	}

	var presets []types.Preset
	if err := otherStore.Read(types.FilePresets, &presets); err != nil {
		t.Fatalf("读取 presets: %v", err)
	}
	if len(presets) != 1 || presets[0].Name != "编程向" {
		t.Fatalf("导入后预设不符: %+v", presets)
	}

	// 技能文件已落到目标仓库目录。
	skillMD, err := os.ReadFile(filepath.Join(otherRepo, "git-tools", "SKILL.md"))
	if err != nil {
		t.Fatalf("导入后技能 SKILL.md 缺失: %v", err)
	}
	if !strings.Contains(string(skillMD), "git 工具") {
		t.Fatalf("导入后 SKILL.md 内容不符: %s", skillMD)
	}
	if _, err := os.Stat(filepath.Join(otherRepo, "git-tools", "scripts", "run.sh")); err != nil {
		t.Fatalf("导入后技能 scripts/run.sh 缺失: %v", err)
	}
	// 可执行位导入后保留（Windows 无 Unix 权限位，跳过）。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(otherRepo, "git-tools", "scripts", "run.sh"))
		if err != nil {
			t.Fatalf("stat run.sh: %v", err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("导入后 run.sh 应保留可执行位，实际 %v", info.Mode().Perm())
		}
	}
	// 隐藏文件不导入。
	if _, err := os.Stat(filepath.Join(otherRepo, "git-tools", ".git")); !os.IsNotExist(err) {
		t.Fatalf("隐藏目录 .git 不应导入")
	}
	// 清单写回。
	var skills []types.Skill
	if err := otherStore.Read(types.FileSkills, &skills); err != nil {
		t.Fatalf("读取 skills 清单: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "git-tools" {
		t.Fatalf("导入后技能清单不符: %+v", skills)
	}
}

func TestConfigImportAppendMerge(t *testing.T) {
	svc, st, repoDir := newTransferService(t)
	seedTransferData(t, svc, st, repoDir)

	// 目标实例已有 ch-1（同名）与一个独有渠道 ch-9。
	ctx := context.Background()
	cipher, _ := st.Encrypt("local-key")
	channels, _ := svc.routing.ListChannels(ctx)
	channels = append(channels, db.Channel{ID: "ch-9", Name: "Local", BaseURL: "https://local.example.com", APIKeyCipher: cipher, ManualEnabled: true})
	if err := svc.routing.ReplaceChannels(ctx, channels); err != nil {
		t.Fatalf("seed local channels: %v", err)
	}
	var presets []types.Preset
	if err := st.Read(types.FilePresets, &presets); err != nil {
		t.Fatalf("read presets: %v", err)
	}
	st.Write(types.FilePresets, append(presets, types.Preset{Name: "本地预设", Skills: []string{"local-skill"}}))

	// 本地独有的技能（append 应保留）。
	writeSkillFile(t, repoDir, "local-skill", "SKILL.md", []byte("---\nname: local-skill\n---\n"))

	// 导出来源数据（与目标同实例即可，合并语义看结果）。
	data := exportAll(t, svc)
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data,
		`{"channels":"append","aggregates":"append","mcp":"append","skills":"append"}`)
	importRec := httptest.NewRecorder()
	svc.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import append 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}

	// append：本地独有 ch-9 保留，导入的 ch-2 追加。
	after, _ := svc.routing.ListChannels(ctx)
	ids := map[string]bool{}
	for _, ch := range after {
		ids[ch.ID] = true
	}
	if !ids["ch-9"] || !ids["ch-2"] || len(after) != 3 {
		t.Fatalf("append 合并渠道不符: %+v", after)
	}
	// 同名 ch-1 以导入为准（密钥被覆盖为新 key）。
	for _, ch := range after {
		if ch.ID == "ch-1" {
			plain, err := st.Decrypt(ch.APIKeyCipher)
			if err != nil || plain != "sk-secret-key-123" {
				t.Fatalf("同名渠道 ch-1 密钥未按导入覆盖: %v / %q", err, plain)
			}
		}
	}

	// presets：本地预设保留 + 导入预设。
	var afterPresets []types.Preset
	if err := st.Read(types.FilePresets, &afterPresets); err != nil {
		t.Fatalf("read presets after: %v", err)
	}
	names := map[string]bool{}
	for _, p := range afterPresets {
		names[p.Name] = true
	}
	if !names["本地预设"] || !names["编程向"] || len(afterPresets) != 2 {
		t.Fatalf("append 合并预设不符: %+v", afterPresets)
	}

	// 技能文件 append：本地独有 local-skill 保留，git-tools 由包内覆盖。
	if _, err := os.Stat(filepath.Join(repoDir, "local-skill", "SKILL.md")); err != nil {
		t.Fatalf("append 后本地技能 local-skill 应保留: %v", err)
	}
	skillMD, err := os.ReadFile(filepath.Join(repoDir, "git-tools", "SKILL.md"))
	if err != nil {
		t.Fatalf("append 后 git-tools 技能缺失: %v", err)
	}
	if !strings.Contains(string(skillMD), "git 工具") {
		t.Fatalf("append 后 git-tools 内容不符: %s", skillMD)
	}
}
