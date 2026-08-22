package adminapi

// 配置导入导出（配置迁移）。
//
// 导出：POST /api/config/export
//   body: {"sections":["channels","aggregates","capability_routes","mcp","skills"]}
//   返回 application/zip：loadout-config/ 目录内按 section 分文件的 JSON。
//   渠道的 api_key 以明文导出（.secret 随实例不同，密文跨环境不可用），
//   导入时用目标实例的密钥重新加密。
//
// 导入预览：POST /api/config/import/preview
//   multipart: file=xxx.zip → 返回 zip 内各 section 摘要（不落盘）。
//
// 导入：POST /api/config/import
//   multipart: file=xxx.zip + modes={"channels":"overwrite",...}
//   modes 取值 overwrite（全量替换）/ append（与现有合并，同名按 id/name 去重）。
//   返回逐 section 的导入报告。

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loadout/core/db"
	"loadout/plugins/types"
)

// 配置分类标识（导出/导入共用的 section key）。
const (
	sectionChannels         = "channels"
	sectionAggregates       = "aggregates"
	sectionCapabilityRoutes = "capability_routes"
	sectionMCP              = "mcp"
	sectionSkills           = "skills"
	sectionOther            = "other" // 杂项：火山引擎免费额度等，统一归类到"其他"
)

// 迁移包格式标识与版本。
const (
	transferFormat  = "loadout-config"
	transferVersion = 1
	// zip 内配置文件的统一包裹目录前缀。
	zipRootPrefix = "loadout-config/"
	// 导入 zip 内容总大小上限（技能实际文件可能较大，放宽到 256MB）。
	maxImportZipSize = 256 << 20
	// 技能文件解压后的总字节上限（防 zip 炸弹耗尽磁盘）。
	maxExtractSkillBytes = 512 << 20
)

// 导入模式。
const (
	modeOverwrite = "overwrite"
	modeAppend    = "append"
)

// sectionInfo 每个配置分类的元数据：key / 中文名 / zip 内文件名。
type sectionInfo struct {
	key  string
	name string
	file string
	// count 计算 zip 内该文件的数量（数组长度；settings 为 1）。
	count func(data []byte) int
}

var transferSections = []sectionInfo{
	{key: sectionChannels, name: "渠道配置", file: "channels.json", count: jsonArrayCount},
	{key: sectionAggregates, name: "聚合模型配置", file: "aggregates.json", count: jsonArrayCount},
	{key: sectionCapabilityRoutes, name: "能力路由配置", file: "capability_routes.json", count: jsonArrayCount},
	{key: sectionMCP, name: "MCP 配置", file: "mcp_servers.json", count: jsonArrayCount},
	{key: sectionSkills, name: "Skills 配置", file: "presets.json", count: jsonArrayCount},
	{key: sectionOther, name: "其他", file: "other.json", count: otherConfigCount},
}

// 额外随 section 一起导出的文件（工具开关/分组/技能清单/设置）。

func jsonArrayCount(data []byte) int {
	var v []json.RawMessage
	if json.Unmarshal(data, &v) != nil {
		return 0
	}
	return len(v)
}

func jsonObjectCount(data []byte) int {
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil {
		return 0
	}
	return 1
}

// otherExportEnvelope "其他"分类的统一导出包：键为子分类标识，值为该子分类的
// 配置数组。结构与主 section 的数组形式保持一致，方便按子项独立展示与模式选择。
// 当前内置 volc_quota（火山引擎免费额度 AK/SK 配置），后续可加新子项（如
// unifyai/sensitive filter 等杂项配置）无需变动顶层结构。
type otherExportEnvelope struct {
	VolcQuota []exportVolcQuotaConfig `json:"volc_quota,omitempty"`
}

// exportVolcQuotaConfig 火山引擎免费额度配置导出结构：SecretKey 以明文导出，
// 导入时由目标实例用 st.Encrypt 重新加密（与渠道 APIKey 一致）。运行态字段
// （last_synced_at / last_error / updated_at）不导出，由导入端首次刷新重建。
type exportVolcQuotaConfig struct {
	ChannelID  string `json:"channel_id"`
	AccountID  string `json:"account_id,omitempty"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key,omitempty"`
	Enabled    bool   `json:"enabled"`
	ForceBlock bool   `json:"force_block"`
}

// otherConfigCount "其他"分类条目数：按子项合并后求和（preview 阶段展示用）。
func otherConfigCount(data []byte) int {
	var env otherExportEnvelope
	if json.Unmarshal(data, &env) != nil {
		return 0
	}
	return len(env.VolcQuota)
}

// sectionByKey 按 key 查找 section 定义。
func sectionByKey(key string) (sectionInfo, bool) {
	for _, info := range transferSections {
		if info.key == key {
			return info, true
		}
	}
	return sectionInfo{}, false
}

// ==================== 导出 ====================

// 渠道导出结构：api_key 为解密后的明文（omitempty，解密失败或原本为空则缺省）。
type exportChannel struct {
	ID            string                     `json:"id"`
	Name          string                     `json:"name"`
	ChannelName   string                     `json:"channel_name,omitempty"`
	BaseURL       string                     `json:"base_url"`
	APIKey        string                     `json:"api_key,omitempty"`
	ManualEnabled bool                       `json:"manual_enabled"`
	SyncBilling   bool                       `json:"sync_billing"`
	ModelsDetail  []types.ChannelModelDetail `json:"models_detail,omitempty"`
	ModelsError   string                     `json:"models_error,omitempty"`
}

