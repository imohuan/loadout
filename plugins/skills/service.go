package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loadout/core/cmdutil"
	"loadout/core/config"
	"loadout/core/deps"
	"loadout/core/db"
	"loadout/core/linkfs"
	"loadout/core/procreg"
	"loadout/core/store"
	"loadout/plugins/types"
)

// MaxZipBytes zip 导入的读取上限（防超大上传耗尽内存）。
const MaxZipBytes = 32 << 20 // 32 MiB

// maxExtractBytes zip 解压后的总字节上限（防 zip 炸弹耗尽磁盘）。
const maxExtractBytes = 64 << 20 // 64 MiB

// runCommand 执行外部命令并返回合并输出。包级变量，测试可替换为 fake。
var runCommand = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmdutil.HideWindow(cmd) // 桌面 exe 下不弹黑色终端框
	out, err := cmd.CombinedOutput()
	return string(out), err
}
// skillsCmd 返回 skills CLI 入口：skills 已全局安装 且 开关打开 → 全局 skills 指令；
// 否则回退 npx 字面量（原行为，由 exec 在 PATH 中解析）。
func skillsCmd() (string, []string) {
	// 依赖开关：skills 已全局安装 且 开关打开 → 用全局 skills 指令；
	// 否则回退 npx（npx -y skills 自动拉取）。
	if deps.UseGlobal && deps.GlobalAvailable("skills") {
		return "skills", []string{"update", "-y"}
	}
	return "npx", []string{"-y", "skills", "update", "-y"}
}

// manifestName 目标目录里 Loadout 自建的链接条目清单文件名。
const manifestName = ".loadout-manifest.json"

// Service 技能仓库 + 预设管理服务。
type Service struct {
	st           *store.Store
	lg           *slog.Logger
	repo         *db.Repository      // SQLite 仓储（skills/presets/settings 持久化）
	repoDir      string              // 技能库目录（~/.loadout/skills，全部技能，永不删除）
	targetDir    string              // 通用目标目录（~/.agents/skills）
	platformDirs map[string]string   // 指定平台 → 其技能目录（codex/claudecode/opencode…）
	updater      *UpdateRunner       // 更新任务广播器（SSE 日志流）
}

// NewService 创建服务。repoDir/targetDir 为空时用 config.SkillsDir / config.ResolveAgentSkillsDir()。
func NewService(st *store.Store, lg *slog.Logger, repoDir, targetDir string) *Service {
	if repoDir == "" {
		repoDir = config.SkillsDir
	}
	if targetDir == "" {
		targetDir = config.ResolveAgentSkillsDir()
	}
	svc := &Service{
		st:           st,
		lg:           lg,
		repoDir:      repoDir,
		targetDir:    targetDir,
		platformDirs: defaultPlatformDirs(),
	}
	svc.updater = newUpdateRunner(svc)
	return svc
}

// SetRepository 注入 SQLite 仓储（由装配层在 db 就绪后调用；测试可省略）。
func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// SubscribeUpdate 订阅更新任务日志流（SSE 用）。
// UpdateRunning 返回是否有更新任务正在跑（前端入口自动显示更新日志 Tab 用）。
func (s *Service) UpdateRunning() bool { return s.updater.IsRunning() }

func (s *Service) SubscribeUpdate() (<-chan UpdateEvent, error) {
	return s.updater.Subscribe()
}

// SetUpdateID 设置下一次更新任务的进程 ID（前端 task id）。
func (s *Service) SetUpdateID(id string) { s.updater.SetUpdateID(id) }

// RepoDir 返回技能仓库目录（所有技能真实文件所在，~/.loadout/skills）。
func (s *Service) RepoDir() string { return s.repoDir }

// defaultPlatformDirs 返回内置平台 → 技能目录映射（以用户主目录展开）。
// 目前内置 codex / claudecode / opencode，后续可扩展。
func defaultPlatformDirs() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	return map[string]string{
		"codex":      filepath.Join(home, ".codex", "skills"),
		"claudecode": filepath.Join(home, ".claude", "skills"),
		"opencode":   filepath.Join(home, ".opencode", "skills"),
	}
}

// targetDirFor 解析预设目标：空（通用）→ 通用目录；平台名 → 平台目录。
// 未知平台返回空串。
func (s *Service) targetDirFor(target string) string {
	if target == "" {
		return s.targetDir
	}
	if dir, ok := s.platformDirs[target]; ok {
		return dir
	}
	return ""
}

// backupDirFor 返回指定目标的备份目录（目标目录 + "-backup"）。
func backupDirFor(dir string) string {
	return dir + "-backup"
}

// ===== 仓库清单 =====

