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
	unifyai "loadout/plugins/unifyai"
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
	svc := NewService(st, slog.Default(), authSvc, keys, skillSvc, nil, unifyai.NewService(slog.Default()))

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

// seedTransferData 写入渠道（DB）、聚合（DB）、能力路由/MCP/预设（DB）与技能文件。
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
			ID: "ch-1", Name: "DeepSeek", ChannelName: "DeepSeek 渠道", BaseURL: "https://api.deepseek.com/v1",
			APIKeyCipher: cipher, ManualEnabled: true,
			CreatedAt: now, UpdatedAt: now,
			Models: []db.ChannelModel{{Model: "deepseek-chat", Source: "probe", Enabled: true, FirstSeenAt: now, LastSeenAt: now}},
		},
		{
			ID: "ch-2", Name: "OpenAI", ChannelName: "OpenAI 渠道", BaseURL: "https://api.openai.com/v1",
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
	if err := svc.routing.ReplaceCapabilityRoutes(ctx, routes); err != nil {
		t.Fatalf("seed capability routes: %v", err)
	}
	servers := []types.MCPServer{
		{ID: "mcp-1", Name: "github", Transport: "http", URL: "https://mcp.example.com", Headers: map[string]string{"Authorization": "Bearer tok"}, Enabled: true},
	}
	if err := svc.routing.ReplaceMCPServers(ctx, servers); err != nil {
		t.Fatalf("seed mcp servers: %v", err)
	}
	groups := []types.Group{{Name: "dev", Tools: []types.GroupTool{{ServerID: "mcp-1", ToolName: "search"}}}}
	if err := svc.routing.ReplaceGroups(ctx, groups); err != nil {
		t.Fatalf("seed groups: %v", err)
	}
	presets := []types.Preset{{Name: "编程向", Skills: []string{"git-tools"}, Targets: []string{""}}}
	if err := svc.routing.ReplacePresets(ctx, presets); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	if err := svc.routing.PutSettings(ctx, types.Settings{ActivePreset: "编程向", DefaultModel: "deepseek-chat"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// 技能真实文件：git-tools（SKILL.md + 可执行 scripts/run.sh）+ 隐藏目录 .git 应被跳过。
	writeSkillFile(t, repoDir, "git-tools", "SKILL.md", []byte("---\nname: git-tools\ndescription: git 工具\n---\n\n# git-tools\n"))
	writeSkillFileMode(t, repoDir, "git-tools", "scripts/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)
	writeSkillFile(t, repoDir, "git-tools", ".git/config", []byte("ignored"))
	// 技能清单（与磁盘一致）。
	if err := svc.routing.ReplaceSkills(ctx, []types.Skill{{Name: "git-tools", Description: "git 工具"}}); err != nil {
		t.Fatalf("seed skills 清单: %v", err)
	}
	// 火山引擎免费额度配置（"其他"分类子项）。直接写 volc_quota_config 表：
	// admin-api 的导入导出通过 sqlDB 直读此表（与 volc-free-quota 共享 SQLite）。
	if _, err := svc.sqlDB.ExecContext(ctx, `
		INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES (?, ?, ?, ?, 1, 0, ?)`,
		"ch-1", "ak-volc-1", "acc-volc-1", cipher, now); err != nil {
		t.Fatalf("seed volc_quota_config: %v", err)
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
		"loadout-config/other.json",
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
	// 渠道名称（channel_name）应随导出保留。
	for _, ch := range channels {
		if ch.ID == "ch-1" && ch.ChannelName != "DeepSeek 渠道" {
			t.Fatalf("渠道 ch-1 的 channel_name 未导出: %q", ch.ChannelName)
		}
		if ch.ID == "ch-2" && ch.ChannelName != "OpenAI 渠道" {
			t.Fatalf("渠道 ch-2 的 channel_name 未导出: %q", ch.ChannelName)
		}
	}

	// "其他"分类：火山引擎免费额度配置（access_key 明文，secret_key 明文导出）。
	var otherEnv otherExportEnvelope
	if err := json.Unmarshal(files["loadout-config/other.json"], &otherEnv); err != nil {
		t.Fatalf("解析 other.json: %v", err)
	}
	if len(otherEnv.VolcQuota) != 1 {
		t.Fatalf("期望 1 条 volc_quota 配置，实际 %d", len(otherEnv.VolcQuota))
	}
	if otherEnv.VolcQuota[0].ChannelID != "ch-1" || otherEnv.VolcQuota[0].AccessKey != "ak-volc-1" || otherEnv.VolcQuota[0].SecretKey != "sk-secret-key-123" {
		t.Fatalf("volc_quota 导出内容不符: %+v", otherEnv.VolcQuota[0])
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
	if len(manifest.Sections) != 6 {
		t.Fatalf("期望 6 个 section，实际 %d", len(manifest.Sections))
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
	for _, key := range []string{"channels", "aggregates", "capability_routes", "mcp", "skills", "skills_files", "other"} {
		if !keys[key] {
			t.Fatalf("preview 缺少 section %q", key)
		}
	}

	// 3) 导入到另一个实例：全部 overwrite。
	other, otherStore, otherRepo := newTransferService(t)
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data,
		`{"channels":"overwrite","aggregates":"overwrite","capability_routes":"overwrite","mcp":"overwrite","skills":"overwrite","other":"overwrite"}`)
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
	// 渠道名称导入后应保留。
	names := map[string]string{}
	for _, ch := range channels {
		names[ch.ID] = ch.ChannelName
	}
	if names["ch-1"] != "DeepSeek 渠道" || names["ch-2"] != "OpenAI 渠道" {
		t.Fatalf("导入后 channel_name 未保留: %+v", names)
	}
	// "其他"分类：火山引擎额度配置导入后落库，secret_key 用目标实例密钥重加密。
	var volcConfig struct {
		AccessKey  string
		AccountID  string
		Cipher     string
		Enabled    bool
		ForceBlock bool
	}
	if err := other.sqlDB.QueryRowContext(ctx,
		`SELECT access_key, account_id, secret_key_cipher, enabled, force_block FROM volc_quota_config WHERE channel_id = ?`,
		"ch-1").Scan(&volcConfig.AccessKey, &volcConfig.AccountID, &volcConfig.Cipher, &volcConfig.Enabled, &volcConfig.ForceBlock); err != nil {
		t.Fatalf("导入后 volc_quota_config 缺失: %v", err)
	}
	if volcConfig.AccessKey != "ak-volc-1" || volcConfig.AccountID != "acc-volc-1" {
		t.Fatalf("导入后 volc 配置字段不符: %+v", volcConfig)
	}
	if plain, err := otherStore.Decrypt(volcConfig.Cipher); err != nil || plain != "sk-secret-key-123" {
		t.Fatalf("导入后 volc secret_key 重加密失败: %v / %q", err, plain)
	}

	aggregates, err := other.routing.ListAggregates(ctx)
	if err != nil {
		t.Fatalf("ListAggregates: %v", err)
	}
	if len(aggregates) != 1 || aggregates[0].Name != "auto" {
		t.Fatalf("导入后聚合不符: %+v", aggregates)
	}

	servers, err := other.routing.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("导入后 MCP 配置不符: %+v", servers)
	}

	presets, err := other.routing.ListPresets(ctx)
	if err != nil {
		t.Fatalf("ListPresets: %v", err)
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
	skills, err := other.routing.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
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
	presets, err := svc.routing.ListPresets(ctx)
	if err != nil {
		t.Fatalf("read presets: %v", err)
	}
	presets = append(presets, types.Preset{Name: "本地预设", Skills: []string{"local-skill"}})
	if err := svc.routing.ReplacePresets(ctx, presets); err != nil {
		t.Fatalf("seed local preset: %v", err)
	}

	// 本地独有的技能（append 应保留）。
	writeSkillFile(t, repoDir, "local-skill", "SKILL.md", []byte("---\nname: local-skill\n---\n"))

	// 本地同键能力路由（与导入包 vision/* 路由同 key，append 应合并去重且导入优先）。
	localRoutes, err := svc.routing.ListCapabilityRoutes(ctx)
	if err != nil {
		t.Fatalf("read capability routes: %v", err)
	}
	localRoutes = append(localRoutes, types.CapabilityRoute{
		Models: []string{"*"}, Capability: "vision", Route: "error",
	})
	if err := svc.routing.ReplaceCapabilityRoutes(ctx, localRoutes); err != nil {
		t.Fatalf("seed local route: %v", err)
	}

	// 导出来源数据（与目标同实例即可，合并语义看结果）。
	data := exportAll(t, svc)
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data,
		`{"channels":"append","aggregates":"append","capability_routes":"append","mcp":"append","skills":"append"}`)
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
	afterPresets, err := svc.routing.ListPresets(ctx)
	if err != nil {
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

	// 能力路由 append 去重：同键（models+capability+channel_ids）仅保留导入条目，不产生重复。
	afterRoutes, err := svc.routing.ListCapabilityRoutes(ctx)
	if err != nil {
		t.Fatalf("read capability routes after: %v", err)
	}
	if len(afterRoutes) != 1 {
		t.Fatalf("append 后能力路由应去重为 1 条，实际 %d: %+v", len(afterRoutes), afterRoutes)
	}
	if afterRoutes[0].Route != "proxy" {
		t.Fatalf("同键能力路由应以导入条目为准（proxy），实际 %s", afterRoutes[0].Route)
	}
}

// TestConfigImportChannelNameGroupInherit 同一 Base URL 组内，空渠道名导入后应继承组内首个非空渠道名；
// 全组无渠道名时回退为 Key 名（与创建/更新逻辑一致，保证"同组同步一致"）。
func TestConfigImportChannelNameGroupInherit(t *testing.T) {
	svc, st, _ := newTransferService(t)
	ctx := context.Background()
	cipher, _ := st.Encrypt("sk-secret-key-123")
	now := "2026-01-01T00:00:00Z"
	channels := []db.Channel{
		{ID: "g1-k1", Name: "Key1", ChannelName: "同一渠道", BaseURL: "https://api.example.com/v1", APIKeyCipher: cipher, ManualEnabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "g1-k2", Name: "Key2", BaseURL: "https://api.example.com/v1", APIKeyCipher: cipher, ManualEnabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "g2-k1", Name: "Solo", BaseURL: "https://solo.example.com/v1", APIKeyCipher: cipher, ManualEnabled: true, CreatedAt: now, UpdatedAt: now},
	}
	if err := svc.routing.ReplaceChannels(ctx, channels); err != nil {
		t.Fatalf("seed channels: %v", err)
	}

	data := exportAll(t, svc)
	other, _, _ := newTransferService(t)
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data, `{"channels":"overwrite"}`)
	importRec := httptest.NewRecorder()
	other.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}

	after, err := other.routing.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	got := map[string]string{}
	for _, ch := range after {
		got[ch.ID] = ch.ChannelName
	}
	if got["g1-k1"] != "同一渠道" {
		t.Fatalf("g1-k1 渠道名不符: %q", got["g1-k1"])
	}
	if got["g1-k2"] != "同一渠道" {
		t.Fatalf("g1-k2 空渠道名未继承同组渠道名: %q", got["g1-k2"])
	}
	if got["g2-k1"] != "Solo" {
		t.Fatalf("g2-k1 全组无渠道名应回退为 Key 名: %q", got["g2-k1"])
	}
}

// TestConfigImportOtherAppendPreserveLocal "其他"分类 append 模式：包内 channel_id 已存在则
// 覆盖（access_key/force_block/secret_key 均更新），本地独有 channel_id 保留。
func TestConfigImportOtherAppendPreserveLocal(t *testing.T) {
	svc, st, _ := newTransferService(t)
	ctx := context.Background()
	if err := svc.routing.ReplaceChannels(ctx, []db.Channel{{ID: "ch-local", Name: "Local", BaseURL: "https://local.example.com/v1", ManualEnabled: true}}); err != nil {
		t.Fatalf("seed local channel: %v", err)
	}
	cipher, _ := st.Encrypt("local-volc-secret")
	if _, err := svc.sqlDB.ExecContext(ctx, `
		INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES (?, ?, ?, ?, 1, 0, ?)`,
		"ch-local", "ak-local", "acc-local", cipher, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed local volc: %v", err)
	}

	// 手工构造一个只含 other.json 的导入包（channel_id 指向本地渠道）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("loadout-config/manifest.json"); err == nil {
		_, _ = w.Write([]byte(`{"format":"loadout-config","version":1}`))
	}
	env := otherExportEnvelope{VolcQuota: []exportVolcQuotaConfig{
		{ChannelID: "ch-local", AccessKey: "ak-new", SecretKey: "new-volc-secret", Enabled: true, ForceBlock: true},
	}}
	data, _ := json.MarshalIndent(env, "", "  ")
	if w, err := zw.Create("loadout-config/other.json"); err == nil {
		_, _ = w.Write(data)
	}
	zw.Close()

	importReq := multipartWithFile(t, "file", "loadout-config.zip", buf.Bytes(), `{"other":"append"}`)
	importRec := httptest.NewRecorder()
	svc.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}

	var got struct {
		AccessKey, AccountID, Cipher string
		Enabled, ForceBlock          bool
	}
	if err := svc.sqlDB.QueryRowContext(ctx,
		`SELECT access_key, account_id, secret_key_cipher, enabled, force_block FROM volc_quota_config WHERE channel_id = ?`,
		"ch-local").Scan(&got.AccessKey, &got.AccountID, &got.Cipher, &got.Enabled, &got.ForceBlock); err != nil {
		t.Fatalf("query ch-local: %v", err)
	}
	if got.AccessKey != "ak-new" || !got.ForceBlock {
		t.Fatalf("append 未覆盖 access_key/force_block: %+v", got)
	}
	if got.AccountID != "acc-local" {
		t.Fatalf("append 不应清空已有 account_id: %q", got.AccountID)
	}
	if plain, err := st.Decrypt(got.Cipher); err != nil || plain != "new-volc-secret" {
		t.Fatalf("append 后 secret_key 未按导入重加密: %v / %q", err, plain)
	}
}

// TestConfigImportOtherOverwrite "其他"分类 overwrite 模式：包内没有的 channel_id 行被删除
// （PUT 整体替换语义），同时孤儿 volc_quota_usage 被清理。
func TestConfigImportOtherOverwrite(t *testing.T) {
	svc, st, _ := newTransferService(t)
	ctx := context.Background()
	if err := svc.routing.ReplaceChannels(ctx, []db.Channel{
		{ID: "ch-a", Name: "A", BaseURL: "https://a.example.com/v1", ManualEnabled: true},
		{ID: "ch-b", Name: "B", BaseURL: "https://b.example.com/v1", ManualEnabled: true},
	}); err != nil {
		t.Fatalf("seed channels: %v", err)
	}
	cipher, _ := st.Encrypt("secret-a")
	for _, row := range [][]any{
		{"ch-a", "ak-a", "acc-a", cipher},
		{"ch-b", "ak-b", "acc-b", cipher},
	} {
		if _, err := svc.sqlDB.ExecContext(ctx, `
			INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
			VALUES (?, ?, ?, ?, 1, 0, ?)`,
			row[0], row[1], row[2], row[3], "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("seed volc %v: %v", row[0], err)
		}
	}
	if _, err := svc.sqlDB.ExecContext(ctx, `
		INSERT INTO volc_quota_usage(account_id, model, use_count, last_used_at) VALUES ('acc-b', 'deepseek-chat', 3, ?)`,
		"2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed volc_quota_usage: %v", err)
	}

	// 包内只含 ch-a 的配置。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("loadout-config/manifest.json"); err == nil {
		_, _ = w.Write([]byte(`{"format":"loadout-config","version":1}`))
	}
	env := otherExportEnvelope{VolcQuota: []exportVolcQuotaConfig{
		{ChannelID: "ch-a", AccessKey: "ak-a", SecretKey: "secret-a", Enabled: true, ForceBlock: true},
	}}
	data, _ := json.MarshalIndent(env, "", "  ")
	if w, err := zw.Create("loadout-config/other.json"); err == nil {
		_, _ = w.Write(data)
	}
	zw.Close()

	importReq := multipartWithFile(t, "file", "loadout-config.zip", buf.Bytes(), `{"other":"overwrite"}`)
	importRec := httptest.NewRecorder()
	svc.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}

	var count int
	if err := svc.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM volc_quota_config`).Scan(&count); err != nil {
		t.Fatalf("count volc_quota_config: %v", err)
	}
	if count != 1 {
		t.Fatalf("overwrite 后期望 1 行 volc_quota_config，实际 %d", count)
	}
	var force bool
	if err := svc.sqlDB.QueryRowContext(ctx, `SELECT force_block FROM volc_quota_config WHERE channel_id = 'ch-a'`).Scan(&force); err != nil {
		t.Fatalf("query ch-a: %v", err)
	}
	if !force {
		t.Fatal("ch-a force_block 应更新为包内值")
	}
	// 孤儿 volc_quota_usage（acc-b 已无对应 config）应被清理。
	if err := svc.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM volc_quota_usage WHERE account_id = 'acc-b'`).Scan(&count); err != nil {
		t.Fatalf("count volc_quota_usage: %v", err)
	}
	if count != 0 {
		t.Fatalf("overwrite 后孤儿 volc_quota_usage 应清理，实际 %d 行", count)
	}
}

