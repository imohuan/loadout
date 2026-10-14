package keyratelimit

import (
	"sync"
	"time"
)

// windowCounter 滑动窗口计数器（纯内存，进程重启清零）。
//
// 每个计数桶 = (渠道 Key, 作用域签名)，桶内只保存最近若干次请求的时刻。判定「窗口内用了
// 几次」时先丢掉滑出窗口的旧时刻，再看剩下的条数。相比「按整分钟切割 + 一个整数」，滑动
// 窗口不会出现「整点前一秒打满、整点后又打满」的两倍突刺。
//
// 内存有界：每个桶最多留 max+1 个时刻——判定只需要知道「是否已经 >= max」，多留一个刚好
// 够用，不需要记录全部历史。后台清扫（sweep）删掉「最后一条记录也已滑出窗口」的空桶，
// 避免渠道数量多时内存只增不减。
//
// 并发：一把互斥锁串行化全部操作。限流在每个请求的热路径上，但操作只是切片裁剪 + 追加，
// 纳秒级，远小于一次上游 HTTP 往返，不需要更细粒度的锁。
type windowCounter struct {
	mu      sync.Mutex
	buckets map[string]*ringBucket
}

// ringBucket 单个计数桶：保存最近的请求时刻（最多 cap 个）。
type ringBucket struct {
	at  []time.Time // 长度 <= cap；at[0] 最旧
	cap int
}

func newWindowCounter() *windowCounter {
	return &windowCounter{buckets: map[string]*ringBucket{}}
}

// normalizeWindow 窗口口径兜底：<=0 时按 1 分钟处理。
func normalizeWindow(window time.Duration) time.Duration {
	if window <= 0 {
		return time.Minute
	}
	return window
}

// trimLocked 丢掉桶内滑出窗口的时刻（调用方须持锁）。
func (b *ringBucket) trimLocked(cutoff time.Time) {
	kept := b.at[:0]
	for _, t := range b.at {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.at = kept
}

// used 返回某计数桶在当前滑动窗口内的已用次数（不改变计数）。
// 上限封顶在 max+1：调用方只需判断「是否 >= max」，桶里也不会保留更多。
func (c *windowCounter) used(key string, max int, window time.Duration, now time.Time) int {
	if max <= 0 {
		return 0
	}
	window = normalizeWindow(window)
	cutoff := now.Add(-window)

	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buckets[key]
	if b == nil {
		return 0
	}
	b.trimLocked(cutoff)
	if len(b.at) == 0 {
		delete(c.buckets, key)
		return 0
	}
	if len(b.at) > max {
		return max + 1
	}
	return len(b.at)
}

// record 记下一次请求（滑动窗口计数 +1），返回记录后的窗口内已用次数。
// max <= 0 视为不限流：不计数，返回 0。
//
// 桶容量封顶在 max+1：已经记满 max+1 条时不再追加（再多也只用来判断「远超上限」），
// 但仍会把最旧的一条挤掉以保持「窗口内确有这么多」的语义。
func (c *windowCounter) record(key string, max int, window time.Duration, now time.Time) int {
	if max <= 0 {
		return 0
	}
	window = normalizeWindow(window)
	cutoff := now.Add(-window)

	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buckets[key]
	if b == nil {
		b = &ringBucket{cap: max + 1}
		c.buckets[key] = b
	}
	b.trimLocked(cutoff)
	limit := b.cap
	if len(b.at) >= limit {
		// 已记满：挤掉最旧一条再追加，长度保持 limit。
		copy(b.at, b.at[1:])
		b.at = b.at[:limit-1]
	}
	b.at = append(b.at, now)
	if len(b.at) > max {
		return max + 1
	}
	return len(b.at)
}

// sweep 清理「最后一条记录也已滑出窗口」的空桶。window 由调用方传入全部规则里最长的窗口
// （短窗口的桶必然也早就空了）。返回删除的桶数（仅用于日志）。
func (c *windowCounter) sweep(window time.Duration, now time.Time) int {
	cutoff := now.Add(-normalizeWindow(window))

	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	for key, b := range c.buckets {
		b.trimLocked(cutoff)
		if len(b.at) == 0 {
			delete(c.buckets, key)
			removed++
		}
	}
	return removed
}

// size 当前桶数量（测试/调试用）。
func (c *windowCounter) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buckets)
}

// concurrencyCounter 在途并发计数器（纯内存，进程重启清零）。
//
// 每个计数键 = (渠道 Key, 作用域签名)，值为「当前正在进行中的请求数」。占用/释放必须
// 严格配对：占位在 proxy:before-attempt（真正要发请求时），释放在 proxy:attempt-finished
// （核心用 defer 保证每次尝试结束都会触发）。
//
// 与 windowCounter 分开存：两个维度独立配置，也不该互相影响清扫。
type concurrencyCounter struct {
	mu       sync.Mutex
	inFlight map[string]int
}

func newConcurrencyCounter() *concurrencyCounter {
	return &concurrencyCounter{inFlight: map[string]int{}}
}

// used 返回当前在途数（不改变计数）。
func (c *concurrencyCounter) used(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[key]
}

// acquire 尝试占一个并发名额。返回 (是否占到, 占位后的在途数)。
// max <= 0 视为不限并发：不占位，返回 (true, 0)（调用方据此跳过释放）。
func (c *concurrencyCounter) acquire(key string, max int) (bool, int) {
	if max <= 0 {
		return true, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.inFlight[key]
	if cur >= max {
		return false, cur
	}
	c.inFlight[key] = cur + 1
	return true, cur + 1
}

// release 释放一个并发名额（在途数减 1，减到 0 时删掉该键）。
// 未占位却调用（如 max<=0 时跳过占位）也不会把计数压成负数。
func (c *concurrencyCounter) release(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.inFlight[key]
	if !ok {
		return 0
	}
	if cur <= 1 {
		delete(c.inFlight, key)
		return 0
	}
	c.inFlight[key] = cur - 1
	return cur - 1
}

// size 当前键数量（测试/调试用）。
func (c *concurrencyCounter) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inFlight)
}
