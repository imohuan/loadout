package adminapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildElevatedBatch 验证提权脚本是纯 CMD 语法：call / >> / exit /b。
// 旧方案用 PowerShell，引号、制表符、中文注释任何一处出错都会让脚本
// 语法报错、窗口一闪就退，用户看不到任何信息。
func TestBuildElevatedBatch(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	batch := buildElevatedBatch(
		`C:\Users\x\AppData\Roaming\npm\ocx.cmd`,
		[]string{"service", "install"},
		logPath,
	)

	if !strings.Contains(batch, "call ") {
		t.Errorf("缺少 call 调用:\n%s", batch)
	}
	if !strings.Contains(batch, ">> ") || !strings.Contains(batch, logPath) {
		t.Errorf("缺少日志重定向:\n%s", batch)
	}
	if !strings.Contains(batch, "%ERRORLEVEL%") || !strings.Contains(batch, "exit /b") {
		t.Errorf("缺少退出码传递:\n%s", batch)
	}

	for _, bad := range []string{"Write-Output", "Out-File", "Start-Process", "{0}"} {
		if strings.Contains(batch, bad) {
			t.Errorf("不应出现 PowerShell 语法 %q:\n%s", bad, batch)
		}
	}
}

// TestBuildElevatedBatchQuotesSpaces 路径含空格时必须加双引号。
func TestBuildElevatedBatchQuotesSpaces(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "my log.log")
	batch := buildElevatedBatch(`C:\Program Files\ocx.cmd`, nil, logPath)
	if !strings.Contains(batch, `"C:\Program Files\ocx.cmd"`) {
		t.Errorf("含空格的命令路径应加双引号:\n%s", batch)
	}
	if !strings.Contains(batch, "\""+logPath+"\"") {
		t.Errorf("含空格的日志路径应加双引号:\n%s", batch)
	}
}

// TestCmdQuote 基础引号规则。
func TestCmdQuote(t *testing.T) {
	if got := cmdQuote("service"); got != "service" {
		t.Errorf("无空格不应加引号，实际 %q", got)
	}
	if got := cmdQuote(`C:\x y\a.cmd`); got != `"C:\x y\a.cmd"` {
		t.Errorf("含空格应加双引号，实际 %q", got)
	}
}

// TestElevatedLogPath 日志写在临时目录，文件名可辨认。
func TestElevatedLogPath(t *testing.T) {
	p := ocxElevatedLogPath("opencodex:service-install:123")
	if !strings.HasSuffix(p, ".log") {
		t.Errorf("应以 .log 结尾，实际 %s", p)
	}
	if !strings.Contains(filepath.Base(p), "opencodex") {
		t.Errorf("文件名应含 opencodex，实际 %s", p)
	}
	if dir := filepath.Dir(p); dir != os.TempDir() {
		t.Errorf("应写在 %s，实际 %s", os.TempDir(), dir)
	}
}

// TestOcxActionTableRequiresElevation 服务类行动作提权；gui/sync/shim 不用。
func TestOcxActionTableRequiresElevation(t *testing.T) {
	for _, a := range []string{"service-install", "service-repair", "service-start", "service-stop", "service-uninstall", "start", "stop"} {
		if !ocxElevatedActions[a] {
			t.Errorf("%s 应标记为需要管理员权限", a)
		}
	}
	for _, a := range []string{"gui", "sync", "shim-install", "shim-uninstall"} {
		if ocxElevatedActions[a] {
			t.Errorf("%s 不需要提权", a)
		}
	}
}

// TestTailLogReadsIncremental 验证日志跟随：分两次写入，逐行回调必须都能收到。
func TestTailLogReadsIncremental(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(logPath, []byte("line-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []string
	stop := make(chan struct{})
	finished := false

	done := make(chan struct{})
	go func() {
		tailLog(logPath, 0, func(line string) {
			got = append(got, line)
		}, stop, func() bool { return finished })
		close(done)
	}()

	// 追加第二批内容（模拟命令继续输出）。
	time.Sleep(350 * time.Millisecond)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("line-2\nline-3\n")
	_ = f.Close()

	// 收尾：把 finished 置真，让 tailLog 读完残留后退出。
	time.Sleep(400 * time.Millisecond)
	finished = true

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tailLog 未退出")
	}

	if len(got) < 3 {
		t.Fatalf("应收到至少 3 行，实际 %d: %v", len(got), got)
	}
	for i, want := range []string{"line-1", "line-2", "line-3"} {
		if got[i] != want {
			t.Errorf("第 %d 行应为 %q，实际 %q", i+1, want, got[i])
		}
	}
}
