package mcphub

import (
	"context"
	"errors"
	"sync"
	"time"

	"loadout/plugins/types"
)

// 自动探活与重连的默认参数。
const (
	// reconnectMaxDefault 探活失败后自动重连的默认次数（设置页可改，0 = 用这个值）。
	reconnectMaxDefault = 3
	// probeIntervalDefault 后台探活周期：定时对每个启用的上游执行 tools/list，失败即进入重连。
	probeIntervalDefault = 15 * time.Second
	// probeTimeout 单次探活的超时（坏上游不能拖住整轮探活）。
	probeTimeout = 5 * time.Second
	// reconnectTimeout 单次重连的超时。
	reconnectTimeout = 30 * time.Second
	// retryDwellDefault 每次重试状态至少停留多久，供管理页轮询看到。
	// 前端每 3 秒拉一次状态；重试本身可能几十毫秒就成功（本地 stdio 重启），
	// 不留够时间就会直接跳过「重试 1」，用户只看到「失败 → 进行中」。
	retryDwellDefault = 4 * time.Second
)

// connAttempt 一个上游当前的重连进度（只存内存，重启后从零开始）。
type connAttempt struct {
	attempts int
	lastErr  string
}

// healthMonitor 维护全部上游的探活与重连状态：
//   - 后台按 probeInterval 定时对每个启用的上游执行 ListTools（stdio 先看进程是否存活）；
//   - 失败后按 1s→2s→4s 的退避重连，次数达到上限仍失败 → 自动停用该上游；
//   - attempts > 0 期间，ServerStatus 报 reconnecting，前端显示「重连中 N」。
type healthMonitor struct {
	mu           sync.Mutex
	states       map[string]*connAttempt
	reconnecting map[string]bool
	max          int
	interval     time.Duration
	dwell        time.Duration
	done         chan struct{}
	closeOnce    sync.Once
}

// newHealthMonitor 创建探活监控（后台循环由 Service.startHealthLoop 启动）。
func newHealthMonitor() *healthMonitor {
	return &healthMonitor{
		states:       map[string]*connAttempt{},
		reconnecting: map[string]bool{},
		max:          reconnectMaxDefault,
		interval:     probeIntervalDefault,
		dwell:        retryDwellDefault,
		done:         make(chan struct{}),
	}
}

// SetDwell 调整每次重试状态的停留时长（测试用；<=0 回落默认值）。
func (m *healthMonitor) SetDwell(d time.Duration) {
	if d <= 0 {
		d = retryDwellDefault
	}
	m.mu.Lock()
	m.dwell = d
	m.mu.Unlock()
}

// Dwell 当前重试状态停留时长。
func (m *healthMonitor) Dwell() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dwell
}

// SetMax 更新重连次数上限（设置页保存后热更新；0 用默认值）。
func (m *healthMonitor) SetMax(n int) {
	if n <= 0 {
		n = reconnectMaxDefault
	}
	m.mu.Lock()
	m.max = n
	m.mu.Unlock()
}

// Max 当前重连次数上限。
func (m *healthMonitor) Max() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.max
}

// Interval 后台探活周期。
func (m *healthMonitor) Interval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interval
}

// Attempts 当前重连尝试次数（0 = 没在重连）。

// RetrySnapshot 一次读出「是否在重试 + 第几次」，避免分两次加锁读到撕裂状态。
// 返回 attempts=0 且 reconnecting=false 表示重试阶段已结束。
func (m *healthMonitor) RetrySnapshot(id string) (attempts int, reconnecting bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[id]; st != nil {
		attempts = st.attempts
	}
	return attempts, m.reconnecting[id]
}
func (m *healthMonitor) Attempts(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[id]; st != nil {
		return st.attempts
	}
	return 0
}

// LastError 最近一次探活/重连失败原因。
func (m *healthMonitor) LastError(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[id]; st != nil {
		return st.lastErr
	}
	return ""
}

// SetAttempt 记录第 n 次重连尝试（用于状态展示与失败原因）。
func (m *healthMonitor) SetAttempt(id string, attempts int, lastErr string) {
	m.mu.Lock()
	m.states[id] = &connAttempt{attempts: attempts, lastErr: lastErr}
	m.mu.Unlock()
}

// Clear 清空进度（探活成功 / 手动启停 / 自动停用后调用）。
func (m *healthMonitor) Clear(id string) {
	m.mu.Lock()
	delete(m.states, id)
	m.mu.Unlock()
}

// BeginReconnect 标记该上游正在重连；已有重连在跑时返回 false（防重复）。
func (m *healthMonitor) BeginReconnect(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconnecting[id] {
		return false
	}
	m.reconnecting[id] = true
	return true
}

// EndReconnect 结束重连标记。
func (m *healthMonitor) EndReconnect(id string) {
	m.mu.Lock()
	delete(m.reconnecting, id)
	m.mu.Unlock()
}

// IsReconnecting 该上游是否正在重连（探活循环据此跳过，避免重复触发）。
func (m *healthMonitor) IsReconnecting(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconnecting[id]
}

// Close 停止后台探活循环。

