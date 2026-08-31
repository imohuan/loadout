package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loadout/core/config"
	"loadout/core/procreg"
	"loadout/core/store"
	"loadout/plugins/types"
)

// newTestService 用临时目录组装 Service：store 数据目录、仓库目录、目标目录互相独立，不碰真实 home。
func newTestService(t *testing.T) (*Service, string, string) {
	t.Helper()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	repoDir := t.TempDir()
	targetDir := t.TempDir()
	return NewService(st, slog.Default(), repoDir, targetDir), repoDir, targetDir
}

// mkSkill 在仓库目录里造一个技能目录，内含一个 SKILL.md 文件。
func mkSkill(t *testing.T, repoDir, name string) {
	t.Helper()

	dir := filepath.Join(repoDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录 %s 失败: %v", name, err)
	}
	body := "# " + name + "\nbody of " + name
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md（%s）失败: %v", name, err)
	}
}

// readManifestForTest 读目标目录 manifest 返回条目清单（测试辅助）。
func readManifestForTest(t *testing.T, targetDir string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(targetDir, manifestName))
	if err != nil {
		t.Fatalf("读取 manifest 失败: %v", err)
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}
	return entries
}

// TestRegisterListRemove 验证仓库清单 Register/List/Remove 往返。
// List 扫描技能库目录（含 SKILL.md 的子目录），Remove 删除目标目录副本（targetDir）+ 移除登记，
// Unregister 删除源目录（repoDir）+ 移除登记。
func TestRegisterListRemove(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, repoDir, "a")
	mkSkill(t, repoDir, "b")
	// 目标目录里放一份 a 的副本（agent 实际使用的那份）。
	mkSkill(t, targetDir, "a")

	if err := svc.Register("a", "repo/a", "main"); err != nil {
		t.Fatalf("Register(a) 失败: %v", err)
	}
	if err := svc.Register("b", "repo/b", "v1.2.3"); err != nil {
		t.Fatalf("Register(b) 失败: %v", err)
	}

	got, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List 数量 = %d，期望 2", len(got))
	}
	if got[0].Name != "a" || got[0].Source != "repo/a" || got[0].Version != "main" {
		t.Fatalf("List[0] 内容不符: %+v", got[0])
	}
	if got[1].Name != "b" || got[1].Version != "v1.2.3" {
		t.Fatalf("List[1] 内容不符: %+v", got[1])
	}

	// Remove 删除目标目录（targetDir）副本 + 移除登记，源目录（repoDir）保留。
	if err := svc.Remove("a"); err != nil {
		t.Fatalf("Remove(a) 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "a")); !os.IsNotExist(err) {
		t.Fatalf("Remove 后目标目录副本 a 应被删除，Stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "a")); err != nil {
		t.Fatalf("Remove 后源目录 a 应保留，Stat=%v", err)
	}

	// Unregister 删除源目录（repoDir）+ 移除登记。
	if err := svc.Unregister("b"); err != nil {
		t.Fatalf("Unregister(b) 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "b")); !os.IsNotExist(err) {
		t.Fatalf("Unregister 后源目录 b 应被删除，Stat=%v", err)
	}

	got, err = svc.List()
	if err != nil {
		t.Fatalf("Remove/Unregister 后 List 失败: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("List = %+v，期望只剩源目录里的 a", got)
	}
}

// TestReadLockEntries 验证 .skill-lock.json 解析 + 文件不存在时返回空 map 不报错。
func TestReadLockEntries(t *testing.T) {
	tmp := t.TempDir()
	// 不存在 → 空 map
	entries, err := readLockEntries(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("不存在的 lock 应返回空 map，err=%v entries=%+v", err, entries)
	}
	// 写入 v3 lock 文件
	lockDir := filepath.Dir(tmp)
	lockPath := filepath.Join(lockDir, ".skill-lock.json")
	body := `{"version":3,"skills":{"foo":{"source":"o/r","sourceType":"github","sourceUrl":"https://github.com/o/r.git","skillPath":"skills/foo/SKILL.md","skillFolderHash":"abcdef1234567","installedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(lockPath, []byte(body), 0o644); err != nil {
		t.Fatalf("写 lock 失败: %v", err)
	}
	entries, err = readLockEntries(tmp)
	if err != nil {
		t.Fatalf("readLockEntries 失败: %v", err)
	}
	if e, ok := entries["foo"]; !ok || e.Source != "o/r" || e.SkillFolderHash != "abcdef1234567" {
		t.Fatalf("lock 解析不符: %+v", entries)
	}
}

// TestUpdateSkills 验证 UpdateSkills：mock npx skills update 修改 lock 里 foo 的 hash，
// 前后对比返回更新列表 [foo]；fresh 未变不计入。
func TestUpdateSkills(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	lockPath := filepath.Join(filepath.Dir(targetDir), ".skill-lock.json")
	body := `{"version":3,"skills":{"foo":{"source":"o/r","skillFolderHash":"aaaa","skillPath":"skills/foo/SKILL.md"},"fresh":{"source":"o/r","skillFolderHash":"cccc","skillPath":"skills/fresh/SKILL.md"}}}`
	// 技能库维护 lock 快照（真实场景由 syncLockFile 产生）。
	if err := os.WriteFile(filepath.Join(repoDir, ".skill-lock.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("写技能库 lock 失败: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(body), 0o644); err != nil {
		t.Fatalf("写 lock 失败: %v", err)
	}

	orig := procreg.SetRunFn(func(_ *procreg.Registry, o procreg.Options) (*procreg.Handle, error) {
		if o.Cmd != "npx" {
			t.Fatalf("期望 npx 命令，实际 %q", o.Cmd)
		}
		// 模拟更新后 foo 的 hash 变化、fresh 不变。
		body2 := `{"version":3,"skills":{"foo":{"source":"o/r","skillFolderHash":"bbbb","skillPath":"skills/foo/SKILL.md"},"fresh":{"source":"o/r","skillFolderHash":"cccc","skillPath":"skills/fresh/SKILL.md"}}}`
		if err := os.WriteFile(lockPath, []byte(body2), 0o644); err != nil {
			return nil, err
		}
		return procreg.NewTestHandle(nil), nil
	})
	t.Cleanup(func() { procreg.SetRunFn(orig) })

	updated, err := svc.UpdateSkills("", nil)
	if err != nil {
		t.Fatalf("UpdateSkills 失败: %v", err)
	}
	if len(updated) != 1 || updated[0] != "foo" {
		t.Fatalf("updated = %+v，期望 [foo]", updated)
	}
}

// TestUpdateSkillsRemovesExtras 验证第 5 步：更新前 .agents 只有 a、b，
// 技能库有 a、b、c；更新后 .agents 的多余技能 c 被删除，恢复到在用集合 {a,b}。
func TestUpdateSkillsRemovesExtras(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, repoDir, "a")
	mkSkill(t, repoDir, "b")
	mkSkill(t, repoDir, "c")
	mkSkill(t, targetDir, "a")
	mkSkill(t, targetDir, "b")

	// 技能库 lock（含三个技能）。
	body := `{"version":3,"skills":{"a":{"source":"o/r","skillFolderHash":"ha","skillPath":"skills/a/SKILL.md"},"b":{"source":"o/r","skillFolderHash":"hb","skillPath":"skills/b/SKILL.md"},"c":{"source":"o/r","skillFolderHash":"hc","skillPath":"skills/c/SKILL.md"}}}`
	if err := os.WriteFile(filepath.Join(repoDir, ".skill-lock.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("写技能库 lock 失败: %v", err)
	}

	orig := procreg.SetRunFn(func(_ *procreg.Registry, _ procreg.Options) (*procreg.Handle, error) {
		return procreg.NewTestHandle(nil), nil
	})
	t.Cleanup(func() { procreg.SetRunFn(orig) })

	updated, err := svc.UpdateSkills("", nil)
	if err != nil {
		t.Fatalf("UpdateSkills 失败: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("hash 未变应无更新: %+v", updated)
	}

	// .agents 恢复到在用集合：a、b 保留，c 被删。
	for _, name := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(targetDir, name)); err != nil {
			t.Fatalf(".agents 应保留 %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "c")); !os.IsNotExist(err) {
		t.Fatalf(".agents 应删除多余技能 c: %v", err)
	}
	// 技能库仍是全集（c 保留）。
	for _, name := range []string{"a", "b", "c"} {
		if _, err := os.Stat(filepath.Join(repoDir, name)); err != nil {
			t.Fatalf("技能库应保留 %s: %v", name, err)
		}
	}
}

// TestUpdateSkillsEmptyLock 验证 lock 为空时 UpdateSkills 仍调用 update 但不报错。
func TestUpdateSkillsEmptyLock(t *testing.T) {
	called := false
	orig := procreg.SetRunFn(func(_ *procreg.Registry, _ procreg.Options) (*procreg.Handle, error) {
		called = true
		return procreg.NewTestHandle(nil), nil
	})
	t.Cleanup(func() { procreg.SetRunFn(orig) })

	svc, _, _ := newTestService(t)
	updated, err := svc.UpdateSkills("", nil)
	if err != nil {
		t.Fatalf("UpdateSkills 失败: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("空 lock 应返回空更新列表: %+v", updated)
	}
	if !called {
		t.Fatal("UpdateSkills 应调用 npx 命令")
	}
}

// TestSyncSkillRegistersFromLock 验证 SyncSkill 在 .skill-lock.json 中有对应条目时
// 会把 source（owner/repo）和 version（skillFolderHash 前 7 位）登记到 skills.json。
func TestSyncSkillRegistersFromLock(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)

	// 目标目录造技能。
	mkSkill(t, targetDir, "alpha")
	// 在目标目录父目录写 lock 文件。
	lockDir := filepath.Dir(targetDir)
	lockPath := filepath.Join(lockDir, ".skill-lock.json")
	body := `{"version":3,"skills":{"alpha":{"source":"owner/repo","sourceType":"github","sourceUrl":"https://github.com/owner/repo.git","skillPath":"skills/alpha/SKILL.md","skillFolderHash":"abcdef1234567890","installedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(lockPath, []byte(body), 0o644); err != nil {
		t.Fatalf("写 lock 失败: %v", err)
	}

	if err := svc.SyncSkill("alpha"); err != nil {
		t.Fatalf("SyncSkill 失败: %v", err)
	}
	items, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(items) != 1 || items[0].Name != "alpha" {
		t.Fatalf("List 不符: %+v", items)
	}
	if items[0].Source != "owner/repo" {
		t.Fatalf("Source = %q，期望 owner/repo", items[0].Source)
	}
	if items[0].Version != "abcdef1" {
		t.Fatalf("Version = %q，期望 abcdef1（hash 前 7 位）", items[0].Version)
	}
	_ = repoDir
}

// TestListFrontmatter 验证 List 扫描目录 + 解析 SKILL.md frontmatter 的 name/description；
// 无 SKILL.md 的目录不计入；未登记技能 source 为空。
func TestListFrontmatter(t *testing.T) {
	svc, repoDir, _ := newTestService(t)

	// 带 frontmatter 的技能。
	dir := filepath.Join(repoDir, "agent-reach")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	md := "---\nname: agent-reach\ndescription: >\n  MUST USE when user wants to research.\n\n  Also for URLs.\n---\n# Body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("写 SKILL.md 失败: %v", err)
	}

	// 无 SKILL.md 的目录，不应计入。
	if err := os.MkdirAll(filepath.Join(repoDir, "not-a-skill"), 0o755); err != nil {
		t.Fatalf("创建非技能目录失败: %v", err)
	}

	// 无 frontmatter 的技能，name 回退目录名。
	plain := filepath.Join(repoDir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("创建 plain 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plain, "SKILL.md"), []byte("# plain\nbody"), 0o644); err != nil {
		t.Fatalf("写 plain SKILL.md 失败: %v", err)
	}

	got, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List 数量 = %d，期望 2（agent-reach、plain）: %+v", len(got), got)
	}
	// 按名称排序：agent-reach 在前。
	if got[0].Name != "agent-reach" {
		t.Fatalf("List[0].Name = %q，期望 agent-reach", got[0].Name)
	}
	if got[0].Description == "" || !strings.Contains(got[0].Description, "MUST USE") {
		t.Fatalf("agent-reach description 解析不符: %q", got[0].Description)
	}
	if got[1].Name != "plain" || got[1].Description != "" {
		t.Fatalf("plain 应回退目录名且描述为空: %+v", got[1])
	}
}

// TestParseSkillFrontmatter 验证 frontmatter 解析的边界情况。
func TestParseSkillFrontmatter(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "SKILL.md")

	cases := []struct {
		name string
		body string
		want types.Skill
	}{
		{"正常", "---\nname: demo\ndescription: hello world\n---\n# x\n", types.Skill{Name: "demo", Description: "hello world"}},
		{"多行折叠", "---\nname: demo\ndescription: >\n  line one\n\n  line two\n---\n", types.Skill{Name: "demo", Description: "line one\nline two"}},
		{"无 frontmatter", "# demo\nbody", types.Skill{Name: "demo"}},
		{"frontmatter 未闭合", "---\nname: demo\n", types.Skill{Name: "demo"}},
		{"缺 name", "---\ndescription: x\n---\n", types.Skill{Name: "demo", Description: "x"}},
	}
	for _, c := range cases {
		if err := os.WriteFile(md, []byte(c.body), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
		got := parseSkillFrontmatter("demo", md)
		if got.Name != c.want.Name || got.Description != c.want.Description {
			t.Fatalf("[%s] parseSkillFrontmatter = %+v，期望 %+v", c.name, got, c.want)
		}
	}
}

// TestPresetLifecycle 验证预设 CreatePreset/ListPresets/DeletePreset 与 ActivePreset/SetActivePreset 往返。
func TestPresetLifecycle(t *testing.T) {
	svc, _, _ := newTestService(t)

	if err := svc.CreatePreset("p", []string{"a", "b"}, nil); err != nil {
		t.Fatalf("CreatePreset(p) 失败: %v", err)
	}
	if err := svc.CreatePreset("q", []string{"c"}, nil); err != nil {
		t.Fatalf("CreatePreset(q) 失败: %v", err)
	}

	presets, err := svc.ListPresets()
	if err != nil {
		t.Fatalf("ListPresets 失败: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("ListPresets 数量 = %d，期望 2", len(presets))
	}
	if presets[0].Name != "p" || !reflect.DeepEqual(presets[0].Skills, []string{"a", "b"}) {
		t.Fatalf("presets[0] 内容不符: %+v", presets[0])
	}

	// 初始无 active_preset。
	if active, err := svc.ActivePreset(); err != nil || active != "" {
		t.Fatalf("初始 ActivePreset = %q, err=%v，期望空串", active, err)
	}
	if err := svc.SetActivePreset("p"); err != nil {
		t.Fatalf("SetActivePreset(p) 失败: %v", err)
	}
	if active, err := svc.ActivePreset(); err != nil || active != "p" {
		t.Fatalf("ActivePreset = %q, err=%v，期望 p", active, err)
	}

	if err := svc.DeletePreset("p"); err != nil {
		t.Fatalf("DeletePreset(p) 失败: %v", err)
	}
	presets, err = svc.ListPresets()
	if err != nil {
		t.Fatalf("DeletePreset 后 ListPresets 失败: %v", err)
	}
	if len(presets) != 1 || presets[0].Name != "q" {
		t.Fatalf("DeletePreset 后 ListPresets = %+v，期望只剩 q", presets)
	}
}

// TestApplyPreset 验证 ApplyPreset 建链接、manifest 记录与 SKILL.md 可读。
func TestApplyPreset(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, repoDir, "a")
	mkSkill(t, repoDir, "b")

	if err := svc.CreatePreset("p", []string{"a", "b"}, nil); err != nil {
		t.Fatalf("CreatePreset(p) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 失败: %v", err)
	}

	// 目标目录出现 a、b 两个条目，且能读到仓库的 SKILL.md 内容。
	for _, name := range []string{"a", "b"} {
		data, err := os.ReadFile(filepath.Join(targetDir, name, "SKILL.md"))
		if err != nil {
			t.Fatalf("读取目标条目 %s/SKILL.md 失败: %v", name, err)
		}
		want := "# " + name + "\nbody of " + name
		if string(data) != want {
			t.Fatalf("%s/SKILL.md 内容 = %q，期望 %q", name, data, want)
		}
	}

	// manifest 记录 ["a","b"]。
	if entries := readManifestForTest(t, targetDir); !reflect.DeepEqual(entries, []string{"a", "b"}) {
		t.Fatalf("manifest = %v，期望 [a b]", entries)
	}
}

// TestApplyPresetSwitch 验证切换预设（备份复制式）：旧目录整体重命名 <dir>-backup，
// 手动文件随备份保留、不出现在新目录；新目录只复制预设选中的技能。
func TestApplyPresetSwitch(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, repoDir, "a")
	mkSkill(t, repoDir, "b")

	if err := svc.CreatePreset("p", []string{"a", "b"}, nil); err != nil {
		t.Fatalf("CreatePreset(p) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 失败: %v", err)
	}

	// 手动放进目标目录的普通文件，切换后应随备份目录保留。
	manual := filepath.Join(targetDir, "manual.txt")
	if err := os.WriteFile(manual, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("写入手动文件失败: %v", err)
	}

	if err := svc.CreatePreset("q", []string{"a"}, nil); err != nil {
		t.Fatalf("CreatePreset(q) 失败: %v", err)
	}
	if err := svc.ApplyPreset("q"); err != nil {
		t.Fatalf("ApplyPreset(q) 失败: %v", err)
	}

	// 新目标目录：a 有（复制）、b 没有、manual.txt 不出现。
	if _, err := os.Stat(filepath.Join(targetDir, "a")); err != nil {
		t.Fatalf("切换后 a 应存在: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "b")); !os.IsNotExist(err) {
		t.Fatalf("切换后 b 不应存在，Lstat 返回 %v", err)
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "manual.txt")); !os.IsNotExist(err) {
		t.Fatalf("切换后 manual.txt 不应在新目录，Lstat 返回 %v", err)
	}

	// 旧目录整体进入备份：manual.txt 随备份保留。
	backupDir := targetDir + "-backup"
	if data, err := os.ReadFile(filepath.Join(backupDir, "manual.txt")); err != nil || string(data) != "keep me" {
		t.Fatalf("手动文件应随备份保留，内容=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "b")); err != nil {
		t.Fatalf("旧技能 b 应在备份目录中: %v", err)
	}

	// manifest 更新为 ["a"]。
	if entries := readManifestForTest(t, targetDir); !reflect.DeepEqual(entries, []string{"a"}) {
		t.Fatalf("切换后 manifest = %v，期望 [a]", entries)
	}
}

// TestApplyPresetMissingSkill 验证仓库缺某个技能目录时 ApplyPreset 不报错，其余照常。
func TestApplyPresetMissingSkill(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, repoDir, "a")

	if err := svc.CreatePreset("p", []string{"a", "missing-x"}, nil); err != nil {
		t.Fatalf("CreatePreset(p) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 应不报错，实际: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "a")); err != nil {
		t.Fatalf("存在的技能 a 应被链接: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(targetDir, "missing-x")); !os.IsNotExist(err) {
		t.Fatalf("缺失技能不应出现条目，Lstat 返回 %v", err)
	}

	if entries := readManifestForTest(t, targetDir); !reflect.DeepEqual(entries, []string{"a"}) {
		t.Fatalf("manifest = %v，期望 [a]", entries)
	}
}

// ===== 安装（Install）与 zip 导入（ImportZip）=====

// fakeGitClone 把 runCommand 替换为一个模拟 git clone 的 fake：在最后一个参数
// （目标目录）里写入 SKILL.md（可选），返回假输出。
func fakeGitClone(t *testing.T, writeSkill bool) {
	t.Helper()
	orig := runCommand
	runCommand = func(name string, args ...string) (string, error) {
		if name != "git" {
			t.Fatalf("期望 git 命令，实际 %q", name)
		}
		dst := args[len(args)-1]
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return "", err
		}
		if writeSkill {
			if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), []byte("# demo\n"), 0o644); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = orig })

	prev := config.SkillInstallMode
	config.SkillInstallMode = "git"
	t.Cleanup(func() { config.SkillInstallMode = prev })
}

// makeZip 构造一个内存 zip，entries 为「条目路径 → 内容」。
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%s) 失败: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip 写入 %s 失败: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close 失败: %v", err)
	}
	return buf.Bytes()
}