// scanSkills 扫描目录下的技能：含 SKILL.md 且能解析 frontmatter 的子目录即为技能，
// 返回 [{name, description}]（按名称排序）。目录不存在时返回空列表。
// 技能列表接口与更新流程共用的抽象。
func scanSkills(dir string) []types.Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	skills := make([]types.Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		mdPath := filepath.Join(dir, name, "SKILL.md")
		if _, err := os.Stat(mdPath); err != nil {
			continue // 没有 SKILL.md 的目录不算技能
		}
		skills = append(skills, parseSkillFrontmatter(name, mdPath))
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// skillNameSet 提取技能列表的名称集合。
func skillNameSet(skills []types.Skill) map[string]bool {
	set := make(map[string]bool, len(skills))
	for _, sk := range skills {
		set[sk.Name] = true
	}
	return set
}

// List 扫描技能库目录返回技能清单：每个含 SKILL.md 的子目录是一个技能，
// name/description 从 SKILL.md 的 YAML frontmatter 解析；source/version/installed_at
// 从 skills.json 登记信息补充（未登记过的技能这些字段为空）；updated_at 来自
// .skill-lock.json。按名称排序。
func (s *Service) List() ([]types.Skill, error) {
	registered, err := s.readSkills()
	if err != nil {
		return nil, err
	}
	regMap := make(map[string]types.Skill, len(registered))
	for _, r := range registered {
		regMap[r.Name] = r
	}
	// lock 里的更新时间（供 UI 显示"3 小时前更新"等）。
	lockEntries, _ := readLockEntries(s.targetDir)

	skills := scanSkills(s.repoDir)
	if skills == nil {
		skills = []types.Skill{}
	}
	for i := range skills {
		sk := &skills[i]
		if r, ok := regMap[sk.Name]; ok {
			sk.Source = r.Source
			sk.Version = r.Version
			sk.InstalledAt = r.InstalledAt
		}
		if e, ok := lockEntries[sk.Name]; ok {
			sk.UpdatedAt = e.UpdatedAt
		}
	}
	return skills, nil
}

// parseSkillFrontmatter 读取 SKILL.md 的 YAML frontmatter（--- 包裹的头块），
// 解析 name 与 description；无 frontmatter、解析失败或缺 name 时回退到目录名与空描述。
func parseSkillFrontmatter(dirName, mdPath string) types.Skill {
	sk := types.Skill{Name: dirName}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return sk
	}
	raw := string(data)
	if !strings.HasPrefix(raw, "---") {
		return sk
	}
	rest := raw[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return sk
	}
	block := rest[:endIdx]

	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(block), &meta); err != nil {
		return sk
	}
	if meta.Name != "" {
		sk.Name = strings.TrimSpace(meta.Name)
	}
	sk.Description = strings.TrimSpace(meta.Description)
	return sk
}

// Register 安装后把技能登记进 skills.json；同名技能更新来源/版本并刷新安装时间。
func (s *Service) Register(name, source, version string) error {
	skills, err := s.readSkills()
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := range skills {
		if skills[i].Name == name {
			skills[i].Source = source
			skills[i].Version = version
			skills[i].InstalledAt = now
			return s.writeSkills(skills)
		}
	}

	skills = append(skills, types.Skill{
		Name:        name,
		Source:      source,
		InstalledAt: now,
		Version:     version,
	})
	return s.writeSkills(skills)
}

// Remove 删除技能：移除技能库（repoDir）里对应目录 + 从 skills.json 移除登记。
// 目录不存在时仅移除登记，静默成功。
func (s *Service) Remove(name string) error {
	if err := validSkillName(name); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.repoDir, name)); err != nil {
		return fmt.Errorf("skills: 删除技能目录失败: %w", err)
	}

	skills, err := s.readSkills()
	if err != nil {
		return err
	}

	out := skills[:0]
	for _, sk := range skills {
		if sk.Name != name {
			out = append(out, sk)
		}
	}
	return s.writeSkills(out)
}

// ===== 预设 =====

// ListPresets 读 presets.json 返回全部技能预设；文件不存在时返回空清单。
func (s *Service) ListPresets() ([]types.Preset, error) {
	presets, err := s.readPresets()
	if err != nil {
		return nil, err
	}
	if presets == nil {
		presets = []types.Preset{}
	}
	return presets, nil
}

// CreatePreset 创建（或同名覆盖）一个技能预设。targets 为空表示通用（~/.agents/skills），
// 否则为平台名列表（codex/claudecode/opencode…），可多选同时部署到多个平台；
// 未知平台直接拒绝，避免切换时静默失败。
func (s *Service) CreatePreset(name string, skillNames, targets []string) error {
	for _, t := range targets {
		if t != "" && s.targetDirFor(t) == "" {
			return fmt.Errorf("skills: 未知平台: %s（可用: 通用/空、codex、claudecode、opencode）", t)
		}
	}

	presets, err := s.readPresets()
	if err != nil {
		return err
	}

	for i := range presets {
		if presets[i].Name == name {
			presets[i].Skills = skillNames
			presets[i].Targets = targets
			presets[i].Target = ""
			return s.writePresets(presets)
		}
	}

	presets = append(presets, types.Preset{Name: name, Skills: skillNames, Targets: targets})
	return s.writePresets(presets)
}