// 聚合模型导出结构：enabled 显式保留（types.AggregateModel 无该字段）。
type exportAggregate struct {
	Name    string                  `json:"name"`
	Enabled *bool                   `json:"enabled,omitempty"`
	Targets []types.AggregateTarget `json:"targets"`
}

// manifest 导出清单。
type transferManifest struct {
	Format     string   `json:"format"`
	Version    int      `json:"version"`
	App        string   `json:"app"`
	ExportedAt string   `json:"exported_at"`
	Sections   []string `json:"sections"`
}

func (s *Service) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sections []string `json:"sections"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// 规范化：去重、过滤未知 key；空列表视为全部。
	selected := map[string]bool{}
	for _, key := range req.Sections {
		if _, ok := sectionByKey(key); ok {
			selected[key] = true
		}
	}
	if len(selected) == 0 {
		for _, info := range transferSections {
			selected[info.key] = true
		}
	}
	var order []string
	for _, info := range transferSections {
		if selected[info.key] {
			order = append(order, info.key)
		}
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := time.Now().UTC()
	manifest := transferManifest{
		Format:     transferFormat,
		Version:    transferVersion,
		App:        "loadout",
		ExportedAt: now.Format(time.RFC3339Nano),
		Sections:   order,
	}
	if err := writeZipEntry(zw, "loadout-config/manifest.json", manifest); err != nil {
		s.writeServerError(w, err)
		return
	}

	for _, key := range order {
		if err := s.writeExportSection(r.Context(), zw, key); err != nil {
			s.writeServerError(w, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.writeServerError(w, err)
		return
	}

	filename := "loadout-config-" + now.Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	_, _ = w.Write(buf.Bytes())
}

// writeExportSection 把一个 section 及其附带文件写入 zip。
func (s *Service) writeExportSection(ctx context.Context, zw *zip.Writer, key string) error {
	switch key {
	case sectionChannels:
		channels, err := s.routing.ListChannels(ctx)
		if err != nil {
			return err
		}
		out := make([]exportChannel, 0, len(channels))
		for _, ch := range channels {
			item := exportChannel{
				ID:            ch.ID,
				Name:          ch.Name,
				ChannelName:   ch.ChannelName,
				BaseURL:       ch.BaseURL,
				ManualEnabled: ch.ManualEnabled,
				SyncBilling:   ch.SyncBilling,
				ModelsError:   ch.ModelsError,
			}
			if ch.APIKeyCipher != "" {
				if plain, err := s.st.Decrypt(ch.APIKeyCipher); err == nil {
					item.APIKey = plain
				}
			}
			for _, model := range ch.Models {
				item.ModelsDetail = append(item.ModelsDetail, types.ChannelModelDetail{
					Model: model.Model, Source: model.Source, Enabled: model.Enabled,
				})
			}
			out = append(out, item)
		}
		return writeZipEntry(zw, "loadout-config/channels.json", out)
	case sectionAggregates:
		aggregates, err := s.routing.ListAggregates(ctx)
		if err != nil {
			return err
		}
		out := make([]exportAggregate, 0, len(aggregates))
		for _, agg := range aggregates {
			targets := make([]types.AggregateTarget, 0, len(agg.Targets))
			for _, t := range agg.Targets {
				targets = append(targets, types.AggregateTarget{Model: t.Model, ChannelID: t.ChannelID, ChannelIDs: t.ChannelIDs, ChannelBaseURL: t.ChannelBaseURL})
			}
			out = append(out, exportAggregate{
				Name:    agg.Name,
				Enabled: boolPtr(agg.Enabled),
				Targets: targets,
			})
		}
		return writeZipEntry(zw, "loadout-config/aggregates.json", out)
	case sectionCapabilityRoutes:
		routes, err := s.readCapabilityRoutes(ctx)
		if err != nil {
			return err
		}
		return writeZipEntry(zw, "loadout-config/capability_routes.json", routes)
	case sectionMCP:
		servers, err := s.readMCPServers(ctx)
		if err != nil {
			return err
		}
		if err := writeZipEntry(zw, "loadout-config/mcp_servers.json", servers); err != nil {
			return err
		}
		groups, err := s.readGroups(ctx)
		if err != nil {
			return err
		}
		if err := writeZipEntry(zw, "loadout-config/mcp_groups.json", groups); err != nil {
			return err
		}
		states, err := s.readToolStates(ctx)
		if err != nil {
			return err
		}
		return writeZipEntry(zw, "loadout-config/mcp_tools_state.json", states)
	case sectionSkills:
		skills, err := s.readSkillsList(ctx)
		if err != nil {
			return err
		}
		if err := writeZipEntry(zw, "loadout-config/skills.json", skills); err != nil {
			return err
		}
		presets, err := s.readPresetsList(ctx)
		if err != nil {
			return err
		}
		if err := writeZipEntry(zw, "loadout-config/presets.json", presets); err != nil {
			return err
		}
		settings, err := s.readSettings(ctx)
		if err != nil {
			return err
		}
		if err := writeZipEntry(zw, "loadout-config/settings.json", settings); err != nil {
			return err
		}
		// 技能真实文件（~/.loadout/skills/<技能名>/...），递归打包，跳过隐藏文件/目录。
		if s.skill != nil {
			if err := writeSkillTreeToZip(zw, "loadout-config/skills", s.skill.RepoDir()); err != nil {
				return err
			}
		}
		return nil
	case sectionOther:
		env, err := s.exportOther(ctx)
		if err != nil {
			return err
		}
		return writeZipEntry(zw, "loadout-config/other.json", env)
	}
	return fmt.Errorf("admin-api: 未知导出分类 %q", key)
}

// exportOther 导出"其他"分类：目前含火山引擎免费额度配置（AK/SK 加密项解为明文，
// 导入时由目标实例用 st.Encrypt 重新加密）。直接读 volc_quota_config 表——
// admin-api 与 volc-free-quota 共享 SQLite 连接，跨插件表读取由 admin-api
// 集中负责导入导出。
func (s *Service) exportOther(ctx context.Context) (otherExportEnvelope, error) {
	var env otherExportEnvelope
	rows, err := s.sqlDB.QueryContext(ctx, `SELECT channel_id, account_id, access_key, secret_key_cipher, enabled, force_block FROM volc_quota_config ORDER BY channel_id`)
	if err != nil {
		return env, fmt.Errorf("admin-api: 读取火山引擎额度配置失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c exportVolcQuotaConfig
		var cipher string
		if err := rows.Scan(&c.ChannelID, &c.AccountID, &c.AccessKey, &cipher, &c.Enabled, &c.ForceBlock); err != nil {
			return env, err
		}
		if cipher != "" {
			if plain, err := s.st.Decrypt(cipher); err == nil {
				c.SecretKey = plain
			}
		}
		env.VolcQuota = append(env.VolcQuota, c)
	}
	return env, rows.Err()
}

// writeSkillTreeToZip 把技能仓库目录（repoDir）下的所有技能目录递归写入 zip 的 base 前缀下。
// 跳过隐藏文件/目录（.git、.DS_Store 等）与符号链接；repoDir 不存在时静默跳过。
func writeSkillTreeToZip(zw *zip.Writer, base, repoDir string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := walkSkillDir(zw, filepath.Join(repoDir, e.Name()), base+"/"+e.Name()); err != nil {
			return fmt.Errorf("admin-api: 打包技能 %q 失败: %w", e.Name(), err)
		}
	}
	return nil
}

// walkSkillDir 递归遍历技能目录并写入 zip（跳过隐藏项与符号链接），保留文件权限位。
func walkSkillDir(zw *zip.Writer, srcDir, zipPrefix string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		src := filepath.Join(srcDir, name)
		dst := zipPrefix + "/" + name
		if e.IsDir() {
			if err := walkSkillDir(zw, src, dst); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: dst, Method: zip.Deflate, Modified: info.ModTime()}
		hdr.SetMode(info.Mode())
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("admin-api: 创建 zip 条目 %s 失败: %w", dst, err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("admin-api: 写入 zip 条目 %s 失败: %w", dst, err)
		}
	}
	return nil
}

func writeZipEntry(zw *zip.Writer, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("admin-api: 序列化 %s 失败: %w", name, err)
	}
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("admin-api: 创建 zip 条目 %s 失败: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("admin-api: 写入 zip 条目 %s 失败: %w", name, err)
	}
	return nil
}

// ==================== 导入预览 ====================

type previewSection struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	File  string `json:"file"`
	Count int    `json:"count"`
}

type importPreview struct {
	Valid      bool             `json:"valid"`
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	ExportedAt string           `json:"exported_at,omitempty"`
	Sections   []previewSection `json:"sections"`
	Unknown    []string         `json:"unknown,omitempty"`
}

func (s *Service) handleConfigImportPreview(w http.ResponseWriter, r *http.Request) {
	files, err := readUploadedZip(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	preview := importPreview{Valid: true}
	if manifest, ok := files["manifest.json"]; ok {
		var m transferManifest
		if json.Unmarshal(manifest.data, &m) == nil {
			preview.Format = m.Format
			preview.Version = m.Version
			preview.ExportedAt = m.ExportedAt
		}
	}
	if preview.Format == "" {
		preview.Format = transferFormat
	}
	for _, info := range transferSections {
		entry, ok := files[info.file]
		if !ok {
			continue
		}
		preview.Sections = append(preview.Sections, previewSection{
			Key: info.key, Name: info.name, File: info.file, Count: info.count(entry.data),
		})
	}
	// 附带文件只在主 section 存在时展示，避免孤立条目。
	hasMCP := files["mcp_servers.json"].data != nil
	hasSkills := files["presets.json"].data != nil
	if hasMCP {
		if entry := files["mcp_groups.json"]; entry.data != nil {
			preview.Sections = append(preview.Sections, previewSection{Key: "mcp_groups", Name: "MCP 分组", File: "mcp_groups.json", Count: jsonArrayCount(entry.data)})
		}
		if entry := files["mcp_tools_state.json"]; entry.data != nil {
			preview.Sections = append(preview.Sections, previewSection{Key: "mcp_tools_state", Name: "工具开关", File: "mcp_tools_state.json", Count: jsonArrayCount(entry.data)})
		}
	}
	if hasSkills {
		if entry := files["skills.json"]; entry.data != nil {
			preview.Sections = append(preview.Sections, previewSection{Key: "skills_list", Name: "技能清单", File: "skills.json", Count: jsonArrayCount(entry.data)})
		}
		if count := countSkillFiles(files); count > 0 {
			preview.Sections = append(preview.Sections, previewSection{Key: "skills_files", Name: "技能文件", File: "skills/<技能名>/", Count: count})
		}
		if entry := files["settings.json"]; entry.data != nil {
			preview.Sections = append(preview.Sections, previewSection{Key: "settings", Name: "运行时设置", File: "settings.json", Count: jsonObjectCount(entry.data)})
		}
	}
	if len(preview.Sections) == 0 {
		preview.Valid = false
		writeError(w, http.StatusBadRequest, "zip 中未发现可识别的 loadout 配置（loadout-config/*.json）")
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// countSkillFiles 统计 zip 内技能文件涉及的不同技能目录数（skills/<name>/...）。
func countSkillFiles(files map[string]zipEntry) int {
	seen := map[string]bool{}
	for path := range files {
		rest, ok := strings.CutPrefix(path, "skills/")
		if !ok {
			continue
		}
		idx := strings.Index(rest, "/")
		if idx <= 0 {
			continue
		}
		seen[rest[:idx]] = true
	}
	return len(seen)
}

// ==================== 导入 ====================

type sectionResult struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Mode     string   `json:"mode"`
	Count    int      `json:"count"`
	Imported int      `json:"imported"`
	Skipped  []string `json:"skipped,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

