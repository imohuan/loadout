package plugin

import (
	"errors"
	"testing"
	"time"
)

// TestRunBackgroundReturnsError 验证正常返回的错误能通过 channel 取回。
func TestRunBackgroundReturnsError(t *testing.T) {
	ch := RunBackground("test-err", func() error {
		return errors.New("boom")
	})
	select {
	case err := <-ch:
		if err == nil || err.Error() != "boom" {
			t.Fatalf("期望 boom，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：后台任务未返回")
	}
}

// TestRunBackgroundReturnsNil 验证成功路径返回 nil。
func TestRunBackgroundReturnsNil(t *testing.T) {
	ch := RunBackground("test-ok", func() error {
		return nil
	})
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("期望 nil，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：后台任务未返回")
	}
}

// TestRunBackgroundRecoversPanic 验证 goroutine 内 panic 不会崩溃进程，而是返回错误。
func TestRunBackgroundRecoversPanic(t *testing.T) {
	ch := RunBackground("test-panic", func() error {
		panic("kaboom")
	})
	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("期望 panic 被转换为错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：panic 未被捕获")
	}
}
