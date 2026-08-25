// Package servercore 提供 Loadout Server 的核心启动逻辑，可被 apps/server 和 apps/desktop 复用。
package servercore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/logger"
	"loadout/core/plugin"
	"loadout/core/procreg"
	"loadout/core/store"
	"loadout/frontend"
	"loadout/plugins"
	adminapi "loadout/plugins/admin-api"
	adminauth "loadout/plugins/admin-auth"
	gatewaykeys "loadout/plugins/gateway-keys"
	mcphub "loadout/plugins/mcp-hub"
)

// newLogger 从 config 构建日志器（SourceRoot = 工作目录，用于裁出仓库相对路径）。
func newLogger() *slog.Logger {
	root, _ := os.Getwd()
	return logger.New(logger.Options{
		Level:      config.LogLevel,
		LogDir:     config.LogsDir,
		Filename:   "loadout.log",
		MaxSizeMB:  config.LogMaxSizeMB,
		MaxBackups: config.LogMaxBackups,
		MaxAgeDays: config.LogMaxAgeDays,
		SourceRoot: root,
		Console:    true,
	})
}

// assemble 装配全部插件，返回装配产物与单端口路由。
func assemble(lg *slog.Logger, st *store.Store) (*plugin.Assembly, http.Handler, error) {
	database, err := db.OpenForStore(st)
	if err != nil {
		return nil, nil, err
	}
	if err := db.ImportJSON(context.Background(), database, st); err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	if err := db.ImportAdminJSON(context.Background(), database, st); err != nil {
		_ = database.Close()
		return nil, nil, err
	}
	asm, err := plugin.Load(plugins.All(), plugin.Options{
		Logger: lg,
		Services: map[string]any{
			"store":       st,
			"logger":      lg,
			"http-client": &http.Client{Timeout: config.UpstreamTimeout},
			"db":          database,
		},
	})
	if err != nil {
		_ = database.Close()
		return nil, nil, err
	}

	keys := asm.Get("gateway-keys").(*gatewaykeys.Manager)
	auth := asm.Get("admin-auth").(*adminauth.Service)
	hub := asm.Get("mcp-hub").(*mcphub.Service)

	// 注入已装配插件总数与自检结果提供者（概览/插件页展示）。
	apiSvc := asm.Get("admin-api").(*adminapi.Service)
	apiSvc.SetPluginCount(len(plugins.All()))
	apiSvc.SetChecksProvider(func() []plugin.PluginCheck { return asm.ChecksByPlugin() })

	// 首启流程：users.json 不存在时生成随机密码。
	if _, err := auth.EnsureFirstRun(); err != nil {
		asm.Unload()
		_ = database.Close()
		return nil, nil, err
	}

	mux := http.NewServeMux()

	// 插件路由（按 Auth 类别挂认证中间件）。
	for _, r := range asm.Routes {
		if r.Pattern == "" {
			continue
		}
		h := r.Handler
		switch r.Auth {
		case plugin.AuthSkKey:
			h = keys.SkKeyMiddleware(h)
		case plugin.AuthMCPHeader:
			h = keys.MCPKeyMiddleware(h)
		case plugin.AuthSession:
			h = auth.SessionMiddleware(h)
		}
		mux.Handle(routePattern(r), h)
	}

	// MCP 端点：单一 /mcp/ 前缀 handler 动态分发，端点随配置增删实时生效。
	// 单 MCP / 分组端点直接暴露工具；$smart 保留 3 工具入口，按 header 分组名动态解析视图。
	// getServer 只在「新 session」时调用一次，因此重新连接总能拿到最新配置的工具视图。
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		ep := r.URL.Path
		if ep == "/mcp/$smart" {
			return hub.SmartEndpointServer(r.Header.Get(config.SmartGroupHeader))
		}
		return hub.EndpointServerOrEmpty(ep)
	}, nil)
	mux.Handle("/mcp/", keys.MCPKeyMiddleware(mcpHandler))

	// 管理后台静态资源（其余路径，公开；数据走 /api/* 的 session 认证）。
	// 用 SPA fallback 包装：Vue Router 为 history 模式时，刷新前端路由
	//（如 /capability-routes）不落盘，直接回退 index.html 由前端接管。
	dist, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		return nil, nil, err
	}
	mux.Handle("/", spaFileServer(dist))

	// 启动自动恢复：拉起所有 enabled 的 stdio MCP 进程，使其常驻后台。
	// 单个失败只记日志不阻断启动；失败状态由前端经 /api/mcp-servers 展示。
	// 整个拉起过程完全后台化（不再占用装配路径），服务先监听、MCP 后绪。
	plugin.RunBackground("mcp-hub-start", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hub.StartEnabled(ctx)
		return nil
	})

	return asm, requestIDMiddleware(lg, corsMiddleware(mux)), nil
}

// corsMiddleware 处理跨域请求：对所有响应设置 CORS 头，OPTIONS 预检直接返回 204。
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,PATCH,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaFileServer 包装 http.FileServer，实现 SPA 单页回退：
// dist 中真实存在的文件照常由 FileServer 服务；不存在的路径视为前端路由，
// 回退 index.html 让 Vue Router（history 模式）接管。形如 assets/xxx.js 的
// 缺失静态资源直接 404，避免把资源 404 当成 HTML 返回。
func spaFileServer(dist fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(dist))
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		panic("dist 缺少 index.html: " + err.Error())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// 根路径与真实存在的文件/目录走 FileServer（根路径自动解析 index.html）。
		if name == "" || name == "." {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(dist, name); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 缺失的资源文件（路径最后一段含扩展名）→ 404。
		if strings.Contains(path.Base(name), ".") {
			http.NotFound(w, r)
			return
		}
		// 否则是前端路由 → 回退 index.html。
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}