// DeletePreset 删除指定预设；不存在时静默成功。
func (s *Service) DeletePreset(name string) error {
	presets, err := s.readPresets()
	if err != nil {
		return err
	}

	out := presets[:0]
	for _, p := range presets {
		if p.Name != name {
			out = append(out, p)
		}
	}
	return s.writePresets(out)
}

// ActivePreset 读 settings.active_preset；文件不存在时返回空字符串。
func (s *Service) ActivePreset() (string, error) {
	settings, err := s.readSettings()
	if err != nil {
		return "", err
	}
	return settings.ActivePreset, nil
}

// SetActivePreset 写 settings.active_preset。
func (s *Service) SetActivePreset(name string) error {
	settings, err := s.readSettings()
	if err != nil {
		return err
	}
	settings.ActivePreset = name
	return s.writeSettings(settings)
}

// ===== 预设切换（核心：备份 + 复制式）=====

// ApplyPreset 按预设部署技能到目标目录（通用和/或指定平台，可多平台）：
//  1. 解析目标目录列表（预设.TargetList：空=通用 ~/.agents/skills；否则各平台目录）；
//  2. 备份：目标目录存在 → 删除旧备份（如有）→ 重命名为 <目录>-backup；
//  3. 新建目标目录；
//  4. 从技能库（repoDir）把预设选中的技能复制到每个目标目录（缺失的技能跳过并记录）；
//  5. 写回 manifest（记录本次部署的条目，供 UI 展示）；
//  6. 记录当前生效预设及其目标平台。
//
// 指定平台时，通用目录（~/.agents/skills）同样会重命名备份——保证切到平台预设后
// 通用目录不残留旧技能。目标目录不存在时自动创建。
func (s *Service) ApplyPreset(name string) error {
	presets, err := s.readPresets()
	if err != nil {
		return err
	}

	var preset *types.Preset
	for i := range presets {
		if presets[i].Name == name {
			preset = &presets[i]
			break
		}
	}
	if preset == nil {
		return fmt.Errorf("skills: 预设不存在: %s", name)
	}

	targets := preset.TargetList()
	if len(targets) == 0 {
		targets = []string{""}
	}

	// 部署目录：每个目标平台各一个；通用目录若未在选中列表中，仅作备份重建（清空旧技能）。
	var dirs []string
	for _, t := range targets {
		d := s.targetDirFor(t)
		if d == "" {
			return fmt.Errorf("skills: 预设 %s 的目标平台无效: %q", name, t)
		}
		dirs = append(dirs, d)
	}
	backupDirs := append([]string{}, dirs...)
	if !containsStr(backupDirs, s.targetDir) {
		backupDirs = append(backupDirs, s.targetDir)
	}
	for _, d := range uniqueStrs(backupDirs) {
		if err := s.backupAndRebuild(d); err != nil {
			return err
		}
	}

	// 从技能库复制选中的技能到每个目标目录，只记录创建成功的条目。
	for _, d := range uniqueStrs(dirs) {
		created := make([]string, 0, len(preset.Skills))
		for _, skillName := range preset.Skills {
			src := filepath.Join(s.repoDir, skillName)
			fi, err := os.Stat(src)
			if err != nil || !fi.IsDir() {
				s.warn("skills: 技能库缺少技能目录，跳过", "skill", skillName)
				continue
			}
			dst := filepath.Join(d, skillName)
			if err := copyTree(src, dst); err != nil {
				s.warn("skills: 复制技能失败，跳过", "skill", skillName, "error", err)
				continue
			}
			created = append(created, skillName)
		}
		if err := writeManifest(filepath.Join(d, manifestName), created); err != nil {
			return err
		}
	}

	return s.activatePreset(name, targets)
}

// containsStr 判断切片是否包含目标字符串。
func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// uniqueStrs 去重并保持顺序。
func uniqueStrs(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, x := range list {
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// backupAndRebuild 备份目录并重建：存在旧备份先删除；目录存在且非空才重命名
// <dir>-backup（空目录/不存在直接跳过，避免产生无意义的空备份）；再新建空目录。
func (s *Service) backupAndRebuild(dir string) error {
	backup := backupDirFor(dir)

	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("skills: 清理旧备份 %s 失败: %w", backup, err)
	}

	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		if empty, err := isDirEmpty(dir); err == nil && !empty {
			if err := os.Rename(dir, backup); err != nil {
				return fmt.Errorf("skills: 备份目录 %s 失败: %w", dir, err)
			}
			s.warn("skills: 目标目录已备份", "dir", dir, "backup", backup)
		}
	}

	if err := linkfs.EnsureDir(dir); err != nil {
		return fmt.Errorf("skills: 创建目标目录失败: %w", err)
	}
	return nil
}

// isDirEmpty 判断目录是否没有任何条目（含隐藏项）。
func isDirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// activatePreset 写 settings 的 active_preset 与目标平台。
// ActivePresetTargets 存完整列表；ActivePresetTarget 存逗号连接串兼容旧前端/旧数据。
func (s *Service) activatePreset(name string, targets []string) error {
	settings, err := s.readSettings()
	if err != nil {
		return err
	}
	settings.ActivePreset = name
	settings.ActivePresetTargets = targets
	settings.ActivePresetTarget = strings.Join(targets, ",")
	return s.writeSettings(settings)
}