// TestInstallGit 验证 git 模式下载 + 登记 + SKILL.md 落盘。
func TestInstallGit(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	fakeGitClone(t, true)

	if err := svc.Install("demo", "owner/repo", "main"); err != nil {
		t.Fatalf("Install 失败: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoDir, "demo", "SKILL.md"))
	if err != nil {
		t.Fatalf("读取仓库 SKILL.md 失败: %v", err)
	}
	if string(data) != "# demo\n" {
		t.Fatalf("SKILL.md 内容 = %q", data)
	}

	items, err := svc.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(items) != 1 || items[0].Name != "demo" || items[0].Source != "owner/repo" || items[0].Version != "main" {
		t.Fatalf("登记清单不符: %+v", items)
	}
}

// TestInstallMissingSKILLmd 验证下载后缺 SKILL.md 时回滚目录且不登记。
func TestInstallMissingSKILLmd(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	fakeGitClone(t, false)

	if err := svc.Install("demo", "owner/repo", ""); err == nil {
		t.Fatal("期望缺少 SKILL.md 报错")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("失败后目录应回滚，Stat=%v", err)
	}
	items, _ := svc.List()
	if len(items) != 0 {
		t.Fatalf("失败后不应登记，实际 %+v", items)
	}
}

// TestInstallEmptyArgs 验证空技能名/来源被拒绝。
func TestInstallEmptyArgs(t *testing.T) {
	svc, _, _ := newTestService(t)
	if err := svc.Install("", "owner/repo", ""); err == nil {
		t.Fatal("期望空技能名报错")
	}
	if err := svc.Install("demo", "", ""); err == nil {
		t.Fatal("期望空来源报错")
	}
}

// TestInstallRejectPathTraversal 验证含路径分隔符/相对路径的技能名被拒绝，
// 防止 name 越界到仓库目录之外。
func TestInstallRejectPathTraversal(t *testing.T) {
	svc, _, _ := newTestService(t)
	for _, bad := range []string{"..", "../x", "a/b", `a\b`, "."} {
		if err := svc.Install(bad, "owner/repo", ""); err == nil {
			t.Fatalf("期望技能名 %q 被拒绝", bad)
		}
	}
}

// TestInstallRejectBadSource 验证危险来源协议 / option 注入被拒绝。
func TestInstallRejectBadSource(t *testing.T) {
	svc, _, _ := newTestService(t)
	bad := []string{
		"ext::sh -c whoami",
		"file:///etc/passwd",
		"file:/etc/passwd",
		"git://host/x",
		"ftps://host/x",
		"ssh://-oProxyCommand=evil",
		"-oProxyCommand=evil",
		`C:\windows\system32`,
	}
	for _, src := range bad {
		if err := svc.Install("demo", src, ""); err == nil {
			t.Fatalf("期望来源 %q 被拒绝", src)
		}
	}
}

// TestInstallAcceptGoodSource 验证合法来源能通过校验（在真正下载前不会被校验拦下）。
func TestInstallAcceptGoodSource(t *testing.T) {
	svc, _, _ := newTestService(t)
	// 这里只验证校验通过后进入下载阶段：git 模式会用 fake，npx 模式跳过。
	// 用 fake git clone 兜住后续步骤，避免真实网络。
	fakeGitClone(t, true)
	for _, src := range []string{
		"owner/repo",
		"https://github.com/o/r",
		"http://example.com/o/r.git",
		"git@github.com:o/r.git",
		"ssh://git@example.com/o/r.git",
	} {
		if err := svc.Install("demo", src, "main"); err != nil {
			t.Fatalf("合法来源 %q 不应被校验拒绝，err=%v", src, err)
		}
	}
}

// TestImportZipRejectBadName 验证 zip 导入的 name 同样做路径穿越校验。
func TestImportZipRejectBadName(t *testing.T) {
	svc, _, _ := newTestService(t)
	z := makeZip(t, map[string]string{"SKILL.md": "# s\n"})
	if err := svc.ImportZip(bytes.NewReader(z), "../evil"); err == nil {
		t.Fatal("期望越界技能名被拒绝")
	}
}

// TestImportZip 验证 zip 导入：解压 + 登记（source=zip）。
func TestImportZip(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	z := makeZip(t, map[string]string{
		"SKILL.md":     "# zip-skill\n",
		"scripts/x.sh": "#!/bin/sh\n",
	})

	if err := svc.ImportZip(bytes.NewReader(z), "zip-skill"); err != nil {
		t.Fatalf("ImportZip 失败: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "zip-skill", "SKILL.md"))
	if err != nil || string(data) != "# zip-skill\n" {
		t.Fatalf("SKILL.md 不符: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "zip-skill", "scripts", "x.sh")); err != nil {
		t.Fatalf("子目录文件缺失: %v", err)
	}

	items, _ := svc.List()
	if len(items) != 1 || items[0].Name != "zip-skill" || items[0].Source != "zip" {
		t.Fatalf("登记清单不符: %+v", items)
	}
}

// TestImportZipStripRoot 验证 zip 统一顶层目录被剥离。
func TestImportZipStripRoot(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	z := makeZip(t, map[string]string{
		"my-skill/SKILL.md": "# s\n",
	})

	if err := svc.ImportZip(bytes.NewReader(z), "s"); err != nil {
		t.Fatalf("ImportZip 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "s", "SKILL.md")); err != nil {
		t.Fatalf("剥离顶层后 SKILL.md 应在 s/ 下: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "s", "my-skill")); !os.IsNotExist(err) {
		t.Fatalf("顶层目录不应残留，Stat=%v", err)
	}
}

// TestImportZipSlip 验证 zip 内 ".." 逃逸条目被拒绝并回滚。
func TestImportZipSlip(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	z := makeZip(t, map[string]string{
		"SKILL.md":    "# s\n",
		"../evil.txt": "x",
	})

	if err := svc.ImportZip(bytes.NewReader(z), "s"); err == nil {
		t.Fatal("期望 Zip Slip 报错")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "s")); !os.IsNotExist(err) {
		t.Fatalf("越界后目录应回滚，Stat=%v", err)
	}
	items, _ := svc.List()
	if len(items) != 0 {
		t.Fatalf("越界后不应登记，实际 %+v", items)
	}
}

// TestImportZipMissingSKILLmd 验证 zip 缺 SKILL.md 时回滚。
func TestImportZipMissingSKILLmd(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	z := makeZip(t, map[string]string{"README.md": "no skill"})

	if err := svc.ImportZip(bytes.NewReader(z), "s"); err == nil {
		t.Fatal("期望缺少 SKILL.md 报错")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "s")); !os.IsNotExist(err) {
		t.Fatalf("缺 SKILL.md 后目录应回滚，Stat=%v", err)
	}
}

// TestGitCloneURL 验证 owner/repo 补全与完整 URL 透传。
func TestGitCloneURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"owner/repo", "https://github.com/owner/repo"},
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"git@github.com:o/r.git", "git@github.com:o/r.git"},
		{"ssh://git@host/o/r.git", "ssh://git@host/o/r.git"},
		{"file:///tmp/x", "file:///tmp/x"},
	}
	for _, c := range cases {
		if got := gitCloneURL(c.in); got != c.want {
			t.Fatalf("gitCloneURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// waitFor 轮询等待条件成立（最长 5s），超时则失败。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestApplyPresetRejectsUnknownPlatform 验证未知平台被拒绝。
func TestApplyPresetRejectsUnknownPlatform(t *testing.T) {
	svc, _, _ := newTestService(t)
	if err := svc.CreatePreset("bad", []string{"a"}, []string{"no-such-platform"}); err == nil {
		t.Fatal("期望未知平台报错")
	}
}

// TestApplyPresetPlatform 验证平台预设：部署到平台目录；通用目录非空时同时被备份。
func TestApplyPresetPlatform(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	platformDir := filepath.Join(t.TempDir(), "codex-skills")
	svc.platformDirs = map[string]string{"codex": platformDir} // 同包注入测试平台目录
	mkSkill(t, repoDir, "a")

	// 先部署一个通用预设，让通用目录非空。
	if err := svc.CreatePreset("g", []string{"a"}, nil); err != nil {
		t.Fatalf("CreatePreset(g) 失败: %v", err)
	}
	if err := svc.ApplyPreset("g"); err != nil {
		t.Fatalf("ApplyPreset(g) 失败: %v", err)
	}

	// 切到 codex 平台预设：通用目录被备份，技能部署到平台目录。
	if err := svc.CreatePreset("p", []string{"a"}, []string{"codex"}); err != nil {
		t.Fatalf("CreatePreset(p, codex) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 失败: %v", err)
	}

	if _, err := os.Stat(filepath.Join(platformDir, "a", "SKILL.md")); err != nil {
		t.Fatalf("平台目录应有技能 a: %v", err)
	}
	if _, err := os.Stat(targetDir + "-backup"); err != nil {
		t.Fatalf("切换平台预设后通用目录应被备份: %v", err)
	}
	// 通用目录本身被重建为空。
	if _, err := os.Stat(targetDir); err != nil {
		t.Fatalf("通用目录应存在: %v", err)
	}
	// 当前预设已激活。
	if active, err := svc.ActivePreset(); err != nil || active != "p" {
		t.Fatalf("ActivePreset = %q, err=%v，期望 p", active, err)
	}
}

// TestApplyPresetMultiTarget 验证多平台预设：技能同时部署到多个平台目录，
// 通用目录同样被备份重建，settings 记录完整平台列表。
func TestApplyPresetMultiTarget(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	codexDir := filepath.Join(t.TempDir(), "codex-skills")
	opencodeDir := filepath.Join(t.TempDir(), "opencode-skills")
	svc.platformDirs = map[string]string{"codex": codexDir, "opencode": opencodeDir}
	mkSkill(t, repoDir, "a")
	mkSkill(t, repoDir, "b")

	// 先部署一个通用预设，让通用目录非空。
	if err := svc.CreatePreset("g", []string{"a"}, nil); err != nil {
		t.Fatalf("CreatePreset(g) 失败: %v", err)
	}
	if err := svc.ApplyPreset("g"); err != nil {
		t.Fatalf("ApplyPreset(g) 失败: %v", err)
	}

	if err := svc.CreatePreset("p", []string{"a", "b"}, []string{"codex", "opencode"}); err != nil {
		t.Fatalf("CreatePreset(p, codex+opencode) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 失败: %v", err)
	}

	// 两个平台目录都有 a、b。
	for _, dir := range []string{codexDir, opencodeDir} {
		for _, name := range []string{"a", "b"} {
			if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
				t.Fatalf("平台目录 %s 应有技能 %s: %v", dir, name, err)
			}
		}
	}
	// 通用目录被备份重建为空。
	if _, err := os.Stat(targetDir + "-backup"); err != nil {
		t.Fatalf("多平台预设切换后通用目录应被备份: %v", err)
	}
	// settings 记录完整平台列表。
	var settings types.Settings
	if err := svc.st.Read(types.FileSettings, &settings); err != nil {
		t.Fatalf("读 settings 失败: %v", err)
	}
	if !reflect.DeepEqual(settings.ActivePresetTargets, []string{"codex", "opencode"}) {
		t.Fatalf("ActivePresetTargets = %v，期望 [codex opencode]", settings.ActivePresetTargets)
	}
}

// TestSyncSkill 验证 SyncSkill：新增 + 修改覆盖，且不反向删除。
func TestSyncSkill(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, targetDir, "alpha")

	if err := svc.SyncSkill("alpha"); err != nil {
		t.Fatalf("SyncSkill(alpha) 失败: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "# alpha\nbody of alpha" {
		t.Fatalf("同步后 SKILL.md 不符: %q err=%v", data, err)
	}

	// 修改源后再次同步 → 覆盖。
	if err := os.WriteFile(filepath.Join(targetDir, "alpha", "SKILL.md"), []byte("# alpha v2\n"), 0o644); err != nil {
		t.Fatalf("修改源失败: %v", err)
	}
	if err := svc.SyncSkill("alpha"); err != nil {
		t.Fatalf("第二次 SyncSkill 失败: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(repoDir, "alpha", "SKILL.md"))
	if err != nil || string(data) != "# alpha v2\n" {
		t.Fatalf("覆盖后 SKILL.md 不符: %q err=%v", data, err)
	}

	// 目标目录删除技能 → 技能库不反向删除。
	if err := os.RemoveAll(filepath.Join(targetDir, "alpha")); err != nil {
		t.Fatalf("删除源失败: %v", err)
	}
	if err := svc.SyncSkill("alpha"); err != nil {
		t.Fatalf("SyncSkill 对缺失源应静默成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "alpha")); err != nil {
		t.Fatalf("技能库不应被反向删除: %v", err)
	}
}

// TestStatusAndRestore 验证 Status 检测 + RestoreBackup + RestoreAllBackups。
func TestStatusAndRestore(t *testing.T) {
	svc, repoDir, _ := newTestService(t)
	mkSkill(t, repoDir, "a")

	if err := svc.CreatePreset("p", []string{"a"}, nil); err != nil {
		t.Fatalf("CreatePreset(p) 失败: %v", err)
	}
	if err := svc.ApplyPreset("p"); err != nil {
		t.Fatalf("ApplyPreset(p) 失败: %v", err)
	}

	// 通用 count=1、无备份。
	st := svc.Status()
	var generic *PlatformStatus
	for i := range st {
		if st[i].Name == "" {
			generic = &st[i]
			break
		}
	}
	if generic == nil {
		t.Fatal("Status 缺少通用条目")
	}
	if generic.Count != 1 || generic.HasBackup {
		t.Fatalf("应用预设后通用 status = %+v，期望 count=1 无备份", *generic)
	}

	// 再次切换制造备份（q 为空清单 → 旧目录进备份）。
	if err := svc.CreatePreset("q", nil, nil); err != nil {
		t.Fatalf("CreatePreset(q) 失败: %v", err)
	}
	if err := svc.ApplyPreset("q"); err != nil {
		t.Fatalf("ApplyPreset(q) 失败: %v", err)
	}
	st = svc.Status()
	for i := range st {
		if st[i].Name == "" {
			generic = &st[i]
			break
		}
	}
	if !generic.HasBackup {
		t.Fatalf("切换后通用应检测到备份: %+v", *generic)
	}

	// 恢复：备份改回，技能 a 回来。
	if err := svc.RestoreBackup(""); err != nil {
		t.Fatalf("RestoreBackup 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.targetDir, "a")); err != nil {
		t.Fatalf("恢复后技能 a 应存在: %v", err)
	}
	st = svc.Status()
	for i := range st {
		if st[i].Name == "" {
			generic = &st[i]
			break
		}
	}
	if generic.HasBackup {
		t.Fatalf("恢复后通用不应再检测到备份: %+v", *generic)
	}

	// RestoreAllBackups：无备份目标全部静默跳过，不报错。
	restored := svc.RestoreAllBackups()
	_ = restored
}

// TestWatcherRecursive 验证 fsnotify 递归监听：新增与修改自动同步到技能库。
func TestWatcherRecursive(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	w := NewWatcher(svc, true, false, 100*time.Millisecond, time.Minute)
	if err := w.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer w.Stop()

	// 新增技能。
	mkSkill(t, targetDir, "alpha")
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(repoDir, "alpha", "SKILL.md"))
		return err == nil
	})

	// 修改技能内容 → 覆盖同步。
	if err := os.WriteFile(filepath.Join(targetDir, "alpha", "SKILL.md"), []byte("# alpha v2\n"), 0o644); err != nil {
		t.Fatalf("修改源失败: %v", err)
	}
	waitFor(t, func() bool {
		data, err := os.ReadFile(filepath.Join(repoDir, "alpha", "SKILL.md"))
		return err == nil && string(data) == "# alpha v2\n"
	})
}

// TestWatcherPolling 验证定时全量扫描：不依赖事件，轮询发现变化并同步。
func TestWatcherPolling(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	w := NewWatcher(svc, false, true, time.Second, 150*time.Millisecond)
	if err := w.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer w.Stop()

	mkSkill(t, targetDir, "beta")
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(repoDir, "beta", "SKILL.md"))
		return err == nil
	})
}

// TestSyncAll 验证主动全量同步：目标目录全部技能同步到技能库并返回数量。
func TestSyncAll(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)
	mkSkill(t, targetDir, "a")
	mkSkill(t, targetDir, "b")

	n, err := svc.SyncAll()
	if err != nil {
		t.Fatalf("SyncAll 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("SyncAll 数量 = %d，期望 2", n)
	}
	for _, name := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(repoDir, name, "SKILL.md")); err != nil {
			t.Fatalf("技能库应有 %s: %v", name, err)
		}
	}
}

