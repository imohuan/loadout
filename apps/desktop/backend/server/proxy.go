package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ProxyHandler 包装静态资源 handler，拦截 API 请求并代理到 Loadout Server。
type ProxyHandler struct {
	staticHandler http.Handler
	loadoutProxy  *httputil.ReverseProxy
}

// NewProxyHandler 创建代理 handler。
// staticHandler: Wails 的静态资源 handler (AssetFileServerFS)
// loadoutAddr: Loadout Server 地址，例如 "http://127.0.0.1:3000"
func NewProxyHandler(staticHandler http.Handler, loadoutAddr string) *ProxyHandler {
	target, err := url.Parse(loadoutAddr)
	if err != nil {
		log.Fatalf("解析 Loadout Server 地址失败: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// SSE/流式响应必须立刻 flush，否则前端 EventSource 会一直挂在 pending 状态、
	// 红点（连接断开）一直亮着，整个进程面板显示 0 个进程等异常。
	// FlushInterval = 0（默认）会在响应体写完才 flush，但 SSE 的 body 永远不结束——
	// 数据全部被 buffer 住，前端永远收不到 snapshot。设为 -1 让 proxy 在每次 Write
	// 后立即将数据 flush 到 webview 侧。
	proxy.FlushInterval = -1

	// 自定义错误处理
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[Proxy Error] %s %s: %v", r.Method, r.URL.Path, err)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"Loadout Server 连接失败","type":"proxy_error"}}`))
	}

	// 自定义请求修改
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// 保留原始 Host 头，确保 Cookie 正确
		req.Host = target.Host
		
		// 调试日志
		log.Printf("[Proxy] %s %s -> %s%s", req.Method, req.URL.Path, target, req.URL.Path)
		log.Printf("[Proxy] Content-Type: %s", req.Header.Get("Content-Type"))
		log.Printf("[Proxy] Content-Length: %s", req.Header.Get("Content-Length"))
	}

	// 自定义响应修改
	proxy.ModifyResponse = func(resp *http.Response) error {
		// 确保 Cookie 的 Domain 正确
		for _, cookie := range resp.Cookies() {
			cookie.Domain = ""
			cookie.Path = "/"
			resp.Header.Add("Set-Cookie", cookie.String())
		}
		return nil
	}

	return &ProxyHandler{
		staticHandler: staticHandler,
		loadoutProxy:  proxy,
	}
}

// ServeHTTP 实现 http.Handler 接口。
// 路由规则：
// - /api/*, /v1/*, /mcp/* -> 代理到 Loadout Server
// - 其他请求 -> 静态资源 handler (AssetFileServerFS)
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 检查是否是 API 请求
	if strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/mcp/") {
		
		log.Printf("[API Request] %s %s", r.Method, path)
		
		// 关键修复：缓存 request body
		// Wails 可能会在某些情况下消费掉 body，导致代理时 body 为空
		var bodyBytes []byte
		if r.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(r.Body)
			if err != nil {
				log.Printf("[Proxy Error] 读取 body 失败: %v", err)
				http.Error(w, "读取请求体失败", http.StatusInternalServerError)
				return
			}
			r.Body.Close()
			
			// 重新设置 body，供代理使用
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
			
			log.Printf("[Proxy] Body size: %d bytes", len(bodyBytes))
		}
		
		h.loadoutProxy.ServeHTTP(w, r)
		return
	}

	// 静态资源请求
	h.staticHandler.ServeHTTP(w, r)
}