// ===== 备份恢复 =====

// RestoreBackup 恢复指定目标（空=通用，或平台名）的备份：
// 删除当前技能目录 → 把 <目录>-backup 重命名回原名。无备份时返回错误。
// 用于还原预设切换产生的备份（更新流程不产生备份，直接替换旧版本）。
func (s *Service) RestoreBackup(target string) error {
	dir := s.targetDirFor(target)
	if dir == "" {
		return fmt.Errorf("skills: 未知平台: %q", target)
	}
	backup := backupDirFor(dir)

	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("skills: %s 没有备份目录（%s）", dir, backup)
	}

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("skills: 删除当前目录 %s 失败: %w", dir, err)
	}
	if err := os.Rename(backup, dir); err != nil {
		return fmt.Errorf("skills: 恢复备份 %s 失败: %w", backup, err)
	}
	s.warn("skills: 已恢复备份", "dir", dir, "backup", backup)
	return nil
}

// RestoreAllBackups 对所有目标（通用 + 各平台）执行恢复；无备份的目标跳过。
func (s *Service) RestoreAllBackups() []string {
	restored := []string{}
	targets := []string{""}
	for name := range s.platformDirs {
		targets = append(targets, name)
	}
	for _, target := range targets {
		if err := s.RestoreBackup(target); err == nil {
			restored = append(restored, target)
		}
	}
	return restored
}

// ===== 平台与备份状态 =====

// PlatformStatus 单个目标（通用或平台）的技能数量与备份状态。
type PlatformStatus struct {
	Name      string `json:"name"`       // 平台名：""=通用
	Dir       string `json:"dir"`        // 技能目录
	Count     int    `json:"count"`      // 一级技能目录数
	HasBackup bool   `json:"has_backup"` // 是否存在 <目录>-backup
}

// Status 返回通用 + 各平台的技能数量与备份状态，供 UI 展示与恢复按钮渲染。
// 平台列表：通用 + 全部已注册平台；目录不存在按数量 0 处理。
func (s *Service) Status() []PlatformStatus {
	names := []string{""}
	for name := range s.platformDirs {
		names = append(names, name)
	}

	out := make([]PlatformStatus, 0, len(names))
	for _, name := range names {
		dir := s.targetDirFor(name)
		count, _ := countSkillDirs(dir)
		_, backupErr := os.Stat(backupDirFor(dir))
		out = append(out, PlatformStatus{
			Name:      name,
			Dir:       dir,
			Count:     count,
			HasBackup: backupErr == nil,
		})
	}
	return out
}

// countSkillDirs 统计目录下的一级技能目录数量（目录不存在返回 0）。
func countSkillDirs(dir string) (int, error) {
	names, err := listSkillDirs(dir)
	if err != nil {
		return 0, err
	}
	return len(names), nil
}

// readSkills 读技能清单（SQLite 优先，fallback skills.json）；文件不存在视为空清单。
func (s *Service) readSkills() ([]types.Skill, error) {
	if s.repo != nil {
		skills, err := s.repo.ListSkills(context.Background())
		if err == nil {
			return skills, nil
		}
		s.warn("skills: 从 SQLite 读技能清单失败，回退 JSON", "err", err)
	}
	var skills []types.Skill
	if err := s.st.Read(types.FileSkills, &skills); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return skills, nil
}

