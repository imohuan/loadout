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
type UpdateRunner struct {
	svc     *Service
	mu      sync.Mutex
	running bool
	subs    map[chan UpdateEvent]bool
}

func newUpdateRunner(svc *Service) *UpdateRunner {
	return &UpdateRunner{svc: svc, subs: map[chan UpdateEvent]bool{}}
}

// Subscribe 订阅更新日志流。若没有任务在跑则立即启动一个。
// 返回事件 channel；任务结束（done/error 推送后）channel 由广播端关闭。
func (r *UpdateRunner) Subscribe() (<-chan UpdateEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		r.running = true
		go r.run()
	}
	ch := make(chan UpdateEvent, 256)
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
}

// finish 推送终态事件并关闭所有订阅 channel、复位运行状态。
func (r *UpdateRunner) finish(ev UpdateEvent) {
	r.broadcast(ev)
	r.mu.Lock()
	for ch := range r.subs {
		close(ch)
	}
	r.subs = map[chan UpdateEvent]bool{}
	r.running = false
	r.mu.Unlock()
}

// run 执行一次更新并实时广播日志。
func (r *UpdateRunner) run() {
	r.broadcast(UpdateEvent{Type: "log", Line: "开始检查并更新技能…"})

	updates, err := r.svc.UpdateSkills(func(line string) {
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
