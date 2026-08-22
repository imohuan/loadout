package visionv2

import (
	"log/slog"
	"sync"
	"testing"
)

// TestStateConcurrentAccess 验证 state() 的并发安全与按 requestID 隔离：
// 20 个 goroutine 并发访问 10 个不同 requestID，无 race 且返回非 nil。
func TestStateConcurrentAccess(t *testing.T) {
	svc := NewService(nil, nil, slog.Default())
	const ids = 10
	const workers = 20

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < ids; j++ {
				id := string(rune('a' + j)) // "a".."j"
				st := svc.state(id)
				if st == nil {
					t.Errorf("state(%q) = nil, want non-nil", id)
					return
				}
			}
		}()
	}
	wg.Wait()

	// 全部 10 个 requestID 都应已建立独立状态。
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.states) != ids {
		t.Fatalf("states 数量 = %d, want %d", len(svc.states), ids)
	}
}

// TestDropState 验证 dropState 之后再次 state 拿到的是新对象（指针不同）。
func TestDropState(t *testing.T) {
	svc := NewService(nil, nil, slog.Default())

	first := svc.state("a")
	if first == nil {
		t.Fatal("state(a) = nil, want non-nil")
	}
	svc.dropState("a")

	second := svc.state("a")
	if second == nil {
		t.Fatal("state(a) after drop = nil, want non-nil")
	}
	if first == second {
		t.Fatal("dropState 后 state(a) 返回了同一指针，期望新对象")
	}
}