type importResponse struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Results []sectionResult `json:"results"`
}

func (s *Service) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	files, err := readUploadedZip(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 用户为每个 section 指定的模式（缺省 append，避免误覆盖）。
	modes := map[string]string{}
	if raw := r.FormValue("modes"); raw != "" {
		var parsed map[string]string
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			for key, mode := range parsed {
				if mode == modeOverwrite || mode == modeAppend {
					modes[key] = mode
				}
			}
		}
	}

	resp := importResponse{Format: transferFormat, Version: transferVersion}
	ctx := r.Context()
	for _, info := range transferSections {
		entry, ok := files[info.file]
		if !ok {
			continue
		}
		// 用户未在 modes 中传入该 section（多选对话框取消勾选）→ 跳过，不写入也不汇报。
		mode, selected := modes[info.key]
		if !selected {
			continue
		}
		result, err := s.applySection(ctx, info, entry.data, mode)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			s.lg.Error("配置导入失败", "section", info.key, "err", err)
		}
		resp.Results = append(resp.Results, result)
	}
	// 附带文件：仅在所属主配置被勾选（modes 中有对应 key）时处理。
	if files["mcp_servers.json"].data != nil {
		_, mcpSelected := modes[sectionMCP]
		if mcpSelected {
			for _, pair := range []struct {
				key  string
				file string
				name string
			}{
				{key: "mcp_groups", file: "mcp_groups.json", name: "MCP 分组"},
				{key: "mcp_tools_state", file: "mcp_tools_state.json", name: "工具开关"},
			} {
				entry := files[pair.file]
				if entry.data == nil {
					continue
				}
				mode := modes[pair.key]
				if mode == "" {
					mode = modes[sectionMCP]
				}
				result, err := s.applyExtraFile(ctx, pair.key, pair.name, pair.file, entry.data, mode)
				if err != nil {
					result.Errors = append(result.Errors, err.Error())
					s.lg.Error("配置导入失败", "section", pair.key, "err", err)
				}
				resp.Results = append(resp.Results, result)
			}
		}
	}
	if files["presets.json"].data != nil {
		_, skillsSelected := modes[sectionSkills]
		if skillsSelected {
			for _, pair := range []struct {
				key  string
				file string
				name string
			}{
				{key: "skills_list", file: "skills.json", name: "技能清单"},
				{key: "settings", file: "settings.json", name: "运行时设置"},
			} {
				entry := files[pair.file]
				if entry.data == nil {
					continue
				}
				mode := modes[pair.key]
				if mode == "" {
					mode = modes[sectionSkills]
				}
				result, err := s.applyExtraFile(ctx, pair.key, pair.name, pair.file, entry.data, mode)
				if err != nil {
					result.Errors = append(result.Errors, err.Error())
					s.lg.Error("配置导入失败", "section", pair.key, "err", err)
				}
				resp.Results = append(resp.Results, result)
			}
		}
	}
	// 技能实际文件：先于清单/预设写入，保证磁盘与清单一致。
	if count := countSkillFiles(files); count > 0 {
		_, skillsSelected := modes[sectionSkills]
		if skillsSelected {
			mode := modes["skills_files"]
			if mode == "" {
				mode = modes[sectionSkills]
			}
			result := sectionResult{Key: "skills_files", Name: "技能文件", Count: count, Mode: defaultMode(mode)}
			imported, err := s.importSkillsFiles(files, result.Mode)
			if err != nil {
				result.Errors = append(result.Errors, err.Error())
				s.lg.Error("技能文件导入失败", "err", err)
			} else {
				result.Imported = imported
			}
			resp.Results = append(resp.Results, result)
		}
	}

	// MCP 配置变更后失效索引缓存，让新端点立即生效。
	if s.hub != nil {
		s.hub.Invalidate()
	}
	writeJSON(w, http.StatusOK, resp)
}

