package skills

import (
	"fmt"
	"strings"
	"sync"
)

// UpdateEvent 更新任务的一次事件（SSE 推送载荷）。
type UpdateEvent struct {
	Type string `json:"type"` // log | done | error
	Line string `json:"line,omitempty"`
	Data string `json:"data,omitempty"`
}

// UpdateRunner 管理单实例更新任务：同一时间只允许一个更新在跑，
// 新订阅者加入当前任务流；任务结束广播 done/error 并关闭所有订阅。
// 任务进行中保留已广播的 history，供中途加入的订阅者回放历史日志，
// 让后连接也能看到从任务开始到当前的全部内容。
type UpdateRunner struct {
	svc     *Service
	mu      sync.Mutex
	running bool
	pendingID string // 本次更新任务的前端 task id（空=自动生成），经 procreg 透传
	subs    map[chan UpdateEvent]bool
	history []UpdateEvent
}

func newUpdateRunner(svc *Service) *UpdateRunner {
	return &UpdateRunner{svc: svc, subs: map[chan UpdateEvent]bool{}}
}

// Subscribe 订阅更新日志流。若没有任务在跑则立即启动一个。
// 返回事件 channel；任务结束（done/error 推送后）channel 由广播端关闭。
// SetUpdateID 设置本次更新任务的进程 ID（前端 task id），在下一次启动时使用。
func (r *UpdateRunner) SetUpdateID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingID = id
}

// IsRunning 返回是否有更新任务正在跑。
func (r *UpdateRunner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *UpdateRunner) Subscribe() (<-chan UpdateEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		r.running = true
		go r.run()
	}
	ch := make(chan UpdateEvent, 256)
	// 回放本任务已产生的历史日志，让中途加入的订阅者不丢上下文。
	for _, ev := range r.history {
		select {
		case ch <- ev:
		default: // 缓冲满则丢弃历史，避免阻塞；实时日志不受影响
		}
	}
	r.subs[ch] = true
	return ch, nil
}

// broadcast 向所有订阅者推送事件（非阻塞，满则丢弃，避免慢消费者拖垮任务）。
func (r *UpdateRunner) broadcast(ev UpdateEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	r.history = append(r.history, ev)
}

// finish 推送终态事件并关闭所有订阅 channel、复位运行状态。
func (r *UpdateRunner) finish(ev UpdateEvent) {
	r.broadcast(ev)
	r.mu.Lock()
	for ch := range r.subs {
		close(ch)
	}
	r.subs = map[chan UpdateEvent]bool{}
	r.history = nil
	r.running = false
	r.mu.Unlock()
}

// run 执行一次更新并实时广播日志。
func (r *UpdateRunner) run() {
	r.broadcast(UpdateEvent{Type: "log", Line: "开始检查并更新技能…"})

	r.mu.Lock()
	id := r.pendingID
	r.pendingID = ""
	r.mu.Unlock()
	updates, err := r.svc.UpdateSkills(id, func(line string) {
		r.broadcast(UpdateEvent{Type: "log", Line: line})
	})
	if err != nil {
		r.finish(UpdateEvent{Type: "error", Data: err.Error()})
		return
	}

	if len(updates) == 0 {
		r.broadcast(UpdateEvent{Type: "log", Line: "所有技能已是最新"})
	} else {
		r.broadcast(UpdateEvent{Type: "log", Line: fmt.Sprintf("已更新 %d 个技能：%s", len(updates), strings.Join(updates, ", "))})
	}
	r.finish(UpdateEvent{Type: "done", Data: strings.Join(updates, ",")})
}
