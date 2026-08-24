package mcphub

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultLogMaxSize 单个日志文件上限（默认 32MB，滚动到 -2/-3… 段文件续写）。
const defaultLogMaxSize = 32 * 1024 * 1024

// maxLogLineBytes 单行硬上限（保险丝；正常行远小于此，防极端超长 JSON 打爆文件）。
const maxLogLineBytes = 4 * 1024 * 1024

// segmentNameRe 合法段文件名：main.log / main-N.log（固定名），
// 或兼容旧格式 <YYYYMMDD-HHMMSS>.log / <...>-N.log（N 为段号捕获组）。
var segmentNameRe = regexp.MustCompile(`^(?:main|[0-9]{8}-[0-9]{6})(?:-([0-9]+))?\.log$`)

// LogFileInfo 一个段文件的信息（API 与 UI 展示用）。
type LogFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	FirstTS string `json:"first_ts"` // 从文件名时间戳解析（本地时区）
	LastTS  string `json:"last_ts"`  // 文件 mtime
	Active  bool   `json:"active,omitempty"`
}

// LogServerInfo 一个有日志文件的 server（API 下拉列表项）。
type LogServerInfo struct {
	Name      string        `json:"name"`
	Transport string        `json:"transport,omitempty"`
	Files     []LogFileInfo `json:"files"`
}

// ServerLog 一个 server 的会话日志：追加写，单文件达 maxSize 滚动到 -N 新文件。
// mu 串行化 write 与 roll；Read/ListFiles 不经过本锁（独立 os.Open 读）。
type ServerLog struct {
	mu       sync.Mutex
	f        *os.File
	seq      int   // 当前段序号（1 = 无 -N 后缀）
	size     int64 // 当前活跃段已写字节
	base     string
	name     string
	root     string // 日志根目录 <root>/<name> 下的 root 部分
	maxSize  int64
}

// LogManager 管理全部 server 的会话日志。
// 两层锁职责分开：m.mu 只保护 map（增删查，短暂持有）；ServerLog.mu 串行化文件写。
type LogManager struct {
	mu      sync.Mutex
	logs    map[string]*ServerLog // key: serverID
	root    string                // filepath.Join(config.LogsDir, "mcp")
	maxSize int64
}

// NewLogManager 创建日志管理器。root 为空时 Write 全部静默跳过（防测试污染）。
func NewLogManager(root string) *LogManager {
	return &LogManager{logs: map[string]*ServerLog{}, root: root, maxSize: defaultLogMaxSize}
}

// Ensure 取（或创建）指定 server 的日志写入器：目录/文件已存在则续开最新段，
// 否则以当前时间作为首写时间戳新建。幂等；Remove 后可重建。
// root 为空（config.LogsDir 未初始化，如单测环境）时返回 nil——与 Write 一致静默跳过。
func (m *LogManager) Ensure(serverID, serverName string) *ServerLog {
	if m.root == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sl, ok := m.logs[serverID]; ok {
		sl.ensureOpen()
		return sl
	}
	sl := &ServerLog{
		seq:     1,
		name:    serverName,
		root:    m.root,
		maxSize: m.maxSize,
	}
	sl.ensureOpen()
	m.logs[serverID] = sl
	return sl
}

// Write 追加一条日志行；server 无日志（从未 Ensure / 已 Close）时静默跳过。
// 回调路径要求：本方法必须轻量（mcpkit 的 stderr reader / os/exec Wait 会等它）。
func (m *LogManager) Write(serverID, kind string, fields ...any) {
	if m.root == "" {
		return
	}
	m.mu.Lock()
	sl := m.logs[serverID]
	m.mu.Unlock()
	if sl == nil {
		return
	}
	sl.write(kind, fields...)
}

// Close 关闭指定 server 的写句柄并从 map 移除（server 停用/删除）。
// 之后再次 Ensure 会从磁盘续开最新段。
func (m *LogManager) Close(serverID string) {
	m.mu.Lock()
	sl := m.logs[serverID]
	delete(m.logs, serverID)
	m.mu.Unlock()
	if sl != nil {
		sl.close()
	}
}