// applySection 应用一个主 section（channels / aggregates / capability_routes / mcp / skills）。
func (s *Service) applySection(ctx context.Context, info sectionInfo, data []byte, mode string) (sectionResult, error) {
	result := sectionResult{Key: info.key, Name: info.name, Count: info.count(data), Mode: defaultMode(mode)}
	switch info.key {
	case sectionChannels:
		imported, err := s.importChannels(ctx, data, result.Mode)
		result.Imported = imported
		return result, err
	case sectionAggregates:
		imported, err := s.importAggregates(ctx, data, result.Mode)
		result.Imported = imported
		return result, err
	case sectionCapabilityRoutes:
		imported, err := s.importCapabilityRoutes(data, result.Mode)
		result.Imported = imported
		return result, err
	case sectionMCP:
		imported, err := s.importMCPServers(data, result.Mode)
		result.Imported = imported
		return result, err
	case sectionSkills:
		imported, err := s.importPresets(data, result.Mode)
		result.Imported = imported
		return result, err
	case sectionOther:
		imported, err := s.importOther(ctx, data, result.Mode)
		result.Imported = imported
		return result, err
	}
	return result, fmt.Errorf("admin-api: 未知导入分类 %q", info.key)
}

// importOther 导入"其他"分类：按子项派发到具体 handler（目前仅 volc_quota）。
// 返回成功导入的子项配置条数。
func (s *Service) importOther(ctx context.Context, data []byte, mode string) (int, error) {
	var env otherExportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return 0, fmt.Errorf("admin-api: 解析 other 配置失败: %w", err)
	}
	total := 0
	if len(env.VolcQuota) > 0 {
		n, err := s.importVolcQuotaConfigs(ctx, env.VolcQuota, mode)
		if err != nil {
			return n, err
		}
		total += n
	}
	return total, nil
}