// FinishRetry 一次结束重试阶段：清掉次数并落下重试标记（同一把锁内完成）。
// 状态查询用 RetrySnapshot 一次读取，因此不会落在两步之间看到撕裂状态。
func (m *healthMonitor) FinishRetry(id string) {
	m.mu.Lock()
	delete(m.states, id)
	delete(m.reconnecting, id)
	m.mu.Unlock()
}

// FinishAndClear 原子地结束一轮重试：清掉 attempts 并标记重试已结束。
//
// 为什么必须原子：ServerStatus 会先读 attempts、再读 enabled。若先清 attempts
// 再写 enabled=false（失败收尾），或反过来（成功收尾），中间那一瞬间会出现
// 「attempts=3 但 enabled=false」→ 判定成 failed → 下一帧又变回 reconnecting，
// 就是用户看到的「失败 ↔ 重试」来回跳。持有同一把锁把两件事一起做完即可消除。
func (m *healthMonitor) FinishAndClear(id string) {
	m.mu.Lock()
	delete(m.states, id)
	m.reconnecting[id] = false
	m.mu.Unlock()
}

// DwellWait 等待 d 这么久，期间若服务关闭则立即返回。
// 用于让「重试 N」状态在管理页停留足够久（前端每 3 秒轮询一次）。
func (m *healthMonitor) DwellWait(d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-m.done:
	case <-t.C:
	}
}
func (m *healthMonitor) Close() {
	m.closeOnce.Do(func() {
		close(m.done)
	})
}

// startHealthLoop 启动后台定时探活：按 interval 对全部启用的上游探活。
func (s *Service) startHealthLoop() {
	interval := s.health.Interval()
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.health.done:
				return
			case <-ticker.C:
				s.probeAll()
			}
		}
	}()
}

// probeAll 对全部启用的上游并行探活（并发上限 4，单个探活 5s 超时）。
// 状态查询不做联网，探活结果只写内存，管理页按需读取。
func (s *Service) probeAll() {
	servers, err := s.readServers()
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, srv := range servers {
		if !srv.Enabled || srv.Builtin {
			continue
		}
		if s.health.IsReconnecting(srv.ID) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(srv types.MCPServer) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			err := s.probeOnce(ctx, srv)
			cancel()
			if err == nil {
				s.health.Clear(srv.ID)
				return
			}
			s.warn("mcphub: 上游探活失败，开始自动重连", "server", srv.Name, "err", err)
			s.startReconnect(srv, err)
		}(srv)
	}
	wg.Wait()
}

// probeOnce 执行一次探活：

// disconnectReason 从 LogHook 的 fields 里取断开原因（形如 ["reason", "process_exit"]）。
func disconnectReason(fields []any) string {
	for i := 0; i+1 < len(fields); i += 2 {
		key, _ := fields[i].(string)
		if key != "reason" {
			continue
		}
		switch v := fields[i+1].(type) {
		case string:
			return v
		}
	}
	return ""
}

// handleUpstreamProcessExit 子进程退出时立刻安排重试（由 LogHook 触发）。
//
// 为什么需要它：进程刚被杀时 Alive() 已是 false，ServerStatus 会直接报 failed；
// 而重试原本只由后台探活（默认 15 秒一轮）触发，中间这段时间前端就看到「失败」。
// 只有「用户/系统杀掉进程」这条路径需要立即重试；正常 Close（用户关闭开关、
// 编辑配置后重启）走 disconnect(closed)，不能在这里重试，否则会与用户意图打架。
//
// 回调约束：本方法从 Upstream 的日志回调里调用，绝不能再调 Upstream 的方法；
// 这里只做内存登记并起协程，立即返回。
func (s *Service) handleUpstreamProcessExit(id string) {
	srv, err := s.findServer(id)
	if err != nil || !srv.Enabled {
		return // 配置已删/已停用：不需要自愈
	}
	exitErr := errors.New("上游进程已退出（进程被杀或崩溃）")
	s.health.SetAttempt(id, 1, exitErr.Error())
	s.startReconnect(*srv, exitErr)
}

// 统一走 tools/list：这是判断「连接还能不能用」最可靠的方式（stdio 进程被外部
// 杀掉、会话管道已坏时，仅看进程句柄会误判为运行中）。
// 只看连接池里已有的连接：启用过的上游在拉起时已经入池；没入池的（从未启动）
// 交给 ServerStatus 的「未连接」判断，不在探活里顺手建连——否则会和用户编辑配置、
// 保存后重建连接的过程相互踩踏。
func (s *Service) probeOnce(ctx context.Context, srv types.MCPServer) error {
	up := s.getUpstreamByID(srv.ID)
	if up == nil {
		return nil // 未拉起：没有连接可探，等用户启用/下次启动恢复
	}
	return up.Probe(ctx)
}