// TestInstallNpx 验证 npx 一步到位安装：技能落 ~/.agents/skills 后复制进技能库并登记。
func TestInstallNpx(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)

	prev := config.SkillInstallMode
	config.SkillInstallMode = "npx"
	t.Cleanup(func() { config.SkillInstallMode = prev })

	orig := runCommand
	runCommand = func(name string, args ...string) (string, error) {
		if name != "npx" {
			t.Fatalf("期望 npx 命令，实际 %q", name)
		}
		mkSkill(t, targetDir, "demo") // 模拟 npx skills add 后技能落在通用目录
		return "", nil
	}
	t.Cleanup(func() { runCommand = orig })

	if err := svc.Install("demo", "owner/repo", ""); err != nil {
		t.Fatalf("Install(npx) 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "demo", "SKILL.md")); err != nil {
		t.Fatalf("技能库应有 demo: %v", err)
	}
	items, err := svc.List()
	if err != nil || len(items) != 1 || items[0].Name != "demo" || items[0].Source != "owner/repo" {
		t.Fatalf("登记清单不符: %+v err=%v", items, err)
	}
}


// TestRemoveUnregisterDirNameMismatch verifies that when a skill dir name differs
// from its SKILL.md frontmatter name, Remove/Unregister still find and delete the
// real directory by matching the frontmatter name (regression: ask-matt copy dir
// declares ask-matt-v2, previously os.RemoveAll(repoDir/name) missed the dir).
func TestRemoveUnregisterDirNameMismatch(t *testing.T) {
	svc, repoDir, targetDir := newTestService(t)

	// dir "dir-a" with frontmatter name "alias-a".
	dir := filepath.Join(repoDir, "dir-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo dir: %v", err)
	}
	md := "---\nname: alias-a\ndescription: d\n---\n# body"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write repo SKILL.md: %v", err)
	}
	tdir := filepath.Join(targetDir, "dir-a")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write target SKILL.md: %v", err)
	}

	got, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Name != "alias-a" {
		t.Fatalf("List = %+v, want alias-a", got)
	}

	// Remove should delete targetDir/dir-a and keep repoDir/dir-a.
	if err := svc.Remove("alias-a"); err != nil {
		t.Fatalf("Remove(alias-a): %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "dir-a")); !os.IsNotExist(err) {
		t.Fatalf("target dir-a should be removed, Stat=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "dir-a")); err != nil {
		t.Fatalf("repo dir-a should remain, Stat=%v", err)
	}

	// Unregister should delete repoDir/dir-a.
	if err := svc.Unregister("alias-a"); err != nil {
		t.Fatalf("Unregister(alias-a): %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "dir-a")); !os.IsNotExist(err) {
		t.Fatalf("repo dir-a should be removed, Stat=%v", err)
	}
}
