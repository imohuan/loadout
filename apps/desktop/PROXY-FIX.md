# Desktop 应用 API 代理修复

## 问题描述

Desktop 应用在开发模式下无法登录，错误信息：
```
POST http://wails.localhost:9245/api/login
Status: 400 Bad Request
{"error":{"message":"请求体不是合法 JSON","type":"invalid_request_error"}}
```

## 根本原因

### 问题 1：Wails 协议导致请求未经过 Vite proxy

Wails 使用 `wails.localhost` 协议而非普通 `http://localhost`，导致：
1. **API 请求没有经过 Vite proxy**
   - 前端代码配置了 Vite proxy (`/api/* -> http://127.0.0.1:3000`)
   - 但 Wails 直接用 `AssetFileServerFS` 服务静态资源
   - `wails.localhost` 请求不会触发 Vite proxy

2. **请求直接打到了 Wails 的资源服务器**
   - Wails 只会返回静态文件（HTML/JS/CSS）
   - 无法处理 API 请求

### 问题 2：Wails 消费了 Request Body

即使添加了 Go 层代理，Wails 的 HTTP handler 会在某些情况下**读取并消费掉 `r.Body`**：
- `Content-Length` 变为空
- 代理时 body 已经是空的 `io.Reader`
- 后端收到空 body，JSON 解析失败

**调试日志证据：**
```
[Proxy] POST /api/login -> http://127.0.0.1:3000/api/login
[Proxy] Content-Type: application/json
[Proxy] Content-Length:                    ← 空的！
```

## 解决方案

### 1. 创建 Go 层 API 代理

**文件：** `apps/desktop/backend/server/proxy.go`

实现 `ProxyHandler` 包装 `AssetFileServerFS`：

```go
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 拦截 API 请求
    if strings.HasPrefix(path, "/api/") || ... {
        // 关键修复：缓存 request body
        // Wails 可能会消费掉 body，导致代理时 body 为空
        var bodyBytes []byte
        if r.Body != nil {
            bodyBytes, _ = io.ReadAll(r.Body)
            r.Body.Close()
            
            // 重新设置 body，供代理使用
            r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            r.ContentLength = int64(len(bodyBytes))
        }
        
        h.loadoutProxy.ServeHTTP(w, r)
        return
    }
    
    // 静态资源
    h.staticHandler.ServeHTTP(w, r)
}
```

**关键特性：**
- ✅ 拦截 `/api/*`, `/v1/*`, `/mcp/*` 请求
- ✅ **缓存并重建 request body**（修复 Wails 消费 body 问题）
- ✅ 使用 `httputil.ReverseProxy` 代理到 `127.0.0.1:3000`
- ✅ 自动处理 Cookie 转发
- ✅ 详细的请求日志

### 2. 更新应用入口

**文件：** `apps/desktop/backend/app/runner.go`

```go
Assets: application.AssetOptions{
    Handler: server.NewProxyHandler(
        application.AssetFileServerFS(assets),
        "http://127.0.0.1:3000",
    ),
},
```

**原理：**
- `AssetFileServerFS(assets)` 处理静态资源
  - 开发模式：代理到 Vite (localhost:9245)
  - 生产模式：从 embed FS 读取
- `ProxyHandler` 包装它，拦截 API 请求

## 架构对比

### 修复前（无法登录）

```
┌─────────────────────────────────────────┐
│  Desktop 窗口 (wails.localhost:9245)    │
│  └─ AssetFileServerFS                   │
│     └─ 只能返回静态文件                 │
│        /api/login → 404 或 400          │
└─────────────────────────────────────────┘

┌─────────────────────────────────────────┐
│  Loadout Server (127.0.0.1:3000)        │
│  └─ 永远收不到请求                      │
└─────────────────────────────────────────┘
```

### 修复后（可以登录）

```
┌─────────────────────────────────────────────────────┐
│  Desktop 窗口 (wails.localhost:9245)                │
│  ├─ ProxyHandler                                    │
│  │  ├─ /api/* → 代理到 127.0.0.1:3000             │
│  │  └─ 其他   → AssetFileServerFS                  │
│  │              ├─ 开发: 代理到 Vite (:9245)       │
│  │              └─ 生产: embed FS                   │
│  └─ 内嵌 Loadout Server (127.0.0.1:3000)           │
└─────────────────────────────────────────────────────┘
```

## 测试验证

### 1. 重启开发模式

```powershell
powershell -File scripts/dev-desktop.ps1
```

### 2. 查看代理日志

终端应该显示：
```
[API Request] POST /api/login
[Proxy] POST /api/login -> http://127.0.0.1:3000/api/login
[Proxy] Content-Type: application/json
[Proxy] Content-Length: 42
[Proxy] Body size: 42 bytes                ← 现在有内容了！
```

### 3. 检查浏览器

按 `F12` 打开开发者工具：
- **Network 标签**
  - 请求 URL: `wails.localhost:9245/api/login`
  - Status: `200 OK`
  - Response Headers: `Set-Cookie: loadout_session=...`

- **Application 标签**
  - Cookies 中应该有 `loadout_session`
  - Domain: 空或 `wails.localhost`
  - Path: `/`
  - SameSite: `Lax`

## 相关文件

- `apps/desktop/backend/server/proxy.go` - API 代理实现
- `apps/desktop/backend/app/runner.go` - 使用代理 handler
- `apps/desktop/README.md` - 更新了架构说明和调试技巧
- `plugins/admin-api/service.go` - Cookie SameSite 配置
- `web/vite.config.js` - Vite proxy 配置（仅供直接访问 Vite 时使用）

## 注意事项

1. **生产模式也需要代理**
   - 生产打包后的 exe 文件同样使用 `ProxyHandler`
   - 静态资源从 embed FS 读取，API 请求代理到内嵌的 Loadout Server

2. **Vite proxy 配置保留**
   - 虽然 Desktop 模式不使用 Vite proxy
   - 但如果直接访问 `http://localhost:9245`（浏览器模式），仍然有效

3. **Cookie 安全**
   - 开发模式：HTTP，`Secure=false`，`SameSite=Lax`
   - 生产模式：同样是 HTTP（本地），但在同一进程内无跨域问题

## 后续优化（可选）

1. **支持 HTTPS（本地自签名证书）**
   - 可以设置 `Secure=true`
   - 需要生成并信任自签名证书

2. **优化日志级别**
   - 生产模式下可以减少日志输出
   - 添加调试模式开关

3. **性能监控**
   - 添加请求耗时统计
   - 监控代理失败率