// importVolcQuotaConfigs 导入火山引擎免费额度配置：
//   - SecretKey 非空 → 用 st.Encrypt 重新加密；空 → 保留库内既有密文（与 volc-free-quota
//     SaveConfigs 行为一致：编辑不回显明文时保留原密文）。
//   - channel_id 必须在 channels 表内存在（FK），不存在的直接报错，避免脏数据。
//   - overwrite：删除包内没有的 channel_id 行（PUT 整体替换语义）+ 清理孤儿 volc_quota_usage。
//   - append：包内 channel_id 已存在则覆盖；本地独有 channel_id 保留。
func (s *Service) importVolcQuotaConfigs(ctx context.Context, configs []exportVolcQuotaConfig, mode string) (int, error) {
	if len(configs) == 0 {
		return 0, nil
	}
	// 校验所有 channel_id 必须存在。
	ids := make([]string, 0, len(configs))
	for _, c := range configs {
		if c.ChannelID == "" {
			return 0, errors.New("admin-api: 火山引擎额度配置缺少 channel_id")
		}
		ids = append(ids, c.ChannelID)
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimSuffix(placeholders, ",")
	countArgs := make([]any, len(ids))
	for i, id := range ids {
		countArgs[i] = id
	}
	var known int
	if err := s.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM channels WHERE id IN (`+placeholders+`)`, countArgs...).Scan(&known); err != nil {
		return 0, fmt.Errorf("admin-api: 校验渠道存在失败: %w", err)
	}
	if known != len(ids) {
		return 0, errors.New("火山引擎额度配置中存在不存在的渠道 ID，请先在渠道列表中添加")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	imported := 0
	for _, c := range configs {
		cipher := ""
		if c.SecretKey != "" {
			enc, err := s.st.Encrypt(c.SecretKey)
			if err != nil {
				return imported, fmt.Errorf("admin-api: 加密火山引擎 secret_key 失败: %w", err)
			}
			cipher = enc
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT secret_key_cipher FROM volc_quota_config WHERE channel_id = ?`, c.ChannelID).Scan(&cipher); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return imported, fmt.Errorf("admin-api: 读取既有密文失败: %w", err)
			}
		}
		// account_id 缺失时保留库内已有值；老版本导出的包可能没带该字段。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO volc_quota_config(channel_id, access_key, account_id, secret_key_cipher, enabled, force_block, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(channel_id) DO UPDATE SET
				access_key = excluded.access_key,
				account_id = CASE WHEN excluded.account_id != '' THEN excluded.account_id ELSE volc_quota_config.account_id END,
				secret_key_cipher = CASE WHEN excluded.secret_key_cipher != '' THEN excluded.secret_key_cipher ELSE volc_quota_config.secret_key_cipher END,
				enabled = excluded.enabled,
				force_block = excluded.force_block,
				updated_at = excluded.updated_at`,
			c.ChannelID, c.AccessKey, c.AccountID, cipher, c.Enabled, c.ForceBlock, now); err != nil {
			return imported, fmt.Errorf("admin-api: upsert volc_quota_config 失败: %w", err)
		}
		imported++
	}

	if mode == modeOverwrite {
		// overwrite：删除包内没有的 channel_id 行（PUT 整体替换语义）。
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_config WHERE channel_id NOT IN (`+placeholders+`)`, args...); err != nil {
			return imported, err
		}
		// 清理孤儿使用记录（被删除的账号不再有 quota 追踪）。
		if _, err := tx.ExecContext(ctx, `DELETE FROM volc_quota_usage WHERE account_id NOT IN (SELECT DISTINCT account_id FROM volc_quota_config WHERE account_id != '')`); err != nil {
			return imported, err
		}
	}
	return imported, tx.Commit()
}

