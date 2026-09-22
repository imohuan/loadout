//go:build ignore

// 调查记录，不是可跑的回归测试：本文件故意断言「第 65 条事件被丢弃」会失败，用来演示
// procreg.broadcast 在 channel 满时的非阻塞丢弃行为（见同目录 research.md）。
//
// 它的 package 声明是 procreg，但目录在 docs/tmp 下、没有该包的其他源文件，所以直接
// `go test ./...` / `go vet ./...` 会编译失败（undefined: New / Event），把整个 CI 拖挂。
// 加 `//go:build ignore` 使其不参与构建，与同目录 verify_run.go 的既有做法一致。
//
// 要真正跑一遍验证该行为：先把本文件复制进 core/procreg/，再 go test -run TestBroadcastDropsWhenChannelFull。
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
