package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"loadout/core/linkfs"
)

// watchIgnoreNames 监听时忽略的目录/文件名。
// 注意：忽略只影响「是否触发同步」，不影响复制——同步时 .git 照样整体复制。
var watchIgnoreNames = map[string]bool{
	".git":      true,
	".DS_Store": true,
}

// isWatchIgnore 判断监听事件涉及的路径是否需要忽略（.git 内部、临时文件）。
func isWatchIgnore(name string) bool {
	base := filepath.Base(name)
	if watchIgnoreNames[base] {
		return true
	}
	if strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, "~") || strings.HasPrefix(base, "#") {
		return true
	}
	return false
}

// Watcher 监听目标目录（~/.agents/skills）的变化，增量同步到技能库（~/.loadout/skills）。
//
// 两条独立管道（可在 config 中单独开关）：
//   - 递归监听：fsnotify 监听目标目录及全部子目录，事件防抖后同步新增/修改；
//   - 定时轮询：周期性比对目录指纹，兜底递归监听漏掉的变化（如进程重启间隙）。
//
// 同步语义（单向）：只做「新增 + 修改」（删旧 + 复制新，含 .git），
// 目标目录里删除的技能不会反向删除技能库（技能库是全集）。
type Watcher struct {
	svc *Service
	lg  *slog.Logger

	recursive    bool          // 是否启用 fsnotify 递归监听
	polling      bool          // 是否启用定时全量扫描
	debounce     time.Duration // 事件防抖窗口
	pollInterval time.Duration // 全量扫描间隔

	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	watched  map[string]bool        // 已注册监听的目录
	pending  map[string]*time.Timer // 技能名 → 防抖 timer
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{} // initListen 退出信号（递归模式）
	syncDone chan struct{} // 启动全量同步完成信号
}

// NewWatcher 创建监听器。recursive/polling 按 config 开关传入，可单独开启。
func NewWatcher(svc *Service, recursive, polling bool, debounce, pollInterval time.Duration) *Watcher {
	if debounce <= 0 {
		debounce = time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 5 * time.Minute
	}
	return &Watcher{
		svc:          svc,
		lg:           svc.lg,
		recursive:    recursive,
		polling:      polling,
		debounce:     debounce,
		pollInterval: pollInterval,
		watched:      map[string]bool{},
		pending:      map[string]*time.Timer{},
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		syncDone:     make(chan struct{}),
	}
}

// Start 启动监听：按开关启动对应管道，并做一次全量同步兜底。
//
// 启动路径的取舍：fsnotify 初始化（NewWatcher + WalkDir 注册全部子目录）在
// 特定环境（如旧实例句柄未释放）下可能长时间阻塞，因此**完全后台化、零等待**，
// 装配路径不检查它的结果、不等它就绪。技能同步本就后台执行，无强制即时要求；
// 监听未就绪时的技能变化由「启动全量同步」和「定时轮询」兜底。
func (w *Watcher) Start() error {
	if w.recursive {
		go w.initListen()
	}

	if w.polling {
		go w.pollLoop()
		w.lg.Info("skills: 定时全量扫描已启动", "dir", w.svc.targetDir, "interval", w.pollInterval)
	}

	// 启动即全量同步一次（独立 goroutine，完成信号 syncDone；可被 Stop 中断），
	// 补齐监听启动前的差异。服务卸载时 Stop 会等待本次全量同步收尾，
	// 避免退出/测试清理时仍在写技能库目录。
	go func() {
		defer close(w.syncDone)
		for _, name := range w.listTargetSkills() {
			select {
			case <-w.stop:
				return
			default:
			}
			w.syncSkill(name)
		}
	}()
	return nil
}

// initListen 初始化 fsnotify 递归监听并进入事件循环。仅在递归模式启用时调用，
// 运行在后台 goroutine，失败只降级、不向上抛错。
func (w *Watcher) initListen() {
	defer func() { close(w.done) }()

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		w.lg.Warn("skills: 创建文件监听失败", "err", err)
		return
	}
	w.mu.Lock()
	w.watcher = fw
	w.mu.Unlock()
	if err := w.watchRecursive(); err != nil {
		w.lg.Warn("skills: 注册递归监听失败", "err", err)
		w.mu.Lock()
		w.watcher = nil
		w.mu.Unlock()
		_ = fw.Close()
		return
	}
	w.lg.Info("skills: 递归监听已启动", "dir", w.svc.targetDir, "debounce", w.debounce)
	w.eventLoop()
}

// Stop 停止监听并等待管道退出（幂等）。stop/done 在构造时创建，
// 因此即使 Start 尚未执行（后台启动中即被卸载）也能安全调用。
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.stop)
		w.mu.Lock()
		wt := w.watcher
		w.mu.Unlock()
		if wt != nil {
			_ = wt.Close()
		}
		// 先等启动全量同步收尾（避免仍在写技能库），再等事件管道退出。
		select {
		case <-w.syncDone:
		case <-time.After(5 * time.Second):
		}
		select {
		case <-w.done:
		case <-time.After(2 * time.Second):
		}
	})
}

