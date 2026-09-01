package procreg

import (
	"testing"
	"time"
)

// 验证：当某个 SSE 订阅者消费慢（channel 积压满 64），后续广播事件会被静默丢弃。
// 这是 procreg.broadcast 的非阻塞丢弃设计。模拟「内存采样频繁广播 + 安装 done 事件」
// 在慢消费场景下，done 事件是否会丢。
func TestBroadcastDropsWhenChannelFull(t *testing.T) {
	r := New()

	// 慢消费者：订阅后不消费，让 channel 积压。
	ch := r.Subscribe()
	_ = ch // 故意不读，模拟 SSE handler 卡住/网络慢

	// 广播超过容量的事件（模拟内存采样 SetMem + 其他进程 update）。
	// channel 容量 64，这里广播 64 次塞满。
	for i := 0; i < 64; i++ {
		r.broadcast(Event{Type: "update"})
	}

	// 关键：此时再广播 install 的 done 事件。
	r.broadcast(Event{Type: "update"})

	// 消费端恢复，尝试读取：能读到几个？
	timeout := time.NewTimer(100 * time.Millisecond)
	defer timeout.Stop()
	got := 0
	for {
		select {
		case <-ch:
			got++
		case <-timeout.C:
			t.Logf("慢消费场景：channel 积压 64 条后，第 65 条(done)读到了=%d 条", got)
			if got <= 64 {
				t.Fatalf("FAIL: 第 65 条事件(相当于 install done)被丢弃，只读到 %d 条 <= 64", got)
			}
			return
		}
	}
}