// applyExtraFile 处理附带文件（mcp_groups / mcp_tools_state / skills_list / settings）。
func (s *Service) applyExtraFile(ctx context.Context, key, name, file string, data []byte, mode string) (sectionResult, error) {
	result := sectionResult{Key: key, Name: name, Count: len(data), Mode: defaultMode(mode)}
	switch key {
	case "mcp_groups":
		imported, err := s.importGroups(data, result.Mode)
		result.Imported = imported
		result.Count = jsonArrayCount(data)
		return result, err
	case "mcp_tools_state":
		imported, err := s.importToolsState(data, result.Mode)
		result.Imported = imported
		result.Count = jsonArrayCount(data)
		return result, err
	case "skills_list":
		// 技能真实文件已随包导入，清单与磁盘一致，直接写回（append 按 name 去重保留本地条目）。
		var values []types.Skill
		if err := json.Unmarshal(data, &values); err != nil {
			return result, fmt.Errorf("admin-api: 解析 %s 失败: %w", file, err)
		}
		result.Count = len(values)
		if result.Mode == modeAppend {
			existing, err := s.readSkillsList(ctx)
			if err != nil {
				return result, err
			}
			values = dedupeBy(append(values, existing...), func(sk types.Skill) string { return sk.Name })
		}
		if err := s.writeSkillsList(ctx, values); err != nil {
			return result, err
		}
		result.Imported = len(values)
		return result, nil
	case "settings":
		if result.Mode == modeAppend {
			result.Skipped = append(result.Skipped, "追加模式不覆盖运行时设置")
			return result, nil
		}
		var settings types.Settings
		if err := json.Unmarshal(data, &settings); err != nil {
			return result, fmt.Errorf("admin-api: 解析 %s 失败: %w", file, err)
		}
		if err := s.writeSettings(ctx, settings); err != nil {
			return result, err
		}
		result.Imported = 1
		return result, nil
	}
	return result, fmt.Errorf("admin-api: 未知附带文件 %q", key)
}

// ==================== 各 section 写入逻辑 ====================

// importChannels 导入渠道：api_key 明文用当前密钥重新加密。
func (s *Service) importChannels(ctx context.Context, data []byte, mode string) (int, error) {
	var values []exportChannel
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析渠道配置失败: %w", err)
	}
	existing, err := s.routing.ListChannels(ctx)
	if err != nil {
		return 0, err
	}
	byID := map[string]bool{}
	merged := make([]db.Channel, 0, len(values))
	for _, item := range values {
		if item.ID == "" || item.Name == "" || item.BaseURL == "" {
			return 0, fmt.Errorf("admin-api: 渠道 %q 缺少 id/name/base_url", item.Name)
		}
		if byID[item.ID] {
			return 0, fmt.Errorf("admin-api: 渠道 id 重复 %q", item.ID)
		}
		byID[item.ID] = true
		cipher := ""
		if item.APIKey != "" {
			cipher, err = s.st.Encrypt(item.APIKey)
			if err != nil {
				return 0, fmt.Errorf("admin-api: 加密渠道 %q 密钥失败: %w", item.Name, err)
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		channel := db.Channel{
			ID: item.ID, Name: item.Name, ChannelName: item.ChannelName, BaseURL: item.BaseURL,
			APIKeyCipher: cipher, ManualEnabled: item.ManualEnabled,
			SyncBilling: item.SyncBilling, ModelsError: item.ModelsError,
			CreatedAt: now, UpdatedAt: now,
		}
		for _, model := range item.ModelsDetail {
			channel.Models = append(channel.Models, db.ChannelModel{
				Model: model.Model, Source: model.Source, Enabled: model.Enabled,
				FirstSeenAt: now, LastSeenAt: now,
			})
		}
		merged = append(merged, channel)
	}
	if mode == modeAppend {
		// 追加：现有渠道 + 导入渠道（同名 id 以导入为准）。
		for _, ch := range existing {
			if !byID[ch.ID] {
				merged = append(merged, ch)
			}
		}
	}
	// 渠道名称兜底：空渠道名继承同 Base URL 组内首个非空渠道名（与创建/更新逻辑一致，
	// 保证"同组同步一致"）；全组为空时回退为 Key 名，避免导入后渠道名缺失。
	fillChannelNames(merged)
	if err := s.routing.ReplaceChannels(ctx, merged); err != nil {
		return 0, err
	}
	return len(values), nil
}

// fillChannelNames 补齐空渠道名：同 Base URL 组内继承首个非空 ChannelName；
// 组内全空则回退为各自 Name。
func fillChannelNames(channels []db.Channel) {
	for i := range channels {
		if channels[i].ChannelName != "" {
			continue
		}
		base := strings.TrimRight(channels[i].BaseURL, "/")
		for j := range channels {
			if channels[j].ID != channels[i].ID && strings.TrimRight(channels[j].BaseURL, "/") == base && channels[j].ChannelName != "" {
				channels[i].ChannelName = channels[j].ChannelName
				break
			}
		}
		if channels[i].ChannelName == "" {
			channels[i].ChannelName = channels[i].Name
		}
	}
}

// importAggregates 导入聚合模型。
func (s *Service) importAggregates(ctx context.Context, data []byte, mode string) (int, error) {
	var values []exportAggregate
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析聚合模型配置失败: %w", err)
	}
	existing, err := s.routing.ListAggregates(ctx)
	if err != nil {
		return 0, err
	}
	byName := map[string]bool{}
	merged := make([]db.Aggregate, 0, len(values))
	for _, item := range values {
		if item.Name == "" {
			return 0, errors.New("admin-api: 聚合模型缺少 name")
		}
		if byName[item.Name] {
			return 0, fmt.Errorf("admin-api: 聚合模型 name 重复 %q", item.Name)
		}
		byName[item.Name] = true
		enabled := len(item.Targets) > 0
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		targets := make([]db.AggregateTarget, 0, len(item.Targets))
		for _, t := range item.Targets {
			targets = append(targets, db.AggregateTarget{Model: t.Model, ChannelID: t.ChannelID, ChannelIDs: t.ChannelIDs, ChannelBaseURL: t.ChannelBaseURL})
		}
		merged = append(merged, db.Aggregate{Name: item.Name, Enabled: enabled, Targets: targets})
	}
	if mode == modeAppend {
		for _, agg := range existing {
			if !byName[agg.Name] {
				merged = append(merged, agg)
			}
		}
	}
	if err := s.routing.ReplaceAggregates(ctx, merged); err != nil {
		return 0, err
	}
	return len(values), nil
}