// ===== 递归监听（fsnotify）=====

// watchRecursive 注册目标目录及其全部子目录到 fsnotify（跳过 .git 等忽略项）。
func (w *Watcher) watchRecursive() error {
	if err := linkfs.EnsureDir(w.svc.targetDir); err != nil {
		return fmt.Errorf("skills: 创建监听目录失败: %w", err)
	}
	return filepath.WalkDir(w.svc.targetDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if isWatchIgnore(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		w.addWatch(path)
		return nil
	})
}

// addWatch 注册单个目录（幂等）。
func (w *Watcher) addWatch(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watched[dir] {
		return
	}
	if err := w.watcher.Add(dir); err != nil {
		w.lg.Warn("skills: 注册监听目录失败", "dir", dir, "err", err)
		return
	}
	w.watched[dir] = true
}

// eventLoop 消费 fsnotify 事件。
func (w *Watcher) eventLoop() {
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.lg.Warn("skills: 监听错误", "err", err)
		}
	}
}

// handleEvent 处理单个事件：提取技能名、新目录动态注册、防抖调度同步。
func (w *Watcher) handleEvent(ev fsnotify.Event) {
	name := filepath.Clean(ev.Name)
	if isWatchIgnore(name) {
		return
	}

	skill, ok := w.skillNameFor(name)
	if !ok {
		return
	}

	// 新目录出现（新技能 / 技能内新增子目录）→ 递归注册其下目录。
	if ev.Op&fsnotify.Create != 0 {
		if fi, err := os.Stat(name); err == nil && fi.IsDir() {
			_ = filepath.WalkDir(name, func(p string, d os.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return nil
				}
				if isWatchIgnore(p) {
					return filepath.SkipDir
				}
				w.addWatch(p)
				return nil
			})
		}
	}

	// 删除/重命名：不同步删除（技能库是全集），仅清理已注册的监听目录。
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		w.mu.Lock()
		delete(w.watched, name)
		w.mu.Unlock()
		return
	}

	w.scheduleSync(skill)
}

// skillNameFor 判断路径是否在目标目录内，并提取其一级技能名。
func (w *Watcher) skillNameFor(path string) (string, bool) {
	rel, err := filepath.Rel(w.svc.targetDir, path)
	if err != nil {
		return "", false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	if first == "" || strings.HasPrefix(first, ".") {
		return "", false
	}
	return first, true
}

// scheduleSync 防抖调度：同一技能的事件在 debounce 窗口内合并，窗口结束才同步。
func (w *Watcher) scheduleSync(skill string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[skill]; ok {
		t.Reset(w.debounce)
		return
	}
	t := time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, skill)
		w.mu.Unlock()
		select {
		case <-w.stop:
			return
		default:
		}
		w.syncSkill(skill)
	})
	w.pending[skill] = t
}

// ===== 定时全量扫描（轮询）=====

// pollLoop 周期性比对目录指纹，差异技能执行同步。
func (w *Watcher) pollLoop() {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	last := map[string]string{}
	w.pollOnce(last) // 首轮建立指纹基线
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.pollOnce(last)
		}
	}
}

// pollOnce 扫描一次目标目录：指纹变化的技能触发同步，并更新指纹基线。
func (w *Watcher) pollOnce(last map[string]string) {
	for _, name := range w.listTargetSkills() {
		fp, err := dirFingerprint(filepath.Join(w.svc.targetDir, name))
		if err != nil {
			continue
		}
		if last[name] != fp {
			w.syncSkill(name)
		}
		last[name] = fp
	}
}

// dirFingerprint 计算技能目录的内容指纹（排除 .git 内部，避免 git 操作触发全量复制）。
func dirFingerprint(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s|%d|%d\n", filepath.ToSlash(rel), fi.Size(), fi.ModTime().Unix())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ===== 同步 =====

// listTargetSkills 列出目标目录下的一级技能目录名（排除隐藏项与 manifest）。
func (w *Watcher) listTargetSkills() []string {
	names, err := listSkillDirs(w.svc.targetDir)
	if err != nil {
		w.lg.Warn("skills: 扫描目标目录失败", "dir", w.svc.targetDir, "err", err)
		return nil
	}
	return names
}

// syncSkill 把目标目录的技能同步到技能库：源不存在则跳过（删除不反向同步）。
func (w *Watcher) syncSkill(skill string) {
	src := filepath.Join(w.svc.targetDir, skill)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return
	}
	if err := w.svc.SyncSkill(skill); err != nil {
		w.lg.Warn("skills: 同步技能失败", "skill", skill, "err", err)
	}
}

// listSkillDirs 列出目录下的一级技能目录名：仅目录、排除隐藏项、manifest 与备份目录。
func listSkillDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == manifestName || strings.HasSuffix(n, "-backup") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