// writeSkills 写技能清单（SQLite 优先，fallback skills.json）。
func (s *Service) writeSkills(skills []types.Skill) error {
	if s.repo != nil {
		if err := s.repo.ReplaceSkills(context.Background(), skills); err == nil {
			return nil
		} else {
			s.warn("skills: 写技能清单到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileSkills, skills)
}

// readPresets 读预设清单（SQLite 优先，fallback presets.json）；文件不存在视为空清单。
func (s *Service) readPresets() ([]types.Preset, error) {
	if s.repo != nil {
		presets, err := s.repo.ListPresets(context.Background())
		if err == nil {
			return presets, nil
		}
		s.warn("skills: 从 SQLite 读预设清单失败，回退 JSON", "err", err)
	}
	var presets []types.Preset
	if err := s.st.Read(types.FilePresets, &presets); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return presets, nil
}

// writePresets 写预设清单（SQLite 优先，fallback presets.json）。
func (s *Service) writePresets(presets []types.Preset) error {
	if s.repo != nil {
		if err := s.repo.ReplacePresets(context.Background(), presets); err == nil {
			return nil
		} else {
			s.warn("skills: 写预设清单到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FilePresets, presets)
}

// readSettings 读运行时设置（SQLite 优先，fallback settings.json）。
func (s *Service) readSettings() (types.Settings, error) {
	if s.repo != nil {
		settings, err := s.repo.GetSettings(context.Background())
		if err == nil {
			return settings, nil
		}
		s.warn("skills: 从 SQLite 读设置失败，回退 JSON", "err", err)
	}
	var settings types.Settings
	if err := s.st.Read(types.FileSettings, &settings); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return types.Settings{}, nil
		}
		return types.Settings{}, err
	}
	return settings, nil
}

// writeSettings 写运行时设置（SQLite 优先，fallback settings.json）。
func (s *Service) writeSettings(settings types.Settings) error {
	if s.repo != nil {
		if err := s.repo.PutSettings(context.Background(), settings); err == nil {
			return nil
		} else {
			s.warn("skills: 写设置到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileSettings, settings)
}

// warn 在日志器可用时记录一条警告日志（nil 日志器安全）。
func (s *Service) warn(msg string, args ...any) {
	if s.lg != nil {
		s.lg.Warn(msg, args...)
	}
}

// readManifest 读取目标目录里的 manifest（[]string）；文件不存在返回空清单。
func readManifest(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: 读取 manifest 失败: %w", err)
	}

	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("skills: 解析 manifest 失败: %w", err)
	}
	return entries, nil
}

// writeManifest 以 JSON 数组形式写回 manifest。
func writeManifest(path string, entries []string) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("skills: 序列化 manifest 失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("skills: 写回 manifest 失败: %w", err)
	}
	return nil
}

// ===== 技能安装（真实下载）=====

// Install 下载技能到仓库目录（repoDir/<name>）并登记进 skills.json。
//
// 流程：清理同名旧目录 → 按 config.SkillInstallMode 选择下载器（git clone / npx skills）
// → 校验 SKILL.md 存在 → Register 登记。任一步失败即回滚已创建的目录。
func (s *Service) Install(name, source, version string) error {
	if err := validSkillName(name); err != nil {
		return err
	}
	if err := validSkillSource(source); err != nil {
		return err
	}

	dst := filepath.Join(s.repoDir, name)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("skills: 清理旧目录失败: %w", err)
	}
	if err := linkfs.EnsureDir(s.repoDir); err != nil {
		return fmt.Errorf("skills: 创建仓库目录失败: %w", err)
	}

	var err error
	switch config.SkillInstallMode {
	case "npx":
		err = s.installNpx(name, source, dst)
	default: // "git"
		err = s.installGit(source, version, dst)
	}
	if err != nil {
		_ = os.RemoveAll(dst) // 回滚半成品目录
		return err
	}

	// 技能目录里必须有 SKILL.md，否则视为下载不完整。
	if _, statErr := os.Stat(filepath.Join(dst, "SKILL.md")); statErr != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("skills: 下载完成但缺少 SKILL.md，已回滚: %w", statErr)
	}

	return s.Register(name, source, version)
}

// validSkillName 校验技能名为安全的单个目录名：非空、非 "."/".."、不含路径分隔符。
// 防止 name 被拼进 repoDir 后越界（如 "../x" 逃逸出仓库目录）。
func validSkillName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return errors.New("skills: 技能名不能为空")
	}
	if n == "." || n == ".." {
		return fmt.Errorf("skills: 非法技能名: %s", n)
	}
	if strings.ContainsAny(n, `/\`) {
		return fmt.Errorf("skills: 技能名不能包含路径分隔符: %s", n)
	}
	return nil
}

// validSkillSource 校验来源（allowlist）：
//   - 非空、不以 "-" 开头（防 option 注入）；
//   - 仅允许 http://、https://、ssh://、git@（scp-like）与 "owner/repo" 简写；
//   - ssh:// 的主机部分不得以 "-" 开头（防 git < 2.38.1 的 ssh 选项注入）。
//
// 其余一切（file:/file://、git://、ftps://、ext::、本地/UNC 路径等）一律拒绝。
func validSkillSource(source string) error {
	s := strings.TrimSpace(source)
	if s == "" {
		return errors.New("skills: 来源不能为空")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("skills: 非法来源（不能以 - 开头）: %s", s)
	}

	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "git@"):
		// 允许。
	case strings.HasPrefix(lower, "ssh://"):
		host := strings.TrimPrefix(s, "ssh://")
		if host == "" || strings.HasPrefix(host, "-") {
			return fmt.Errorf("skills: 非法 ssh 来源: %s", s)
		}
	case strings.Contains(lower, "/") && !strings.Contains(lower, ":") && !strings.ContainsAny(s, `\`):
		// "owner/repo" 简写，由 gitCloneURL 补全为 https://github.com/。
	default:
		return fmt.Errorf("skills: 不支持的来源: %s", s)
	}
	return nil
}

// installGit 用 git clone 下载：git clone --depth 1 [--branch version] -- <url> dst。
// source 支持 "owner/repo"（自动补全 https://github.com/）或完整 git URL。
// 通过 -c protocol.ext/file.allow=never 禁用 ext 与 file 协议（防 RCE / 本地读取），
// 并用 "--" 分隔位置参数，避免 source/version 被当作 option 注入。
func (s *Service) installGit(source, version, dst string) error {
	url := gitCloneURL(source)
	args := []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--depth", "1",
	}
	if version != "" {
		args = append(args, "--branch="+version)
	}
	args = append(args, "--", url, dst)

	out, err := runCommand("git", args...)
	if err != nil {
		return fmt.Errorf("skills: git clone %s 失败: %v: %s", url, err, strings.TrimSpace(out))
	}
	return nil
}

// installNpx 用 npx skills CLI 一步到位下载：npx skills add <source> -y --copy -g，
// 技能直接落在 ~/.agents/skills（或检测到的平台目录），再找到该技能复制进技能库。
//
// -g 装到用户级目录（~/.agents/skills 等），不污染当前项目的 .agents/skills。
// 不指定 -a，由 npx skills 自动检测环境里的 agent；随后在通用目录与各平台目录
// 中搜索技能名，确保无论装到哪都能搬进技能库。
func (s *Service) installNpx(name, source, dst string) error {
	out, err := runCommand("npx", "-y", "skills", "add", source, "-y", "--copy", "-g")
	if err != nil {
		return fmt.Errorf("skills: npx skills add %s 失败: %v: %s", source, err, strings.TrimSpace(out))
	}

	src := s.findInstalledSkillDir(name)
	if src == "" {
		return fmt.Errorf("skills: npx 已下载但未在 ~/.agents/skills 或平台目录找到技能 %s，请确认技能名，或改用 LOADOUT_SKILL_INSTALL_MODE=git", name)
	}
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("skills: 搬运技能文件失败: %w", err)
	}
	return nil
}

// findInstalledSkillDir 在通用目录与各平台目录中查找已安装的技能目录。
func (s *Service) findInstalledSkillDir(name string) string {
	candidates := []string{filepath.Join(s.targetDir, name)}
	for _, dir := range s.platformDirs {
		candidates = append(candidates, filepath.Join(dir, name))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// SyncSkill 把目标目录（~/.agents/skills）里的技能同步到技能库：
// 源不存在/不是目录时静默跳过（删除不反向同步）；否则删旧 + 复制新（含 .git）。
// 同步后从 ~/.agents/.skill-lock.json 读取来源信息（source/skillFolderHash）并 Register，
// 让 UI 能显示来源与版本（短 hash）。
func (s *Service) SyncSkill(name string) error {
	src := filepath.Join(s.targetDir, name)
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return nil
	}
	dst := filepath.Join(s.repoDir, name)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("skills: 清理技能库旧目录失败: %w", err)
	}
	if err := copyTree(src, dst); err != nil {
		return fmt.Errorf("skills: 同步技能 %s 失败: %w", name, err)
	}
	// 尝试从 lock 文件登记来源（不存在/无条目不报错）。
	if entries, err := readLockEntries(s.targetDir); err == nil {
		if e, ok := entries[name]; ok {
			version := e.SkillFolderHash
			if len(version) > 7 {
				version = version[:7]
			}
			_ = s.Register(name, e.Source, version)
		}
	}
	s.warn("skills: 已同步技能到技能库", "skill", name)
	return nil
}

// SyncAll 主动全量同步：把目标目录（~/.agents/skills）下所有技能同步到技能库，
// 并同步 .skill-lock.json 配置。返回成功同步的技能数；目录不存在返回 0。
func (s *Service) SyncAll() (int, error) {
	names, err := listSkillDirs(s.targetDir)
	if err != nil {
		return 0, err
	}
	synced := 0
	for _, name := range names {
		if err := s.SyncSkill(name); err != nil {
			s.warn("skills: 主动同步技能失败", "skill", name, "error", err)
			continue
		}
		synced++
	}
	// 同步 lock 配置（技能库维护一份完整快照）。
	if err := s.syncLockFile(); err != nil {
		s.warn("skills: 同步锁文件失败", "error", err)
	}
	return synced, nil
}

// lockEntry 解析 npx skills 的 .skill-lock.json（v3 格式）单条记录。
type lockEntry struct {
	Source          string `json:"source"`
	SourceType      string `json:"sourceType"`
	SourceUrl       string `json:"sourceUrl"`
	SkillPath       string `json:"skillPath"`
	SkillFolderHash string `json:"skillFolderHash"`
	InstalledAt     string `json:"installedAt"`
	UpdatedAt       string `json:"updatedAt"`
	PluginName      string `json:"pluginName"`
}

// readLockEntries 读取 npx skills 的全局锁文件（targetDir 父目录下 .skill-lock.json）。
// 文件不存在时返回空 map（不报错）；解析失败返回错误（让调用方决定）。
func readLockEntries(targetDir string) (map[string]lockEntry, error) {
	lockPath := filepath.Join(filepath.Dir(targetDir), ".skill-lock.json")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]lockEntry{}, nil
		}
		return nil, err
	}
	var lock struct {
		Version int                  `json:"version"`
		Skills  map[string]lockEntry `json:"skills"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("skills: 解析 .skill-lock.json 失败: %w", err)
	}
	if lock.Skills == nil {
		lock.Skills = map[string]lockEntry{}
	}
	return lock.Skills, nil
}