// Assemble is the testable startup path used by the server and desktop hosts.
func Assemble(lg *slog.Logger, st *store.Store) (*plugin.Assembly, http.Handler, error) {
	return assemble(lg, st)
}

// routePattern 归一化路由模式：RouteSpec.Pattern 若已含 "METHOD /path" 则直接用，
// 否则用 Method + " " + Pattern 拼接（兼容两种写法）。
func routePattern(r plugin.RouteSpec) string {
	if r.Method != "" && !strings.HasPrefix(r.Pattern, r.Method+" ") {
		return r.Method + " " + r.Pattern
	}
	return r.Pattern
}

// requestIDMiddleware 给每个请求生成 request_id，并在请求结束时统一记录访问日志；
// 同时兜底 recover 处理 handler panic：记录错误与堆栈，未写出响应时补一个 500。
// 优先复用客户端传入的 X-Request-Id（SDK 重试复用同一 id 时 route-log 会合并为一条），
// 缺失时生成并写回请求头，保证下游（model-gateway 等）能取到同一个 id。
func requestIDMiddleware(lg *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newRequestID()
			r.Header.Set("X-Request-Id", id)
		}
		w.Header().Set("X-Request-Id", id)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		defer func() {
			// panic 兜底：记录 + 若尚未写响应则补 500，避免客户端拿空响应。
			if p := recover(); p != nil {
				lg.Error("请求处理 panic",
					"request_id", id, "method", r.Method, "path", r.URL.Path,
					"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				if rec.status == 0 {
					rec.Header().Set("Content-Type", "application/json; charset=utf-8")
					rec.WriteHeader(http.StatusInternalServerError)
					_, _ = rec.Write([]byte(`{"error":{"message":"服务器内部错误","type":"internal_error"}}`))
				}
			}

			// 结束日志：按状态码分级，5xx 记 Error、4xx 记 Warn，方便按错误检索。
			args := []any{
				"request_id", id, "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration_ms", time.Since(start).Milliseconds(),
			}
			switch {
			case rec.status >= 500:
				lg.Error("请求结束", args...)
			case rec.status >= 400:
				lg.Warn("请求结束", args...)
			default:
				lg.Info("请求结束", args...)
			}
		}()

		lg.Info("请求", "method", r.Method, "path", r.URL.Path, "request_id", id)
		next.ServeHTTP(rec, r)
	})
}

// statusRecorder 包装 http.ResponseWriter，捕获响应状态码与写入字节数，
// 并透传 Flush 以支持 SSE 流式响应。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader 记录首次写入的状态码。
func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write 记录写入字节数；若未显式 WriteHeader 则按 200 处理。
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush 透传底层 Flusher（SSE 流式转发需要）。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newRequestID 生成 16 位十六进制 request_id。
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Run 启动服务器：装配 → 自检日志 → 监听 → 优雅退出。
// 全局可编程退出通道：外部（如桌面版退出）调用 TriggerShutdown 触发 servercore 优雅退出。
var (
	shutdownOnce sync.Once
	shutdownCh   = make(chan struct{})
)

// TriggerShutdown 触发 servercore 优雅退出（终止子进程、关闭 server）。幂等。
func TriggerShutdown() { shutdownOnce.Do(func() { close(shutdownCh) }) }

func Run() error {
	lg := newLogger()
	// 替换全局默认 logger，保证任何 slog.Default() 都落到日志文件而非 stderr。
	slog.SetDefault(lg)
	lg.Info("启动", "app", config.AppName, "version", config.Version, "mode", config.RunMode)

	st, err := store.New(config.DataDir)
	if err != nil {
		return err
	}

	asm, handler, err := assemble(lg, st)
	if err != nil {
		return err
	}
	defer asm.Unload()

	// 打印插件自检结果。
	for name, issues := range asm.Checks() {
		for _, it := range issues {
			lg.Info("自检", "check", name, "level", it.Level, "message", it.Message)
		}
	}

	addr := listenAddr()
	// 所有 HTTP 连接共享一个可取消的 context：Shutdown 时 cancel 它，
	// 让 SSE 等长连接立即断开，避免 srv.Shutdown 一直等它们完成而卡住。
	connCtx, connCancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return connCtx },
		ReadHeaderTimeout: config.HTTPReadTimeout,
		WriteTimeout:      config.UpstreamTimeout,
	}

	lg.Info("监听", "addr", addr)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	// 可编程退出：除 os.Signal 外，外部（如桌面版退出）可触发优雅退出。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	// 终止子进程 + 取消活跃连接，再优雅关闭 HTTP 服务器。
	doShutdown := func(reason string) error {
		// 1) 终止所有运行中的子进程（统一命令执行器），避免退出后残留孤儿进程。
		procreg.Get().Shutdown()
		// 2) 取消活跃连接 context，让 SSE 长连接立即断开，避免 srv.Shutdown 卡住。
		connCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		lg.Info("收到信号，退出", "signal", sig.String())
		return doShutdown(sig.String())
	case <-shutdownCh:
		lg.Info("收到退出请求")
		return doShutdown("shutdown-requested")
	}
}

// listenAddr 按运行模式决定监听地址：server 监听全网卡，desktop 仅 127.0.0.1。
func listenAddr() string {
	if config.RunMode == "desktop" {
		return "127.0.0.1" + portOnly(config.ServerAddr)
	}
	return config.ServerAddr
}

// portOnly 提取地址里的端口部分（":3000" 或 ":8080"）。
func portOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":3000"
}
