package keyratelimit

import (
	"testing"
	"time"
)

// TestCounterSlidingWindowBoundary 滑动窗口边界：第 max+1 次判定为「已满」，
// 时间前移使旧记录滑出窗口后重新可用。
func TestCounterSlidingWindowBoundary(t *testing.T) {
	c := newWindowCounter()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	window := 60 * time.Second
	const max = 20

	// 先记满 20 次：每次记录前都应判定为「未满」，每次间隔 1 秒。
	for i := 0; i < max; i++ {
		if used := c.used("ch", max, window, now); used >= max {
			t.Fatalf("第 %d 次前 used=%d，应 < %d", i+1, used, max)
		}
		if got := c.record("ch", max, window, now); got != i+1 {
			t.Fatalf("第 %d 次 record 返回 %d，期望 %d", i+1, got, i+1)
		}
		now = now.Add(time.Second)
	}
	if used := c.used("ch", max, window, now); used != max {
		t.Fatalf("记满后 used=%d，期望 %d", used, max)
	}

	// 整体滑出窗口：全部旧记录失效。
	if used := c.used("ch", max, window, now.Add(61*time.Second)); used != 0 {
		t.Fatalf("窗口整体滑出后 used=%d，期望 0（应重新可用）", used)
	}
}

// TestCounterPartialSlide 部分记录滑出时按剩余条数收窄。
func TestCounterPartialSlide(t *testing.T) {
	c := newWindowCounter()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	const window = 60 * time.Second
	const max = 5

	for i := 0; i < 3; i++ {
		c.record("ch", max, window, base)
	}
	if used := c.used("ch", max, window, base.Add(30*time.Second)); used != 3 {
		t.Fatalf("t=30s used=%d，期望 3", used)
	}
	if used := c.used("ch", max, window, base.Add(61*time.Second)); used != 0 {
		t.Fatalf("t=61s used=%d，期望 0", used)
	}
}

// TestCounterMaxZeroNotLimited max<=0（未配参数）时不计流、不建桶。
func TestCounterMaxZeroNotLimited(t *testing.T) {
	c := newWindowCounter()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if used := c.record("ch", 0, time.Minute, now); used != 0 {
			t.Fatalf("max=0 时 record 返回 %d，期望 0（不计数）", used)
		}
	}
	if got := c.used("ch", 0, time.Minute, now); got != 0 {
		t.Fatalf("max=0 时 used=%d，期望 0", got)
	}
	if c.size() != 0 {
		t.Fatalf("max=0 不应建立计数桶，实际 %d 个", c.size())
	}
}

// TestCounterBucketMemoryBounded 单桶内存有界：远超上限也不无限增长。
func TestCounterBucketMemoryBounded(t *testing.T) {
	c := newWindowCounter()
	now := time.Now()
	const max = 3
	for i := 0; i < 1000; i++ {
		c.record("ch", max, time.Hour, now) // 同一时刻，永不滑出
	}
	c.mu.Lock()
	n := len(c.buckets["ch"].at)
	c.mu.Unlock()
	if n > max+1 {
		t.Fatalf("单桶保留 %d 条时刻，超过 max+1=%d（内存未封顶）", n, max+1)
	}
}

// TestCounterSweepReclaimsIdle 清扫回收空闲桶、保留活跃桶。
func TestCounterSweepReclaimsIdle(t *testing.T) {
	c := newWindowCounter()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	c.record("idle", 5, time.Minute, base)                    // 很旧
	c.record("active", 5, time.Minute, base.Add(2*time.Hour)) // 新鲜

	removed := c.sweep(time.Hour, base.Add(2*time.Hour))
	if removed != 1 {
		t.Fatalf("应回收 1 个空闲桶，实际 %d", removed)
	}
	if c.size() != 1 {
		t.Fatalf("回收后应剩 1 个桶，实际 %d", c.size())
	}
	if used := c.used("active", 5, time.Minute, base.Add(2*time.Hour)); used != 1 {
		t.Fatalf("活跃桶的计数不应被清扫破坏，used=%d", used)
	}
}