// importCapabilityRoutes 导入能力路由（DB 优先，fallback JSON 文件，无渠道依赖）。
// append 模式以 (models+capability+channel_ids) 为键去重，导入条目优先。
func (s *Service) importCapabilityRoutes(data []byte, mode string) (int, error) {
	var values []types.CapabilityRoute
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析能力路由配置失败: %w", err)
	}
	if mode == modeAppend {
		existing, err := s.readCapabilityRoutes(context.Background())
		if err != nil {
			return 0, err
		}
		values = dedupeBy(append(values, existing...), capabilityRouteKey)
	}
	if err := s.writeCapabilityRoutes(context.Background(), values); err != nil {
		return 0, err
	}
	return len(values), nil
}

// capabilityRouteKey 能力路由的去重键：models + capability + channel_ids（顺序无关）。
func capabilityRouteKey(r types.CapabilityRoute) string {
	models := append([]string(nil), r.Models...)
	channels := append([]string(nil), r.ChannelIDs...)
	sort.Strings(models)
	sort.Strings(channels)
	return strings.Join(models, ",") + "\x00" + r.Capability + "\x00" + strings.Join(channels, ",")
}

// importMCPServers 导入 MCP 服务器列表（按 id 去重合并，导入条目优先）。
func (s *Service) importMCPServers(data []byte, mode string) (int, error) {
	var values []types.MCPServer
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析 MCP 配置失败: %w", err)
	}
	if mode == modeAppend {
		existing, err := s.readMCPServers(context.Background())
		if err != nil {
			return 0, err
		}
		values = dedupeBy(append(values, existing...), func(srv types.MCPServer) string { return srv.ID })
	}
	if err := s.writeMCPServers(context.Background(), values); err != nil {
		return 0, err
	}
	return len(values), nil
}

// importGroups 导入 MCP 分组（按 name 去重合并，导入条目优先）。
func (s *Service) importGroups(data []byte, mode string) (int, error) {
	var values []types.Group
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析 MCP 分组失败: %w", err)
	}
	if mode == modeAppend {
		existing, err := s.readGroups(context.Background())
		if err != nil {
			return 0, err
		}
		values = dedupeBy(append(values, existing...), func(g types.Group) string { return g.Name })
	}
	if err := s.writeGroups(context.Background(), values); err != nil {
		return 0, err
	}
	return len(values), nil
}

// importToolsState 导入工具开关（按 server_id+tool_name 去重合并，导入条目优先）。
func (s *Service) importToolsState(data []byte, mode string) (int, error) {
	var values []types.ToolState
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析工具开关失败: %w", err)
	}
	if mode == modeAppend {
		existing, err := s.readToolStates(context.Background())
		if err != nil {
			return 0, err
		}
		values = dedupeBy(append(values, existing...), func(ts types.ToolState) string {
			return ts.ServerID + "\x00" + ts.ToolName
		})
	}
	if err := s.writeToolStates(context.Background(), values); err != nil {
		return 0, err
	}
	return len(values), nil
}

// importPresets 导入技能预设（按 name 去重合并，导入条目优先）。
func (s *Service) importPresets(data []byte, mode string) (int, error) {
	var values []types.Preset
	if err := json.Unmarshal(data, &values); err != nil {
		return 0, fmt.Errorf("admin-api: 解析技能预设失败: %w", err)
	}
	if mode == modeAppend {
		existing, err := s.readPresetsList(context.Background())
		if err != nil {
			return 0, err
		}
		values = dedupeBy(append(values, existing...), func(p types.Preset) string { return p.Name })
	}
	if err := s.writePresetsList(context.Background(), values); err != nil {
		return 0, err
	}
	return len(values), nil
}

