package mcphub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestLogMgr 构造一个 root 指向临时目录、maxSize 可注入的 LogManager。
// 不用 t.TempDir()：Windows 上句柄关闭后杀毒/索引服务可能短暂占用文件，
// testing 框架的 TempDir RemoveAll 会无限重试导致 go test 进程挂住。
// 改为自建目录 + cleanup 主动关句柄 + 尽力删除（失败忽略，残留系统 temp 无害）。
func newTestLogMgr(t *testing.T, maxSize int64) *LogManager {
	t.Helper()
	dir, err := os.MkdirTemp("", "mcplog-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	m := NewLogManager(dir)
	m.maxSize = maxSize
	t.Cleanup(func() {
		m.CloseAll()
		for i := 0; i < 3; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return m
}

func TestLogManagerWriteReadIncremental(t *testing.T) {
	m := newTestLogMgr(t, 32*1024*1024)
	m.Ensure("s1", "github")
	m.Write("s1", "CONNECT", "transport", "http", "url", "http://localhost:8080")
	m.Write("s1", "CONNECT-OK", "pid", 123)
	m.Write("s1", "FRAME→", "id", 1, "method", "tools/call", "params", `{"name":"x"}`)

	files := m.ListFiles("github")
	if len(files) != 1 {
		t.Fatalf("段文件数 = %d，期望 1（未滚动）", len(files))
	}
	seg := files[0].Name

	// 全量读
	data, size, eof, err := m.Read("github", seg, 0, 0)
	if err != nil {
		t.Fatalf("Read 全量: %v", err)
	}
	if !eof {
		t.Fatal("全量读应 eof=true")
	}
	if int64(len(data)) != size {
		t.Fatalf("data len = %d, size = %d", len(data), size)
	}
	content := string(data)
	for _, want := range []string{"[CONNECT]", "[CONNECT-OK]", "[FRAME→]", "transport=", "url="} {
		if !strings.Contains(content, want) {
			t.Fatalf("日志内容缺 %q，实际:\n%s", want, content)
		}
	}
	if strings.Contains(content, "http://localhost:8080") == false {
		t.Fatalf("url 未落盘:\n%s", content)
	}

	// 增量读：读前半，再读后半，拼起来 = 全量
	half := size / 2
	d1, size1, eof1, err := m.Read("github", seg, 0, half)
	if err != nil {
		t.Fatalf("Read 前半: %v", err)
	}
	if size1 != size {
		t.Fatalf("前半 size = %d，期望 %d", size1, size)
	}
	if eof1 {
		t.Fatal("前半不应 eof")
	}
	d2, _, eof2, err := m.Read("github", seg, half, 0)
	if err != nil {
		t.Fatalf("Read 后半: %v", err)
	}
	if !eof2 {
		t.Fatal("后半应 eof")
	}
	if string(d1)+string(d2) != content {
		t.Fatal("增量拼接 != 全量")
	}

	// offset 超过段大小 → 空 + eof
	d3, _, eof3, err := m.Read("github", seg, size+100, 0)
	if err != nil {
		t.Fatalf("Read 超界: %v", err)
	}
	if len(d3) != 0 || !eof3 {
		t.Fatalf("超界读应空+eof，实际 len=%d eof=%v", len(d3), eof3)
	}

	// 非法段名 → 错误
	if _, _, _, err := m.Read("github", "../evil.log", 0, 0); err == nil {
		t.Fatal("非法段名 ../evil.log 应返回错误")
	}
	if _, _, _, err := m.Read("github", "not-a-segment", 0, 0); err == nil {
		t.Fatal("非法段名 not-a-segment 应返回错误")
	}
}

func TestLogManagerRollOnMaxSize(t *testing.T) {
	// maxSize 很小：写 10 条长行必然触发滚动。
	m := newTestLogMgr(t, 256)
	m.Ensure("s1", "github")
	for i := 0; i < 10; i++ {
		m.Write("s1", "FRAME→", "id", i+1, "method", "tools/call",
			"params", fmt.Sprintf(`{"payload":"%s"}`, strings.Repeat("x", 64)))
	}

	files := m.ListFiles("github")
	if len(files) < 2 {
		t.Fatalf("段文件数 = %d，期望 ≥2（已滚动）", len(files))
	}
	// 首段必须是无后缀文件（段号 1），后续为 -N 递增
	if segmentSeq(files[0].Name) != 1 {
		t.Fatalf("首段应无后缀（段号 1），实际 %v", files[0].Name)
	}
	for i := 1; i < len(files); i++ {
		if segmentSeq(files[i].Name) != i+1 {
			t.Fatalf("段 %d 段号 = %d（%v），期望 %d", i, segmentSeq(files[i].Name), files[i].Name, i+1)
		}
	}
	// 旧段（第一段）内容仍可读
	data, size, _, err := m.Read("github", files[0].Name, 0, 0)
	if err != nil || size == 0 {
		t.Fatalf("旧段读取失败: err=%v size=%d", err, size)
	}
	if strings.Contains(string(data), "[FRAME→]") == false {
		t.Fatalf("旧段应含 FRAME 行:\n%s", string(data))
	}
	// 所有段都有日志行
	total := 0
	for _, f := range files {
		d, _, _, err := m.Read("github", f.Name, 0, 0)
		if err != nil {
			t.Fatalf("读段 %s: %v", f.Name, err)
		}
		total += strings.Count(string(d), "[FRAME→]")
	}
	if total != 10 {
		t.Fatalf("FRAME→ 总行数 = %d，期望 10（滚动不丢行）", total)
	}
}

func TestLogManagerEnsureIdempotentAndRemove(t *testing.T) {
	m := newTestLogMgr(t, 32*1024*1024)
	m.Ensure("s1", "github")
	m.Write("s1", "CONNECT", "transport", "stdio", "cmd", "npx")
	m.Close("s1") // 停用

	// Ensure 幂等：重建后续写同一文件（base 冻结，不新建）
	m.Ensure("s1", "github")
	m.Write("s1", "CONNECT-OK")
	files := m.ListFiles("github")
	if len(files) != 1 {
		t.Fatalf("Ensure 重建后段数 = %d，期望 1（续写不新建）", len(files))
	}
	data, _, _, _ := m.Read("github", files[0].Name, 0, 0)
	if !strings.Contains(string(data), "cmd=") || !strings.Contains(string(data), "[CONNECT-OK]") {
		t.Fatalf("续写内容缺失:\n%s", string(data))
	}

	// Remove 后目录消失
	m.RemoveServerLogs("github")
	if _, err := os.Stat(filepath.Join(m.root, "github")); !os.IsNotExist(err) {
		t.Fatal("RemoveServerLogs 后目录仍存在")
	}
	if len(m.ListServers()) != 0 {
		t.Fatal("RemoveServerLogs 后 ListServers 应为空")
	}
	// Remove 后 Ensure 可重建（新 base）
	m.Ensure("s1", "github")
	m.Write("s1", "CONNECT", "transport", "stdio", "cmd", "npx")
	if len(m.ListFiles("github")) != 1 {
		t.Fatal("Remove 后重建失败")
	}
}

// TestLogManagerConcurrentWriteAndRoll 验证并发写 + 滚动不丢行。
// 注意：maxSize 不宜太小——Windows Defender 实时扫描新建 .log 文件，
// 极端滚动频率（几十次 OpenFile 新文件）会偶发长时间阻塞，测试进程看似卡死
//（实测 maxSize=512 滚 67 段挂 ~50%，4KB 滚 4 段 0 挂）。逻辑覆盖优先，频率适中。
func TestLogManagerConcurrentWriteAndRoll(t *testing.T) {
	m := newTestLogMgr(t, 4*1024)
	m.Ensure("s1", "github")
	const goroutines = 8
	const perGoroutine = 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				m.Write("s1", "FRAME→", "id", g*1000+i, "method", "tools/call",
					"params", fmt.Sprintf(`{"g":%d,"i":%d,"pad":"%s"}`, g, i, strings.Repeat("y", 32)))
			}
		}(g)
	}
	wg.Wait()

	files := m.ListFiles("github")
	if len(files) < 2 {
		t.Fatalf("并发写应触发滚动，段数 = %d", len(files))
	}
	total := 0
	for _, f := range files {
		d, _, _, err := m.Read("github", f.Name, 0, 0)
		if err != nil {
			t.Fatalf("读段 %s: %v", f.Name, err)
		}
		total += strings.Count(string(d), "[FRAME→]")
	}
	if total != goroutines*perGoroutine {
		t.Fatalf("FRAME→ 总行数 = %d，期望 %d（并发滚动不丢行）", total, goroutines*perGoroutine)
	}
}