// TestConfigImportSelectiveSkip 验证多选对话框语义：modes 中未列出的 section
//（含其附带文件）整体跳过，不写库也不出现在结果里；列出的正常导入。
func TestConfigImportSelectiveSkip(t *testing.T) {
	// source 用于导出（不预置本地独有数据），target 用于导入并预置 ch-local 等
	// 不在 zip 内的本地数据，验证未勾选 section 不会写入也不会破坏。
	source, st, repoDir := newTransferService(t)
	seedTransferData(t, source, st, repoDir)
	data := exportAll(t, source)

	target, targetStore, _ := newTransferService(t)
	ctx := context.Background()
	cipher, _ := targetStore.Encrypt("local-key")
	if err := target.routing.ReplaceChannels(ctx, []db.Channel{
		{ID: "ch-local", Name: "Local", BaseURL: "https://local.example.com",
			APIKeyCipher: cipher, ManualEnabled: true, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("seed target channels: %v", err)
	}
	if err := target.routing.ReplaceMCPServers(ctx, []types.MCPServer{
		{ID: "mcp-local", Name: "local", Transport: "http", URL: "https://local.mcp", Enabled: true},
	}); err != nil {
		t.Fatalf("seed target mcp: %v", err)
	}
	if _, err := target.sqlDB.ExecContext(ctx, `
		INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
		VALUES (?, 'ak-local', 'acc-local', ?, 1, 0, '2026-01-01T00:00:00Z')`,
		"ch-local", cipher); err != nil {
		t.Fatalf("seed target volc_quota: %v", err)
	}

	// 仅勾选 skills；其余（含 channels / aggregates / capability_routes / mcp / other）
	// 与其附带文件（mcp_groups / mcp_tools_state / settings）应被跳过。
	// skills_files 是 skills 的附带文件，会被一起写入。
	importReq := multipartWithFile(t, "file", "loadout-config.zip", data,
		`{"skills":"append"}`)
	importRec := httptest.NewRecorder()
	target.handleConfigImport(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import 期望 200，实际 %d: %s", importRec.Code, importRec.Body.String())
	}
	var resp importResponse
	if err := json.Unmarshal(importRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 import 响应: %v", err)
	}

	// 结果里仅出现 skills 主配置及其附带（skills_list / settings / skills_files）。
	keys := map[string]bool{}
	for _, r := range resp.Results {
		keys[r.Key] = true
	}
	for _, want := range []string{"skills", "skills_list", "settings", "skills_files"} {
		if !keys[want] {
			t.Errorf("期望结果包含 %q，实际 keys=%v", want, keys)
		}
	}
	for _, skip := range []string{"channels", "aggregates", "capability_routes", "mcp", "other",
		"mcp_groups", "mcp_tools_state"} {
		if keys[skip] {
			t.Errorf("未勾选 %q 不应出现在结果中，实际 keys=%v", skip, keys)
		}
	}

	// channels 未勾选 → 本地独有 ch-local 保留，包内 ch-1/ch-2 不应进入 target。
	afterChannels, err := target.routing.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(afterChannels) != 1 || afterChannels[0].ID != "ch-local" {
		t.Fatalf("未勾选 channels 时本地独有应保留，实际 %+v", afterChannels)
	}

	// mcp 未勾选 → 本地 MCP 服务器保留，包内 mcp-1 不进入。
	afterServers, err := target.routing.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(afterServers) != 1 || afterServers[0].ID != "mcp-local" {
		t.Fatalf("未勾选 mcp 时本地 MCP 应保留，实际 %+v", afterServers)
	}

	// other 未勾选 → volc_quota_config 行数不变（仍为 1 行 ch-local）。
	var volcCount int
	if err := target.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM volc_quota_config`).Scan(&volcCount); err != nil {
		t.Fatalf("count volc_quota_config: %v", err)
	}
	if volcCount != 1 {
		t.Fatalf("未勾选 other 时 volc_quota_config 应仅保留本地 1 行，实际 %d", volcCount)
	}
}
