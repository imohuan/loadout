// Package server 提供自定义 HTTP 服务，监听本地端口，处理前端 fetch 请求。
// 该服务作为独立的 http.Server 运行，不注册为 Wails Service，
// 避免拦截 /wails/runtime 导致 Window API 失效。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"proxyui/backend"

	wailsapp "github.com/wailsapp/wails/v3/pkg/application"
)

// Service 自定义 HTTP 服务，持有 Wails app 引用以支持窗口控制。
type Service struct {
	cfg     Config         // 当前运行时配置
	server  *http.Server   // HTTP 服务器实例
	running bool           // 服务器是否在运行
	app     *wailsapp.App  // Wails app 引用（用于窗口控制 API）
}

// New 创建 Service 实例。
func New() *Service {
	return &Service{}
}

// SetApp 注入 Wails app 引用，使 Service 可以控制窗口。
func (s *Service) SetApp(app *wailsapp.App) {
	s.app = app
}

// ServeHTTP 实现 http.Handler 接口。路由 /__api/* 到 serveAPI，其余返回 404。
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS,PATCH")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if r.Method == "OPTIONS" {
		return
	}

	path := r.URL.Path
	if strings.HasPrefix(path, "/__api/") {
		s.serveAPI(w, r)
		return
	}

	writeJSON(w, 404, map[string]string{"error": "not found"})
}

// serveAPI 处理 /__api/* 路由分发。
func (s *Service) serveAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/__api")
	switch {
	case path == "/status" && r.Method == "GET":
		// 返回服务运行状态和端口
		writeJSON(w, 200, map[string]any{"port": s.cfg.Port, "running": s.running})
	case path == "/config" && r.Method == "GET":
		// 获取运行时配置
		cfg, _ := LoadConfig()
		writeJSON(w, 200, cfg)
	case path == "/config" && r.Method == "POST":
		// 保存运行时配置并重启服务器（端口变更时生效）
		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		SaveConfig(cfg)
		s.restartServer()
		writeJSON(w, 200, map[string]string{"ok": "saved"})
	case path == "/window/min" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil {
			w.Minimise()
		}
		writeJSON(w, 200, map[string]string{"ok": "minimised"})
	case path == "/window/max" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil {
			w.ToggleMaximise()
		}
		writeJSON(w, 200, map[string]string{"ok": "toggled"})
	case path == "/window/close" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil {
			w.Close()
		}
		writeJSON(w, 200, map[string]string{"ok": "closed"})
	default:
		writeJSON(w, 404, map[string]string{"error": "unknown api"})
	}
}

// Start 启动 HTTP 服务器，首次调用创建服务器，后续调用仅设置 running 状态。
func (s *Service) Start() {
	if s.server != nil {
		s.running = true
		return
	}
	cfg, _ := LoadConfig()
	s.cfg = cfg

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.ServeHTTP)
	s.server = &http.Server{Addr: ":" + strconv.Itoa(cfg.Port), Handler: mux}
	s.running = true
	go func() {
		log.Printf("%s 服务 :%d", config.App.Name, cfg.Port)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()
}

// Stop 停止服务（仅标记状态，不关闭 HTTP 服务器，API 仍可用）。
func (s *Service) Stop() {
	s.running = false
}

// restartServer 关闭旧服务器，在新端口上重新启动（配置端口变更时调用）。
func (s *Service) restartServer() {
	s.running = false
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	cfg, _ := LoadConfig()
	s.cfg = cfg
	s.running = true

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.ServeHTTP)
	s.server = &http.Server{Addr: ":" + strconv.Itoa(cfg.Port), Handler: mux}
	go func() {
		log.Printf("%s 服务 :%d [端口已变更]", config.App.Name, cfg.Port)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("server error: %v", err)
		}
	}()
}

// writeJSON 写入 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
