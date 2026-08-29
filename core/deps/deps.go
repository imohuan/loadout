// Package deps 提供全局 npm 依赖的查询与安装，供 unifyai / skills / admin-api 复用。
//
// 核心职责：
//  1. 查询某个库的安装状态（npm outdated -g --json + npm ls -g）；
//  2. 按「全局指令开关 + 是否已全局安装」决定执行命令用全局指令还是 npx；
//  3. 安装/更新统一走 npm install -g <name>@latest，经 procreg 统一执行（SSE 可见）。
package deps

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"loadout/core/procreg"
)

// UseGlobal 全局指令开关（true=优先用全局指令，false=用 npx）。
// 由 admin-api 在读取/保存设置时同步（types.Settings.UseGlobalCmd）。
var UseGlobal bool

// Status 单个库的检查状态。
type Status struct {
	Name       string `json:"name"`       // 库名（unifyai / skills）
	Installed  bool   `json:"installed"`  // 是否已全局安装
	Current    string `json:"current"`    // 当前已装版本（未装时为空）
	Latest     string `json:"latest"`     // 最新版本
	NeedUpdate bool   `json:"needUpdate"` // 已装且需要更新
	Error      string `json:"error,omitempty"`
}

// Check 查询单个库的安装状态（npm ls -g --json + npm view dist-tags --json）。
// 判定逻辑：
//  1. npm ls -g 拿到本地当前版本（有 = 已装，无 = 未装）；
//  2. npm view dist-tags 拿到最新版本；
//  3. 已装 且 当前 != 最新 → 需更新；已装 且 相等 → 已最新；未装 → 未安装。
func Check(name string) Status {
	st := Status{Name: name}

	// 并发跑「本地版本」和「最新版本」两条查询，减少等待。
	var current string
	var installed bool
	var latest string
	var lsErr, viewErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		current, installed, lsErr = installedVersion(name)
	}()
	go func() {
		defer wg.Done()
		latest, viewErr = latestVersion(name)
	}()
	wg.Wait()

	if lsErr != nil {
		st.Error = lsErr.Error()
		return st
	}
	if viewErr != nil {
		st.Error = viewErr.Error()
		return st
	}

	st.Installed = installed
	st.Current = current
	st.Latest = latest
	if installed && latest != "" && current != latest {
		st.NeedUpdate = true
	}
	return st
}

// latestVersion 执行 `npm view <name> -g --json dist-tags` 并解析 latest。
func latestVersion(name string) (string, error) {
	lines, _ := runCollect("检查最新 "+name, "dep", "view", name, "-g", "--json", "dist-tags")
	return parseDistTagsJSON(strings.Join(lines, "\n"))
}

// parseDistTagsJSON 解析 `npm view <name> --json dist-tags` 输出，返回 latest。
func parseDistTagsJSON(text string) (string, error) {
	var tags map[string]string
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &tags); err != nil {
		return "", fmt.Errorf("解析 npm view dist-tags 失败: %v (输出: %s)", err, trimmed)
	}
	latest, ok := tags["latest"]
	if !ok || latest == "" {
		return "", fmt.Errorf("npm view dist-tags 无 latest")
	}
	return latest, nil
}

// installedVersion 用 `npm ls -g <name> --depth=0 --json` 判断是否全局顶层安装并返回当前版本。
// 返回 (版本, 是否已装)。嵌套依赖不影响：--depth=0 只看顶层。
func installedVersion(name string) (string, bool, error) {
	lines, _ := runCollect("检查已装 "+name, "dep", "ls", "-g", name, "--depth=0", "--json")
	return parseNpmLsJSON(strings.Join(lines, "\n"), name)
}

// parseNpmLsJSON 解析 `npm ls -g --depth=0 --json` 输出，返回 (版本, 是否已装)。
func parseNpmLsJSON(text, name string) (string, bool, error) {
	var tree struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &tree); err != nil {
		return "", false, fmt.Errorf("解析 npm ls --json 失败: %v (输出: %s)", err, trimmed)
	}
	if dep, ok := tree.Dependencies[name]; ok && dep.Version != "" {
		return dep.Version, true, nil
	}
	return "", false, nil
}

// npmPath 返回可用的 npm 命令路径（优先 PATH，Windows 兜底常见安装位置）。
func npmPath() string {
	if p, err := exec.LookPath("npm"); err == nil {
		return p
	}
	candidates := []string{}
	if appdata := getenv("APPDATA"); appdata != "" {
		candidates = append(candidates,
			appdata+"\\npm\\npm.cmd",
			appdata+"\\npm\\npx.cmd",
		)
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return "npm" // 最终回落
}

// GlobalAvailable 判断 name 对应的全局指令是否可用（exec.LookPath）。
func GlobalAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// installMu 串行化安装，避免并发 install 互相干扰 npm 全局目录。
var installMu sync.Mutex

// Install 安装/更新全局包：npm install -g <name>@latest，经 procreg 统一执行。
// onLog 用于实时推送日志（SSE）；nil 时静默。返回退出错误（nil=成功）。
// id 可传前端 task id（空则自动生成），用于按 id 关联/查询该进程。
func Install(name string, id string, onLog func(string)) error {
	if onLog == nil {
		onLog = func(string) {}
	}
	installMu.Lock()
	defer installMu.Unlock()

	full := []string{"install", "-g", name + "@latest"}
	onLog(fmt.Sprintf("执行: %s %s", npmPath(), strings.Join(full, " ")))
	h, err := procreg.Run(procreg.Options{
		ID:    id,
		Name:  "安装 " + name,
		Kind:  "dep",
		Cmd:   npmPath(),
		Args:  full,
		OnLog: onLog,
	})
	if err != nil {
		return fmt.Errorf("deps: 启动安装失败: %w", err)
	}
	if err := h.Wait(); err != nil {
		return fmt.Errorf("deps: 安装 %s 失败: %w", name, err)
	}
	return nil
}

// runCollect 用 procreg 统一执行一条 npm 命令并收集全部输出行。
// 走 procreg 让命令出现在全局进程面板（ProcessFooter 可见），kind 统一为 "dep"。
// 返回 (输出行, 退出错误)。命令快速完成时输出在 Wait 后已完整收集。
// 通用实现已提取到 procreg.RunCollect。
func runCollect(name string, kind string, args ...string) ([]string, error) {
	return procreg.RunCollect(name, kind, npmPath(), args, nil)
}

// CheckAll 批量检查多个库，返回按输入顺序的结果。
func CheckAll(names []string) []Status {
	out := make([]Status, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			out[idx] = Check(name)
		}(i, n)
	}
	wg.Wait()
	return out
}

var getenv = os.Getenv

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