// lockFilePath 返回 npx skills 全局锁文件路径（targetDir 父目录下 .skill-lock.json）。
func lockFilePath(targetDir string) string {
	return filepath.Join(filepath.Dir(targetDir), ".skill-lock.json")
}

// syncLockFile 把 .agents 的 .skill-lock.json 复制到技能库（~/.loadout/.skill-lock.json），
// 保证技能库是「技能目录 + lock 配置」的完整快照。.agents 无 lock 时静默跳过。
func (s *Service) syncLockFile() error {
	src := lockFilePath(s.targetDir)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	dst := filepath.Join(s.repoDir, ".skill-lock.json")
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("skills: 同步锁文件到技能库失败: %w", err)
	}
	return nil
}


// UpdateSkills 检测并更新技能。完整流程（官方 check/update 同命令，检测即更新）：
//  0. 快照：扫描 ~/.agents/skills 得到当前在用的技能列表（scanSkills，与列表接口同逻辑）；
//  1. 全量部署：清空重建 ~/.agents/skills，把技能库（~/.loadout/skills 全部技能 +
//     ~/.loadout/.skill-lock.json）同步过来，让 npx skills update 覆盖所有技能；
//  2. 执行 npx skills update -y（逐行输出经 onLog 实时回传）；
//  3. 对比更新前后 .skill-lock.json 的 skillFolderHash 得出实际更新的技能，
//     并把最新代码全部同步回技能库（含 lock 文件）；
//  4. 过滤：删除 ~/.agents/skills 下不在第 0 步快照里的多余技能，
//     .agents 恢复到「更新前在用」的技能集合（内容为更新后版本）。
//
// onLog 用于实时推送进度（SSE）；nil 时静默。
// 注意：更新依赖 GitHub API（匿名限流 60 次/小时），失败时提示用户 gh auth login。
// 旧版本不备份（skills-backup / lock-backup 不产生）：更新即替换，其他 agent 只读
// SKILL.md，lock 更新后更全也不影响。
func (s *Service) UpdateSkills(id string, onLog func(string)) ([]string, error) {
	if onLog == nil {
		onLog = func(string) {}
	}

	// 0) 更新前在用的技能快照（第 4 步过滤依据）。
	onLog("扫描当前在用的技能…")
	before := skillNameSet(scanSkills(s.targetDir))
	onLog(fmt.Sprintf("当前在用 %d 个技能", len(before)))

	// 1) 全量部署：清空重建 .agents，复制技能库全部技能 + lock 配置。
	onLog("全量部署技能库到 ~/.agents/skills…")
	if err := s.deployAll(); err != nil {
		return nil, err
	}
	repoLock := filepath.Join(s.repoDir, ".skill-lock.json")
	if err := copyFile(repoLock, lockFilePath(s.targetDir)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("skills: 部署锁文件失败: %w", err)
	}

	// 2) 执行官方更新（逐行实时输出）。
	onLog("执行 npx skills update -y…")
	beforeHashes, _ := readLockEntries(s.targetDir)
	cmd, args := skillsCmd()
	h, err := procreg.Run(procreg.Options{
		ID:    id,
		Name:  "更新技能",
		Kind:  "skill",
		Cmd:   cmd,
		Args:  args,
		OnLog: onLog,
	})
	if err != nil {
		return nil, fmt.Errorf("skills: 启动 npx skills update 失败: %w", err)
	}
	err = h.Wait()
	if err != nil {
		return nil, fmt.Errorf("skills: npx skills update 失败: %w", err)
	}

	// 3) 对比 hash 得出实际更新的技能，同步最新代码回技能库（含 lock）。
	afterHashes, _ := readLockEntries(s.targetDir)
	updated := []string{}
	for name, e := range afterHashes {
		b, ok := beforeHashes[name]
		if !ok || b.SkillFolderHash != e.SkillFolderHash {
			updated = append(updated, name)
		}
	}
	sort.Strings(updated)
	onLog(fmt.Sprintf("有 %d 个技能发生变化，同步回技能库…", len(updated)))
	if _, err := s.SyncAll(); err != nil {
		s.warn("skills: 更新后同步技能库失败", "error", err)
	}

	// 4) 过滤：删除 .agents 下不在更新前快照里的多余技能。
	onLog("按在用快照过滤多余技能…")
	for _, sk := range scanSkills(s.targetDir) {
		if !before[sk.Name] {
			if err := os.RemoveAll(filepath.Join(s.targetDir, sk.Name)); err != nil {
				s.warn("skills: 删除多余技能失败", "skill", sk.Name, "error", err)
			} else {
				onLog(fmt.Sprintf("已删除多余技能: %s", sk.Name))
			}
		}
	}
	onLog("更新完成")
	return updated, nil
}

