package unifyai

import (
	"bufio"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// RunEvent 任务的一次事件（SSE 推送载荷）。
type RunEvent struct {
	Type string `json:"type"` // log | done | error
	Line string `json:"line,omitempty"`
	Data string `json:"data,omitempty"`
}

// RunRunner 管理单实例 unifyai 任务：同一时间只允许一个任务在跑，
// 新订阅者加入当前任务流；任务结束广播 done/error 并关闭所有订阅。
// 与 skills.UpdateRunner 同构（skills 包私有实现，此处独立实现避免跨包耦合）。
type RunRunner struct {
	svc     *Service
	mu      sync.Mutex
	running bool
	subs    map[chan RunEvent]bool
}

func newRunRunner(svc *Service) *RunRunner {
	return &RunRunner{svc: svc, subs: map[chan RunEvent]bool{}}
}

// Subscribe 订阅任务日志流。若没有任务在跑则立即启动一个。
// 返回事件 channel；任务结束（done/error 推送后）channel 由广播端关闭。
func (r *RunRunner) Subscribe() (<-chan RunEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		r.running = true
		go r.run()
	}
	ch := make(chan RunEvent, 256)
	r.subs[ch] = true
	return ch, nil
}

// broadcast 向所有订阅者推送事件（非阻塞，满则丢弃，避免慢消费者拖垮任务）。
func (r *RunRunner) broadcast(ev RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// finish 推送终态事件并关闭所有订阅 channel、复位运行状态。
func (r *RunRunner) finish(ev RunEvent) {
	r.broadcast(ev)
	r.mu.Lock()
	for ch := range r.subs {
		close(ch)
	}
	r.subs = map[chan RunEvent]bool{}
	r.running = false
	r.mu.Unlock()
}

// run 执行一次任务并实时广播日志。
// args 为 CLI 参数（由 handler 在启动时通过 setArgs 传入，缺省为 []）。
func (r *RunRunner) run() {
	args := r.svc.takePendingArgs()
	r.broadcast(RunEvent{Type: "log", Line: "开始执行 unifyai…"})
	if err := r.svc.Run(args, func(line string) {
		r.broadcast(RunEvent{Type: "log", Line: line})
	}); err != nil {
		r.finish(RunEvent{Type: "error", Data: err.Error()})
		return
	}
	r.finish(RunEvent{Type: "done", Data: "0"})
}

// runCommandStream 执行命令并逐行回调合并输出（stdout+stderr）。
// 包级变量，测试可替换为 fake。
var runCommandStream = func(name string, args []string, onLine func(string)) error {
	cmd := exec.Command(name, args...)
	// 后台服务 PATH 可能不完整（找不到 npx/node），
	// 把命令所在目录补到 PATH 最前，保证 npx 能找到同目录的 node。
	if runtime.GOOS != "windows" && filepath.IsAbs(name) {
		cmd.Env = osEnvironWithPathPrefix(filepath.Dir(name))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	pipe := func(rc io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			onLine(sc.Text())
		}
	}
	wg.Add(2)
	go pipe(stdout)
	go pipe(stderr)
	wg.Wait()
	return cmd.Wait()
}
