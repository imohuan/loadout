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

// procreg 全局注册表。
type Registry struct {
	mu      sync.Mutex
	seq     int
	running map[string]*Proc // 运行中的进程
	history []*Proc          // 历史记录（新→旧排序，上限 historyLimit）
	subs    map[chan Event]bool

	maxLogLines int // 每进程日志行数上限
	historyMax  int // 历史记录条数上限
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

// Get 返回全局注册表单例（所有接入点共享）。
func Get() *Registry {
	globalOnce.Do(func() {
		global = New()
	})
	return global
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
func (r *Registry) Snapshot() []Proc {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Proc, 0, len(r.running)+len(r.history))
	// 先历史（新→旧），再运行中（开始时间新→旧）
	for _, p := range r.history {
		out = append(out, *p)
	}
	running := make([]*Proc, 0, len(r.running))
	for _, p := range r.running {
		running = append(running, p)
	}
	sort.Slice(running, func(i, j int) bool {
		return running[i].StartedAt.After(running[j].StartedAt)
	})
	for _, p := range running {
		out = append(out, *p)
	}
	return out
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

// Run 统一执行入口：异步启动命令并登记到进程表，立即返回 Handle。
// 状态/日志/结束通过订阅广播推送。名称 Name 必填。
func (r *Registry) Run(o Options) (*Handle, error) {
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
	r.mu.Unlock()
	r.broadcast(Event{Type: "update", Data: []Proc{copyProc}})
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