// CloseAll 关闭全部写句柄（Service.Close 时调用）。
func (m *LogManager) CloseAll() {
	m.mu.Lock()
	doomed := make([]*ServerLog, 0, len(m.logs))
	for _, sl := range m.logs {
		doomed = append(doomed, sl)
	}
	m.logs = map[string]*ServerLog{}
	m.mu.Unlock()
	for _, sl := range doomed {
		sl.close()
	}
}

// RemoveServerLogs 按 server 名关句柄并删除整个日志目录（server 删除联动）。
// serverName 必须为单段名（调用方负责校验，见 admin-api 端点）。
func (m *LogManager) RemoveServerLogs(serverName string) {
	if m.root == "" || serverName == "" || filepath.Base(serverName) != serverName {
		return
	}
	m.mu.Lock()
	var doomed []*ServerLog
	for id, sl := range m.logs {
		if sl.name == serverName {
			delete(m.logs, id)
			doomed = append(doomed, sl)
		}
	}
	m.mu.Unlock()
	for _, sl := range doomed {
		sl.close()
	}
	_ = os.RemoveAll(filepath.Join(m.root, serverName))
}

// ListServers 返回日志根下所有含段文件的 server 目录名（API 下拉列表用）。
func (m *LogManager) ListServers() []string {
	if m.root == "" {
		return nil
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(m.root, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range sub {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".log") {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// ListFiles 列出该 server 的全部段文件（含历史段；按段名排序 = 时间序）。
func (m *LogManager) ListFiles(serverName string) []LogFileInfo {
	if m.root == "" || serverName == "" || filepath.Base(serverName) != serverName {
		return nil
	}
	dir := filepath.Join(m.root, serverName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	activeSeq := map[string]int{}
	for _, sl := range m.logs {
		if sl.name == serverName {
			activeSeq[serverName] = sl.seq
		}
	}
	m.mu.Unlock()
	out := make([]LogFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") || !segmentNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, LogFileInfo{
			Name:    e.Name(),
			Size:    info.Size(),
			FirstTS: firstTSFromSegment(e.Name()),
			LastTS:  info.ModTime().Format("2006-01-02 15:04:05"),
			Active:  activeSeq[serverName] == segmentSeq(e.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		// 按段号升序 = 时间序（字典序在段数 >9 时会错乱：-10 < -2）。
		return segmentSeq(out[i].Name) < segmentSeq(out[j].Name)
	})
	return out
}

// Read 增量读指定段文件：从 offset 起读 limit 字节（append-only，读不锁写）。
// 返回 (数据, 段总字节, eof, err)；offset 超过段大小返回空 + eof=true。
func (m *LogManager) Read(serverName, segment string, offset, limit int64) ([]byte, int64, bool, error) {
	if m.root == "" || serverName == "" || filepath.Base(serverName) != serverName ||
		!segmentNameRe.MatchString(segment) {
		return nil, 0, false, fmt.Errorf("mcphub: 非法日志段 %q", segment)
	}
	f, err := os.Open(filepath.Join(m.root, serverName, segment))
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	size := st.Size()
	if offset < 0 {
		offset = 0
	}
	if offset >= size {
		return nil, size, true, nil
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if offset+limit > size {
		limit = size - offset
	}
	buf := make([]byte, limit)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, false, err
	}
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, 0, false, err
	}
	return buf[:n], size, offset+int64(n) >= size, nil
}

// ---------------------------------------------------------------------------
// ServerLog 内部实现
// ---------------------------------------------------------------------------

// ensureOpen 打开（或续开）当前活跃段。幂等：已打开则空操作。
// 文件名固定为 main.log（滚动 main-2.log…），不再按时间戳命名：
// 每次「重新连接」由上层（mcp-hub LogHook 的 connect 事件）先 RemoveServerLogs 清空，
// 本方法只负责打开/续开当前最大 seq 段。
func (s *ServerLog) ensureOpen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		return
	}
	dir := filepath.Join(s.root, s.name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if s.base == "" {
		s.base = filepath.Join(dir, "main")
	}
	// 扫描找最大 seq（滚动后 Close→Ensure 续写最新段；清空后无文件则 seq=1）。
	entries, err := os.ReadDir(dir)
	if err == nil {
		bestSeq := 1
		for _, e := range entries {
			if e.IsDir() || !segmentNameRe.MatchString(e.Name()) {
				continue
			}
			if seq := segmentSeq(e.Name()); seq > bestSeq {
				bestSeq = seq
			}
		}
		s.seq = bestSeq
	}
	s.openCurrentLocked()
}

// openCurrentLocked 打开当前 seq 对应文件（调用方需持 s.mu）。
func (s *ServerLog) openCurrentLocked() {
	f, err := os.OpenFile(s.currentPathLocked(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return
	}
	s.f = f
	s.size = st.Size()
}

// currentPathLocked 返回当前 seq 的段文件完整路径（调用方需持 s.mu）。
func (s *ServerLog) currentPathLocked() string {
	if s.seq <= 1 {
		return s.base + ".log"
	}
	return fmt.Sprintf("%s-%d.log", s.base, s.seq)
}

// rollLocked 滚动到下一段文件（调用方需持 s.mu）。
func (s *ServerLog) rollLocked() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	s.seq++
	s.openCurrentLocked()
}

// write 拼行并追加写；行超限触发滚动；写失败（磁盘满等）关句柄静默放弃。
func (s *ServerLog) write(kind string, fields ...any) {
	line := buildLogLine(kind, fields...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}
	if s.size+int64(len(line)) > s.maxSize {
		s.rollLocked()
		if s.f == nil {
			return
		}
	}
	if _, err := s.f.WriteString(line); err != nil {
		_ = s.f.Close()
		s.f = nil
		return
	}
	s.size += int64(len(line))
}

// close 关闭写句柄（幂等）。
func (s *ServerLog) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
}

// ---------------------------------------------------------------------------
// 行格式化 / 掩码 / 工具函数
// ---------------------------------------------------------------------------

// buildLogLine 拼日志行：`ISO8601+08:00 [KIND] k=v k=v\n`。单行超 maxLogLineBytes 截断。
// kind 统一转大写（mcpkit hook 事件名为小写 connect/connect_ok/stderr…，落盘统一大写字面）。
func buildLogLine(kind string, fields ...any) string {
	var sb strings.Builder
	sb.WriteString(time.Now().Format("2006-01-02T15:04:05.000-07:00"))
	sb.WriteString(" [")
	sb.WriteString(strings.ToUpper(kind))
	sb.WriteString("]")
	for i := 0; i+1 < len(fields); i += 2 {
		sb.WriteByte(' ')
		sb.WriteString(fmt.Sprintf("%v", fields[i]))
		sb.WriteByte('=')
		sb.WriteString(fieldValue(fields[i+1]))
	}
	sb.WriteByte('\n')
	out := sb.String()
	if len(out) > maxLogLineBytes {
		out = out[:maxLogLineBytes] + fmt.Sprintf(" ...(truncated %d bytes)\n", len(out)-maxLogLineBytes)
	}
	return out
}

// fieldValue 把字段值序列化为可读文本：字符串加引号，其他 JSON。
func fieldValue(v any) string {
	switch val := v.(type) {
	case string:
		return strconv.Quote(val)
	case nil:
		return "null"
	default:
		if b, err := json.Marshal(val); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", val)
	}
}

// maskSecret 敏感值掩码：≤8 位全掩；否则保留首尾 4 位（与 unifyai 同语义）。
func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// maskMap 掩码 map 的全部值（env / headers 落日志用）。
func maskMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = maskSecret(v)
	}
	return out
}


// segmentSeq 解析段序号：`main.log` → 1；`main-2.log` → 2；
// 旧格式 `20260824-144325.log` → 1；`...-2.log` → 2。
// 注意时间戳中间也有 `-`，必须用正则区分「-N 段号」与「时间戳部分」。
func segmentSeq(name string) int {
	m := segmentNameRe.FindStringSubmatch(name)
	if m == nil || m[1] == "" {
		return 1
	}
	if n, err := strconv.Atoi(m[1]); err == nil && n > 1 {
		return n
	}
	return 1
}

// firstTSFromSegment 从段文件名时间戳解析显示时间（本地时区）。
// 固定名 main.log 无时间戳，返回空串（UI 只用 last_ts 展示）。
func firstTSFromSegment(name string) string {
	m := segmentNameRe.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	tsPart := strings.TrimSuffix(name, ".log")
	if m[1] != "" {
		tsPart = strings.TrimSuffix(tsPart, "-"+m[1])
	}
	if tsPart == "main" {
		return ""
	}
	t, err := time.ParseInLocation("20060102-150405", tsPart, time.Local)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