func TestLogManagerRemoveDuringWrite(t *testing.T) {
	m := newTestLogMgr(t, 32*1024*1024)
	m.Ensure("s1", "github")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			m.Write("s1", "FRAME→", "id", i+1, "method", "tools/call", "params", `{}`)
		}
	}()
	// 写入进行中删除（反复触发，覆盖不同时序）
	for i := 0; i < 5; i++ {
		m.RemoveServerLogs("github")
		m.Ensure("s1", "github")
	}
	wg.Wait()
	// 删除后目录要么不存在、要么重新 Ensure 重建——不 panic 即可
	_ = m.ListFiles("github")
	_ = m.ListServers()
}

func TestMaskSecretAndSegmentHelpers(t *testing.T) {
	if got := maskSecret("abcdefgh"); got != "****" {
		t.Fatalf("maskSecret 8 位 = %q，期望 ****", got)
	}
	if got := maskSecret("1234567890abcdef"); got != "1234****cdef" {
		t.Fatalf("maskSecret 16 位 = %q，期望 1234****cdef", got)
	}
	if got := segmentSeq("20260824-144325.log"); got != 1 {
		t.Fatalf("segmentSeq 无后缀 = %d，期望 1", got)
	}
	if got := segmentSeq("20260824-144325-3.log"); got != 3 {
		t.Fatalf("segmentSeq -3 = %d，期望 3", got)
	}
	// 固定名 main.log / main-N.log
	if got := segmentSeq("main.log"); got != 1 {
		t.Fatalf("segmentSeq(main.log) = %d，期望 1", got)
	}
	if got := segmentSeq("main-2.log"); got != 2 {
		t.Fatalf("segmentSeq(main-2.log) = %d，期望 2", got)
	}
	if got := firstTSFromSegment("main.log"); got != "" {
		t.Fatalf("firstTSFromSegment(main.log) = %q，期望空串", got)
	}
	if got := firstTSFromSegment("20260824-144325.log"); got != "2026-08-24 14:43:25" {
		t.Fatalf("firstTSFromSegment = %q", got)
	}
	if got := firstTSFromSegment("20260824-144325-2.log"); got != "2026-08-24 14:43:25" {
		t.Fatalf("firstTSFromSegment(-2) = %q", got)
	}
}


func TestBuildLogLine(t *testing.T) {
	line := buildLogLine("CONNECT", "transport", "http", "url", "http://x")
	if !strings.HasPrefix(line, "20") || !strings.Contains(line, " [CONNECT] ") {
		t.Fatalf("行格式异常: %q", line)
	}
	if !strings.HasSuffix(line, "\n") {
		t.Fatal("行应以换行结尾")
	}
}
