package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// SPAFallback 包裹 asset handler，给前端 history 模式的路由做 fallback：
//
//   - 上游返回 2xx/3xx/其它 4xx（包括 401/403/405 等）：透传响应，不干预。
//   - 上游返回 404：若 URL 末段看起来是资源文件（最后一段含 "."），透传真实
//     404，避免把资源缺失伪装成 HTML 200 让前端继续炸；
//     若 URL 末段不带扩展名（典型的 Vue Router 路由，如 /settings、/channels）
//     则重写为嵌入的 index.html，让前端 router 接管。
//
// 为什么需要：wails 的 AssetFileServer / BundledAssetFileServer 是裸 FileServer，
// 不带 SPA fallback。直接刷新某个前端路由（/settings、/channels 等）会触发
// 404 错误页。给 webview 和外部浏览器一个 fallback 后，SPA history 模式
// 才能正常工作。
//
// fsys 用于找 index.html：如 index.html 缺失就退化为透传。
//
// 实现细节：用 buffered writer 拦截上游的 WriteHeader/Write，验证后再决定
// 是 flush 缓冲还是用 index.html 覆盖。流式响应（SSE 等）不会被这里的 fallback
// 涉及——SPA 路由全部走静态资源，不会有流。
func SPAFallback(handler http.Handler, fsys fs.FS) http.Handler {
	indexBytes, err := readIndexHTML(fsys)
	if err != nil {
		// 兜底：没 index.html 就不做事，让上游 404 行为保持。
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := &spaBuffer{header: http.Header{}}
		handler.ServeHTTP(buf, r)

		// 只对"找不到文件 + 看起来是 SPA 路由"做 fallback。
		// 路径最后一段含 "." 一律视为静态资源，保留真实 404。
		isSPA := buf.statusCode == http.StatusNotFound &&
			!strings.Contains(path.Base(strings.TrimPrefix(path.Clean(r.URL.Path), "/")), ".")
		if !isSPA {
			// 透传上游响应
			flushSpaBuffer(w, buf)
			return
		}

		// SPA fallback：丢弃上游 404 的 body 与长度相关头，写 index.html。
		for k, vv := range buf.header {
			if isHopByHop(k) || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})
}

// readIndexHTML 兼容两种 embed 形态：
//
//  1. fsys 根直接是 dist 内容（如 `//go:embed dist`，本仓库就内嵌 frontend/dist
//     的根调用方拿到了 dist/*），直接 ReadFile 拿到 index.html。
//  2. fsys 根是上一层目录（如 `//go:embed all:frontend/dist`，apps/desktop/main
//     把 frontend/dist 整体嵌进 binary），index.html 在 dist/index.html，
//     必须先 WalkDir 找到 index.html 路径再 fs.Sub，然后 ReadFile。
//
// 之前写 `fs.ReadFile(fsys, "index.html")` 在形态 2 下必失败（直接返回
// `file does not exist`），导致 SPA fallback 退化为透传——这就是「Ctrl+R
// 刷新 /login 还是 404」的根因。这两种形态都要支持。
func readIndexHTML(fsys fs.FS) ([]byte, error) {
	// 形态 1：根就是 dist。
	if data, err := fs.ReadFile(fsys, "index.html"); err == nil {
		return data, nil
	}
	// 形态 2：在 fsys 里递归找 index.html，子目录截取后再读。
	var found string
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, "index.html") {
			found = p
			return fs.SkipAll
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if found == "" {
		return nil, fs.ErrNotExist
	}
	dir, _ := path.Split(found)
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		// index.html 真在 fsys 根的兜底分支。
		return fs.ReadFile(fsys, "index.html")
	}
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(sub, "index.html")
}

// spaBuffer 缓冲上游 ResponseWriter 的输出，便于在 fallback 决策时
// 改写响应（替换 404 的 body / 状态码）而不污染下游连接状态。
type spaBuffer struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
	statusSet  bool
}

func (s *spaBuffer) Header() http.Header { return s.header }

func (s *spaBuffer) WriteHeader(code int) {
	if s.statusSet {
		return
	}
	s.statusSet = true
	s.statusCode = code
}

func (s *spaBuffer) Write(p []byte) (int, error) {
	if !s.statusSet {
		// 显式 WriteHeader 之前 Write，按 200 处理（与 net/http 行为一致）
		s.statusSet = true
		s.statusCode = http.StatusOK
	}
	return s.body.Write(p)
}

// flushSpaBuffer 把缓冲写入下游（透传路径）。
func flushSpaBuffer(w http.ResponseWriter, buf *spaBuffer) {
	for k, vv := range buf.header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if buf.statusSet {
		w.WriteHeader(buf.statusCode)
	}
	_, _ = w.Write(buf.body.Bytes())
}

// isHopByHop 过滤掉 hop-by-hop 头，避免双重控制连接语义。
// 列表来自 RFC 7230 §6.1；Connection 头的项也是 hop-by-hop，但标准库处理。
func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}