// deployAll 把技能库（repoDir）全部技能复制到目标目录（先清空重建）并写 manifest，
// 用于更新前的全量部署，保证 npx skills update 能覆盖所有技能。
func (s *Service) deployAll() error {
	names, err := listSkillDirs(s.repoDir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(s.targetDir); err != nil {
		return err
	}
	if err := linkfs.EnsureDir(s.targetDir); err != nil {
		return err
	}
	created := make([]string, 0, len(names))
	for _, name := range names {
		src := filepath.Join(s.repoDir, name)
		dst := filepath.Join(s.targetDir, name)
		if err := copyTree(src, dst); err != nil {
			s.warn("skills: 全量部署技能失败", "skill", name, "error", err)
			continue
		}
		created = append(created, name)
	}
	return writeManifest(filepath.Join(s.targetDir, manifestName), created)
}

// gitCloneURL 把 "owner/repo" 补全为 https://github.com/owner/repo；
// 已是完整 URL（http(s)://、git@、ssh://、file://）或本地路径则原样返回。
func gitCloneURL(source string) string {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://") ||
		strings.HasPrefix(s, "file://") {
		return s
	}
	if strings.Contains(s, "/") && !strings.Contains(s, ":") && !strings.ContainsAny(s, "\\") {
		return "https://github.com/" + s
	}
	return s
}

// ===== zip 导入 =====

// ImportZip 从 zip 读取器解压技能到仓库目录（repoDir/<name>）并登记。
// 若 zip 内所有条目共用一个顶层目录，则自动剥离该层；每个条目清理后的路径
// 必须落在目标目录内（防 Zip Slip），否则报错并回滚整个目录。
func (s *Service) ImportZip(r io.Reader, name string) error {
	if err := validSkillName(name); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxZipBytes+1))
	if err != nil {
		return fmt.Errorf("skills: 读取 zip 失败: %w", err)
	}
	if len(data) > MaxZipBytes {
		return fmt.Errorf("skills: zip 超过 %d MiB 上限", MaxZipBytes>>20)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("skills: 解析 zip 失败: %w", err)
	}
	if len(zr.File) == 0 {
		return errors.New("skills: zip 内没有文件")
	}

	dst := filepath.Join(s.repoDir, name)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("skills: 清理旧目录失败: %w", err)
	}
	if err := linkfs.EnsureDir(dst); err != nil {
		return fmt.Errorf("skills: 创建目标目录失败: %w", err)
	}

	root := zipCommonRoot(zr.File)
	var extracted int64
	for _, f := range zr.File {
		raw := filepath.ToSlash(f.Name)
		// 防 Zip Slip：在任何剥离之前，先拒绝绝对路径与 ".." 逃逸条目。
		clean := filepath.Clean(raw)
		if filepath.IsAbs(raw) || clean == ".." || strings.HasPrefix(clean, "../") {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("skills: zip 内路径越界: %s", f.Name)
		}

		rel := strings.TrimPrefix(raw, root)
		rel = strings.TrimPrefix(rel, "/")
		rel = filepath.Clean(rel)
		if rel == "." || rel == "" {
			continue
		}
		target := filepath.Join(dst, rel)
		// 二次防御：确保 target 仍落在目标目录内。
		if target != dst && !strings.HasPrefix(target, dst+string(filepath.Separator)) {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("skills: zip 内路径越界: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := linkfs.EnsureDir(target); err != nil {
				_ = os.RemoveAll(dst)
				return fmt.Errorf("skills: 创建目录失败: %w", err)
			}
			continue
		}
		if err := extractZipFile(f, target, &extracted); err != nil {
			_ = os.RemoveAll(dst)
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("skills: zip 内缺少 SKILL.md，已回滚: %w", err)
	}
	return s.Register(name, "zip", "")
}