// dedupeBy 按键函数去重，保留首个出现（输入顺序稳定）。
// 用于追加合并：先导入条目后现有条目，因此同键时导入条目优先。
func dedupeBy[T any](items []T, key func(T) string) []T {
	seen := map[string]bool{}
	out := items[:0]
	for _, item := range items {
		k := key(item)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, item)
	}
	return out
}

// importSkillsFiles 把 zip 内 skills/<技能名>/... 的真实文件写入技能仓库目录。
// overwrite：先清空仓库内现有技能目录再写入；append：按技能名覆盖合并。
// 返回成功导入的技能数。含 zip slip 越界防护与解压总量上限；文件权限位随条目保留。
func (s *Service) importSkillsFiles(files map[string]zipEntry, mode string) (int, error) {
	// 按技能名分组收集文件（相对路径）。
	bySkill := map[string]map[string]zipEntry{}
	for path, entry := range files {
		rest, ok := strings.CutPrefix(path, "skills/")
		if !ok {
			continue
		}
		idx := strings.Index(rest, "/")
		if idx <= 0 {
			continue
		}
		name := rest[:idx]
		rel := rest[idx+1:]
		if bySkill[name] == nil {
			bySkill[name] = map[string]zipEntry{}
		}
		bySkill[name][rel] = entry
	}
	if len(bySkill) == 0 {
		return 0, nil
	}
	if s.skill == nil {
		return 0, errors.New("admin-api: skills 服务未装配，无法导入技能文件")
	}
	repoDir := s.skill.RepoDir()
	if repoDir == "" {
		return 0, errors.New("admin-api: 技能仓库目录为空")
	}

	if mode == modeOverwrite {
		entries, err := os.ReadDir(repoDir)
		if err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("admin-api: 读取技能仓库失败: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				if err := os.RemoveAll(filepath.Join(repoDir, e.Name())); err != nil {
					return 0, fmt.Errorf("admin-api: 清空技能 %q 失败: %w", e.Name(), err)
				}
			}
		}
	}

	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return 0, fmt.Errorf("admin-api: 创建技能仓库目录失败: %w", err)
	}
	var total int64
	imported := 0
	for name, skillFiles := range bySkill {
		dst := filepath.Join(repoDir, name)
		if err := os.RemoveAll(dst); err != nil {
			return 0, fmt.Errorf("admin-api: 清理技能 %q 目录失败: %w", name, err)
		}
		for rel, entry := range skillFiles {
			clean := filepath.Clean(filepath.FromSlash(rel))
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return 0, fmt.Errorf("admin-api: 技能 %q 内路径越界: %s", name, rel)
			}
			target := filepath.Join(dst, clean)
			if target != dst && !strings.HasPrefix(target, dst+string(filepath.Separator)) {
				return 0, fmt.Errorf("admin-api: 技能 %q 内路径越界: %s", name, rel)
			}
			total += int64(len(entry.data))
			if total > maxExtractSkillBytes {
				return 0, fmt.Errorf("admin-api: 技能文件解压后超过 %d MiB 上限", maxExtractSkillBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return 0, fmt.Errorf("admin-api: 创建技能文件目录失败: %w", err)
			}
			perm := entry.mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			if err := os.WriteFile(target, entry.data, perm); err != nil {
				return 0, fmt.Errorf("admin-api: 写入技能文件 %s 失败: %w", target, err)
			}
		}
		imported++
	}
	return imported, nil
}

// ==================== 工具函数 ====================

func defaultMode(mode string) string {
	if mode == modeOverwrite {
		return modeOverwrite
	}
	return modeAppend
}

func boolPtr(v bool) *bool { return &v }

// readUploadedZip 读取上传的 zip，返回按完整相对路径索引的内容 map：
// 键为去掉 zipRootPrefix（loadout-config/）后的路径，如 "channels.json"、
// "skills/git-tools/SKILL.md"；zip 内无统一包裹目录时保留原路径。
// 同名顶层文件（如多个 skills/*/SKILL.md）互不覆盖。
// 越界路径条目（绝对路径 / ".." 逃逸）被直接跳过；文件权限位随条目保留。
func readUploadedZip(w http.ResponseWriter, r *http.Request) (map[string]zipEntry, error) {
	if err := r.ParseMultipartForm(maxImportZipSize + 1<<20); err != nil {
		return nil, errors.New("请求体过大或不是 multipart 表单")
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, errors.New("缺少 file 字段")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxImportZipSize+1))
	if err != nil {
		return nil, errors.New("读取上传文件失败")
	}
	if len(raw) > maxImportZipSize {
		return nil, errors.New("zip 文件超过 256MB 上限")
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, errors.New("无法解析 zip 文件（不是合法的 zip 包）")
	}
	files := map[string]zipEntry{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		slash := filepath.ToSlash(f.Name)
		rel := strings.TrimPrefix(slash, zipRootPrefix)
		rel = strings.TrimPrefix(rel, "/")
		// 拒绝绝对路径与 ".." 逃逸条目（zip slip）。
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			continue
		}
		if rel == "" || rel == "." {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxImportZipSize))
		rc.Close()
		if err != nil {
			continue
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		if _, exists := files[rel]; !exists {
			files[rel] = zipEntry{data: data, mode: mode}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("zip 中没有文件")
	}
	return files, nil
}

// zipEntry zip 内一个文件的完整内容与权限位。
type zipEntry struct {
	data []byte
	mode fs.FileMode
}
