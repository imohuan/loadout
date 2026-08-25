// Package procreg 提供统一的命令执行入口与全局进程注册表。
//
// 所有后台命令（技能更新、unifyai 同步、MCP 子进程等）都应经 procreg 启动或登记，
// 以便统一：进程状态查询、全局 SSE 推送、内存采样与终止。
//
// 进程关闭统一规则：进程结束（正常退出或被终止）后，自动从「运行中」移除，
// 追加进「历史记录」列表，并广播一条 update 事件。历史记录为内存态，
// 后端每次启动全部清空。
package procreg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"loadout/core/cmdutil"
)

// ProcStatus 进程状态。
type ProcStatus string

const (
	StatusRunning ProcStatus = "running"
	StatusDone    ProcStatus = "done"
	StatusError   ProcStatus = "error"
)

// Proc 一条进程记录（运行中或历史）。
type Proc struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Cmd       string     `json:"cmd"`
	PID       int        `json:"pid"`
	Status    ProcStatus `json:"status"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   time.Time  `json:"endedAt,omitempty"`
	ExitCode  int        `json:"exitCode,omitempty"`
	MemBytes  uint64     `json:"memBytes"`
	Log       []string   `json:"log"`
}

// Event 进程表广播事件（SSE 载荷）。
type Event struct {
	Type string `json:"type"` // snapshot | update
	Data []Proc `json:"data,omitempty"`
}

// Options 统一执行入口的启动参数。
type Options struct {
	Name  string   // 展示名（如「更新技能」），SSE 与前端用它展示
	Kind  string   // 分类（skill | unifyai | mcp | …）
	Cmd   string   // 命令
	Args  []string // 参数
	OnLog func(line string)
}

// Handle 单个进程的控制器。
type Handle struct {
	procID string
	done   chan error
}

// ID 返回进程 ID。
func (h *Handle) ID() string { return h.procID }

// Wait 阻塞等待进程结束，返回命令退出错误（nil 表示成功）。
func (h *Handle) Wait() error { return <-h.done }

// NewTestHandle 返回一个 Wait 立即返回给定错误的 Handle（测试辅助，mock 命令执行用）。
func NewTestHandle(err error) *Handle {
	done := make(chan error, 1)
	done <- err
	return &Handle{procID: "test-handle", done: done}
}

// HistoryStore 进程历史持久化接口。注入后可让历史记录跨后端重启保留。
// procreg 保持纯核心不直接依赖 db：servercore 装配时注入 SQLite 实现。
type HistoryStore interface {
	// Save 保存一条已结束的进程记录（幂等：按 id upsert）。
	Save(proc Proc) error
	// List 返回按结束时间新→旧排序的历史记录，最多 limit 条（limit<=0 表示不限）。
	List(limit int) ([]Proc, error)
}

// procreg 全局注册表。
type Registry struct {
	mu      sync.Mutex
	seq     int
	running map[string]*Proc // 运行中的进程
	history []*Proc          // 历史记录（新→旧排序，上限 historyLimit，内存态兜底）
	subs    map[chan Event]bool

	historyStore HistoryStore // 可选持久化后端（注入后历史可跨重启保留）
	storeLoaded  bool         // 本次会话是否已把内存历史回填进 store

	maxLogLines int // 每进程日志行数上限
	historyMax  int // 内存历史记录条数上限
}

// 默认上限（防长输出/多进程撑爆内存）。
const (
	defaultMaxLogLines = 500
	defaultHistoryMax  = 50
)

var (
	global    *Registry
	globalOnce sync.Once
)

// Get 返回全局注册表单例（所有接入点共享），并自动启动内存采样循环。
func Get() *Registry {
	globalOnce.Do(func() {
		global = New()
		global.StartSampler(sampleInterval)
	})
	return global
}

// Run 包级便捷函数：在全局注册表上异步启动命令。等价于 Get().Run(o)。
func Run(o Options) (*Handle, error) { return Get().Run(o) }

// RegisterExisting 包级便捷函数：在全局注册表上登记外部进程。
func RegisterExisting(id, name, kind string, cmd *exec.Cmd) *Handle {
	return Get().RegisterExisting(id, name, kind, cmd)
}

// Kill 包级便捷函数：终止全局注册表中的进程。
func Kill(id string) error { return Get().Kill(id) }

// Running 包级便捷函数：查询全局注册表中的运行中进程。
func Running(id string) *Proc { return Get().Running(id) }

// Unregister 包级便捷函数：把登记的外部进程移出运行中并加入历史。
func Unregister(id string, runErr error) { Get().Unregister(id, runErr) }

// runCommandFn 是 Registry.Run 的底层可替换 seam（测试可替换为 fake，
// 避免真实执行命令、模拟命令的副作用与退出结果）。
var runCommandFn = func(r *Registry, o Options) (*Handle, error) { return r.defaultRun(o) }

// SetRunFn 替换底层命令执行实现（仅供测试使用）。返回旧实现，便于 Cleanup 恢复。
func SetRunFn(fn func(r *Registry, o Options) (*Handle, error)) func(r *Registry, o Options) (*Handle, error) {
	old := runCommandFn
	if fn == nil {
		runCommandFn = func(r *Registry, o Options) (*Handle, error) { return r.defaultRun(o) }
	} else {
		runCommandFn = fn
	}
	return old
}

// sampleInterval 内存采样周期。
const sampleInterval = 2 * time.Second

// StartSampler 启动后台内存采样循环：每 interval 采样一次所有运行中进程的内存。
// 采样结果经 SetMem 广播（前端实时展示）。
func (r *Registry) StartSampler(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			r.sampleAll()
		}
	}()
}

// sampleAll 采样所有运行中进程的内存。
func (r *Registry) sampleAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.running))
	pids := make([]int, 0, len(r.running))
	for id, p := range r.running {
		ids = append(ids, id)
		pids = append(pids, p.PID)
	}
	r.mu.Unlock()
	for i, id := range ids {
		if pids[i] <= 0 {
			continue
		}
		mem := sampleMem(pids[i])
		if mem > 0 {
			r.SetMem(id, mem)
		}
	}
}

// New 创建一个新的注册表（主要供测试使用）。
func New() *Registry {
	return &Registry{
		running:     map[string]*Proc{},
		subs:        map[chan Event]bool{},
		maxLogLines: defaultMaxLogLines,
		historyMax:  defaultHistoryMax,
	}
}

// SetHistoryStore 注入历史持久化后端。进程结束记录会写入 store，Snapshot 会合并
// store 中的完整历史（跨重启保留）。可传 nil 关闭持久化。只应在启动装配时调用一次。
func (r *Registry) SetHistoryStore(s HistoryStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyStore = s
	if s != nil {
		// 把本次会话已有的内存历史回填进 store，避免注入后旧记录丢失。
		r.storeLoaded = true
		for i := len(r.history) - 1; i >= 0; i-- {
			if err := s.Save(*r.history[i]); err != nil {
				break
			}
		}
	}
}

// Subscribe 订阅进程事件流。返回事件 channel；连接断开后调用 Unsubscribe。
func (r *Registry) Subscribe() chan Event {
	ch := make(chan Event, 64)
	r.mu.Lock()
	r.subs[ch] = true
	r.mu.Unlock()
	return ch
}

// Unsubscribe 取消订阅。
func (r *Registry) Unsubscribe(ch chan Event) {
	r.mu.Lock()
	delete(r.subs, ch)
	r.mu.Unlock()
}

// broadcast 向所有订阅者推送事件（非阻塞，满则丢弃，避免慢消费者拖垮）。
func (r *Registry) broadcast(ev Event) {
	r.mu.Lock()
	for ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	r.mu.Unlock()
}

// Snapshot 返回当前全部进程（运行中 + 历史，新→旧排序）。供 SSE 连接时全量推送。
// 注入了 HistoryStore 时，会先取 store 中完整历史（跨重启保留），再合并本次会话
// 内存历史与当前运行中进程（按 id 去重，新→旧）。无 store 时只返回内存态历史。
func (r *Registry) Snapshot() []Proc {
	r.mu.Lock()
	store := r.historyStore
	r.mu.Unlock()

	// 历史来源：store（若注入）优先，否则内存 history。
	var hist []Proc
	seen := map[string]bool{}
	if store != nil {
		if l, err := store.List(0); err == nil {
			hist = l
			for _, p := range hist {
				seen[p.ID] = true
			}
		}
	}

	r.mu.Lock()
	// 合并本次会话内存 history 中 store 未覆盖的（进程结束后瞬间、写 store 前的记录）。
	for _, p := range r.history {
		if !seen[p.ID] {
			hist = append(hist, *p)
			seen[p.ID] = true
		}
	}
	// 当前运行中进程（开始时间新→旧）。
	running := make([]*Proc, 0, len(r.running))
	for _, p := range r.running {
		running = append(running, p)
	}
	r.mu.Unlock()
	sort.Slice(running, func(i, j int) bool {
		return running[i].StartedAt.After(running[j].StartedAt)
	})

	// 合并历史 + 运行中，整体按「结束/开始时间」新→旧。
	out := make([]Proc, 0, len(hist)+len(running))
	out = append(out, hist...)
	for _, p := range running {
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return timeOf(out[i]).After(timeOf(out[j]))
	})
	return out
}

// timeOf 取进程的排序时间锚点：已结束用结束时间，否则用开始时间。
func timeOf(p Proc) time.Time {
	if !p.EndedAt.IsZero() {
		return p.EndedAt
	}
	return p.StartedAt
}

// procByID 查找运行中的进程（加锁外调用方需保证持锁）。
func (r *Registry) procByID(id string) *Proc {
	return r.running[id]
}

// Running 返回指定 ID 的运行中进程；不存在返回 nil。供终止/查询用。
func (r *Registry) Running(id string) *Proc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[id]
}

// Kill 终止指定运行中的进程（杀整棵进程树）。不存在则返回错误。
func (r *Registry) Kill(id string) error {
	p := r.Running(id)
	if p == nil {
		return errNotFound
	}
	return killProcessTree(p.PID)
}

// Shutdown 终止所有运行中的子进程（退出软件时调用，避免留下孤儿进程）。
// 一次持锁收集全部运行中进程的 PID，再逐个杀进程树，减少锁竞争。
// 返回聚合错误：任一进程终止失败会累积到返回的 error（nil 表示全部成功）。
func (r *Registry) Shutdown() error {
	r.mu.Lock()
	procs := make([]*Proc, 0, len(r.running))
	for _, p := range r.running {
		procs = append(procs, p)
	}
	r.mu.Unlock()

	var errs []error
	for _, p := range procs {
		if p.PID <= 0 {
			continue
		}
		if err := killProcessTree(p.PID); err != nil {
			errs = append(errs, fmt.Errorf("kill %s(pid=%d): %w", p.Name, p.PID, err))
		}
	}
	return errors.Join(errs...)
}

// Run 统一执行入口：异步启动命令并登记到进程表，立即返回 Handle。
// 状态/日志/结束通过订阅广播推送。名称 Name 必填。
func (r *Registry) Run(o Options) (*Handle, error) {
	return runCommandFn(r, o)
}

// defaultRun 是 Run 的默认实现：真正启动子进程。
func (r *Registry) defaultRun(o Options) (*Handle, error) {
	if strings.TrimSpace(o.Name) == "" {
		return nil, errEmptyName
	}
	cmd := exec.Command(o.Cmd, o.Args...)
	cmdutil.HideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	proc := r.add(o, cmd.Process.Pid)
	handle := &Handle{procID: proc.ID, done: make(chan error, 1)}

	go func() {
		var wg sync.WaitGroup
		pipe := func(rc io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(rc)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				line := sc.Text()
				r.appendLog(proc, line)
				if o.OnLog != nil {
					o.OnLog(line)
				}
			}
		}
		wg.Add(2)
		go pipe(stdout)
		go pipe(stderr)
		wg.Wait()
		runErr := cmd.Wait()
		handle.done <- runErr
		r.finish(proc, runErr)
	}()

	return handle, nil
}

// RegisterExisting 登记一个已由外部创建并 Start 的子进程（如 MCP SDK 内部进程）。
// 进程结束后需由外部调用 Unregister(id) 移入历史。返回 Handle 供查询/Kill。
func (r *Registry) RegisterExisting(id, name, kind string, cmd *exec.Cmd) *Handle {
	pid := 0
	if cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	proc := &Proc{
		ID:        id,
		Name:      name,
		Kind:      kind,
		Cmd:       cmdString(cmd),
		PID:       pid,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		Log:       []string{},
	}
	r.mu.Lock()
	r.running[id] = proc
	r.mu.Unlock()
	r.broadcast(Event{Type: "update", Data: []Proc{*proc}})
	return &Handle{procID: id, done: make(chan error, 1)}
}

// Unregister 把登记的外部进程移出运行中并加入历史（status 可指定 done/error）。
// 供 RegisterExisting 的调用方在检测到进程退出后调用。
func (r *Registry) Unregister(id string, runErr error) {
	r.mu.Lock()
	p, ok := r.running[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	p.EndedAt = time.Now()
	if runErr != nil {
		p.Status = StatusError
		p.ExitCode = 1
	} else {
		p.Status = StatusDone
	}
	delete(r.running, id)
	r.history = append([]*Proc{p}, r.history...) // 新→旧
	if len(r.history) > r.historyMax {
		r.history = r.history[:r.historyMax]
	}
	copyProc := *p
	store := r.historyStore
	r.mu.Unlock()
	r.broadcast(Event{Type: "update", Data: []Proc{copyProc}})
	// 持久化到 store（失败不阻断进程结束流程，只丢弃该条写入）。
	if store != nil {
		if err := store.Save(copyProc); err != nil {
			// 无 logger 可用，静默；仅开发期日志可见。写入失败不影响运行。
			_ = err
		}
	}
}

// SetMem 更新运行中进程的内存采样值并广播（节流由调用方控制）。
func (r *Registry) SetMem(id string, mem uint64) {
	r.mu.Lock()
	p, ok := r.running[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	p.MemBytes = mem
	copyProc := *p
	r.mu.Unlock()
	r.broadcast(Event{Type: "update", Data: []Proc{copyProc}})
}

// add 登记一个新启动的进程并广播。
func (r *Registry) add(o Options, pid int) *Proc {
	r.mu.Lock()
	r.seq++
	id := "proc-" + strconv.Itoa(r.seq)
	proc := &Proc{
		ID:        id,
		Name:      o.Name,
		Kind:      o.Kind,
		Cmd:       strings.Join(append([]string{o.Cmd}, o.Args...), " "),
		PID:       pid,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		Log:       []string{},
	}
	r.running[id] = proc
	copyProc := *proc
	r.mu.Unlock()
	r.broadcast(Event{Type: "update", Data: []Proc{copyProc}})
	return proc
}

// appendLog 追加一行日志（带行数上限，超出丢最旧的）。
func (r *Registry) appendLog(p *Proc, line string) {
	r.mu.Lock()
	p.Log = append(p.Log, line)
	if len(p.Log) > r.maxLogLines {
		p.Log = p.Log[len(p.Log)-r.maxLogLines:]
	}
	r.mu.Unlock()
	// 日志不逐行广播（量太大），前端经历史展开时从快照读取。
}

// finish 进程结束：从运行中移除，进历史，广播。
func (r *Registry) finish(p *Proc, runErr error) {
	r.Unregister(p.ID, runErr)
}

// cmdString 还原命令行展示串。
func cmdString(cmd *exec.Cmd) string {
	if cmd == nil {
		return ""
	}
	return strings.Join(append([]string{cmd.Path}, cmd.Args[1:]...), " ")
}