// startReconnect 对探活失败的上游执行自动重连：最多 N 次（N = 设置值），
// 每次尝试把 attempts 写进内存（前端显示「重连中 N」）；全部失败后自动停用。
func (s *Service) startReconnect(srv types.MCPServer, firstErr error) {
	if !s.health.BeginReconnect(srv.ID) {
		return
	}
	// 立刻登记「第 1 次」，别等 goroutine 调度：否则探测失败到第一次重试之间，
	// attempts 仍是 0，状态会短暂退回 failed（用户看到的失败↔进行中跳动）。
	s.health.SetAttempt(srv.ID, 1, firstErr.Error())
	go func() {
		max := s.health.Max()
		err := firstErr
		for attempt := 1; attempt <= max; attempt++ {
			s.health.SetAttempt(srv.ID, attempt, err.Error())
			s.warn("mcphub: 自动重连", "server", srv.Name, "attempt", attempt, "err", err)
			// 每次重试先亮出「重试 N」并停留一会儿，再真正建连。
			// 首次重试原来会立刻执行：上游恢复快时状态只存在几十毫秒，
			// 管理页 3 秒一次的轮询根本抓不到，看起来就像从「失败」直接跳回「进行中」。
			s.health.DwellWait(s.health.Dwell())
			ctx, cancel := context.WithTimeout(context.Background(), reconnectTimeout)
			err = s.reconnectOnce(ctx, srv.ID)
			cancel()
			if err == nil {
				// 成功后先停留一会儿再回到「进行中」：建连可能几十毫秒就完成，
				// 不停留的话轮询看不到「重试 N 成功」这一帧。
				s.health.DwellWait(s.health.Dwell())
				s.health.FinishRetry(srv.ID)
				s.Invalidate() // 重连成功后工具清单可能变化，清索引让端点按新连接重建
				s.info("mcphub: 自动重连成功", "server", srv.Name, "attempt", attempt)
				return
			}
		}
		// 超过上限：自动关闭（与用户手动关闭一致：配置里 enabled=false + 进程停掉）。
		s.warn("mcphub: 自动重连达到上限，停用", "server", srv.Name, "attempts", max, "err", err)
		// 把最后一次错误一起落库：状态显示「失败」，表示重试机会已经用完。
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		// 顺序很重要：先清内存进度（结束「重试中」），再写 enabled=false。
		// 用一次原子收尾同时清掉「次数 + 重试标记」，状态查询不会读到撕裂状态。
		s.health.FinishRetry(srv.ID)
		if derr := s.disableServerPersistentWithError(srv.ID, errText); derr != nil {
			s.warn("mcphub: 自动停用失败", "server", srv.Name, "err", derr)
		}
	}()
}

// disableServerPersistent 把配置里的 enabled 置 false 并停掉进程。
// 自动重连用尽后调用：与用户手动关闭效果一致（重启后也不会再被拉起）。
// 注意：必须写回配置，只关连接会让列表继续显示「启用」，下次启动又被拉起。
func (s *Service) disableServerPersistent(id string) error {
	return s.disableServerPersistentWithError(id, "")
}

// disableServerPersistentWithError 同 disableServerPersistent，但额外记录终态失败原因。
// 自动重试耗尽时传入最后一次错误：状态显示「失败」而不是「已停止」，
// 明确告诉用户「重试机会用完了」。用户手动停用则传空串，清掉历史失败标记。
func (s *Service) disableServerPersistentWithError(id, lastErr string) error {
	servers, err := s.readServers()
	if err != nil {
		// 读不到配置也要尽力停进程，避免留下孤儿连接。
		_ = s.SetServerEnabled(context.Background(), id, false)
		return err
	}
	found := false
	for i := range servers {
		if servers[i].ID == id {
			servers[i].Enabled = false
			servers[i].LastError = lastErr
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if s.repo != nil {
		if err := s.repo.ReplaceMCPServers(context.Background(), servers); err != nil {
			return err
		}
	} else if err := s.st.Write(types.FileMCPServers, servers); err != nil {
		return err
	}
	_ = s.SetServerEnabled(context.Background(), id, false)
	s.Invalidate()
	return nil
}

// reconnectOnce 关闭旧连接并按当前配置重建（一次真正的重连尝试）。

// clearLastError 清空某台上游持久化的终态失败原因（手动重新启用 / 重连成功时调用）。
// 只改内存里的配置快照并落库，不触发进程操作。
func (s *Service) clearLastError(id string) {
	servers, err := s.readServers()
	if err != nil {
		return
	}
	found := false
	for i := range servers {
		if servers[i].ID == id && servers[i].LastError != "" {
			servers[i].LastError = ""
			found = true
			break
		}
	}
	if !found {
		return
	}
	if s.repo != nil {
		if err := s.repo.ReplaceMCPServers(context.Background(), servers); err == nil {
			return
		}
	}
	_ = s.st.Write(types.FileMCPServers, servers)
}
func (s *Service) reconnectOnce(ctx context.Context, id string) error {
	srv, err := s.findServer(id)
	if err != nil {
		return err
	}
	if !srv.Enabled {
		return nil // 已被手动关闭：视为结束，不再重连
	}
	if up := s.getUpstreamByID(id); up != nil {
		_ = up.Close()
		s.dropUpstream(id)
	}
	up := s.getUpstream(*srv)
	if srv.Transport == "stdio" {
		return up.Connect(ctx)
	}
	return up.Probe(ctx)
}