// zipCommonRoot 返回 zip 内所有条目共用的顶层目录前缀（含末尾 "/"）；
// 若顶层即出现文件（无统一包裹目录）返回空串。
func zipCommonRoot(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	root := ""
	for _, f := range files {
		name := filepath.ToSlash(f.Name)
		idx := strings.Index(name, "/")
		if idx < 0 {
			return "" // 顶层就出现文件，说明没有统一包裹目录
		}
		top := name[:idx+1]
		if root == "" {
			root = top
		} else if root != top {
			return ""
		}
	}
	return root
}

// extractZipFile 解压单个文件条目到 target（含父目录创建），保留权限位。
// extracted 累计解压字节数，超过 maxExtractBytes 时报错（防 zip 炸弹）。
func extractZipFile(f *zip.File, target string, extracted *int64) error {
	if err := linkfs.EnsureDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("skills: 创建父目录失败: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("skills: 打开 zip 条目 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.FileInfo().Mode().Perm())
	if err != nil {
		return fmt.Errorf("skills: 创建文件 %s 失败: %w", target, err)
	}

	limit := maxExtractBytes - *extracted
	if limit < 1 {
		limit = 1
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	*extracted += n
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("skills: 解压 %s 失败: %w", f.Name, err)
	}
	if *extracted > maxExtractBytes {
		return fmt.Errorf("skills: zip 解压后超过 %d MiB 上限", maxExtractBytes>>20)
	}
	return nil
}

// copyTree 递归复制 src 目录内容到 dst（npx 下载后把真实文件搬进仓库）。
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := linkfs.EnsureDir(dst); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(sp, dp); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(sp, dp); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 复制单个文件，保留权限位。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
