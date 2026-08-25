# 代码开发注意事项（踩坑集）

> 本文件记录 Loadout 开发中反复踩到、且不容易从代码一眼看出的「隐性约束」。
> 新增功能或改前端/桌面端时，先扫一遍相关条目，避免重蹈覆辙。
> 一切以当前代码为准；本文件只解释「为什么这样写」，不替代代码本身。

## 1. 桌面端 `wails.localhost` 是伪 host，前端不要把 origin 当真实可达地址

**现象**：桌面壳（Wails v3 Windows）里前端 `location.origin` 永远是 `http://wails.localhost`。
它是框架注入的**伪 host**——只在 WebView 内部可解析，外部浏览器、其他进程、DNS 全都不认识。

**坑**：
- 把 `location.origin` 拼成绝对地址**暴露给外部 / 写进 MCP 客户端配置 / 复制给用户**会失效
  （外部连不上 `wails.localhost`）。
- 正确做法：前端需要「真实可达地址」时，调用后端 `GET /api/desktop-base`
  （返回 `http://127.0.0.1:<port>`），见 `frontend/src/lib/base.ts` 的 `getLoadoutBase()`。
- 后端两处 `isDesktopOrigin`（判断 `wails.localhost`）是**有意保留**的：用于区分
  「桌面端登录」与「浏览器登录」，逻辑正确，勿删。

## 2. SSE / 长连接绝不能走 `wails.localhost`，且必须带鉴权 token

**现象**：桌面壳内 `EventSource('/api/processes/stream')` 永远 0 返回。

**根因（两个叠加）**：
1. **缓冲坑**：SSE 直走 `wails.localhost` 时，Wails 的 `WebResourceRequested` 桥接会缓冲响应体
   到「响应结束」才回传 WebView；而 SSE 永不结束 → 前端一直 pending，收不到任何事件。
   （短请求不受影响，仅流式/长连接中招。）
2. **鉴权坑**：若 SSE 改为直连真实地址 `http://127.0.0.1:<port>` 绕过上述缓冲，登录 Cookie
   却绑在 `wails.localhost` 这个 host 上——跨 host 浏览器不携带 Cookie → 后端判定未登录 →
   仍被拦截（0 返回）。Cookie 的 Domain 无法设成同时覆盖两个不同 host。

**正确做法（当前实现）**：
- 前端 SSE **直连真实地址**（`getLoadoutBase()` + 路径），绕过缓冲坑。
- 鉴权走 **query token** 而非 Cookie：先用带 Cookie 的同源请求
  `POST /api/sse-token`（限本机回环）换取 5 分钟短期 JWT，再以 `?sse_token=xxx` 建立 SSE。
  后端 `SessionMiddleware` 已支持「Cookie 或 `?sse_token=` 任一通过即放行」。
- 涉及文件：
  - `frontend/src/lib/base.ts`：`getLoadoutBase()`（真实地址）、`getSseToken()`（换 token 并缓存）
  - `frontend/src/composables/useProcessMonitor.ts`：进程流 SSE
  - `frontend/src/components/StreamLogPanel.vue`：通用日志流 SSE（unifyai / skills 更新流复用）
  - `plugins/admin-auth/service.go`：`SessionMiddleware` 的 `?sse_token=` 回退 + `ClaimsFromContext`
  - `plugins/admin-api/service.go`：`POST /api/sse-token` 端点

**为什么用 query token 而不是自定义 Header**：`EventSource` 规范不允许设置自定义请求头，
只能借 URL query 携带凭据。这是浏览器限制，非本项目特有。

**新增 SSE 端点时的检查清单**：
- [ ] 后端 handler 设 `Content-Type: text/event-stream` + `Cache-Control: no-cache` + `X-Accel-Buffering: no`
- [ ] 用 `w.(http.Flusher).Flush()` 每次写入后立即 flush
- [ ] 桌面壳代理 `proxy.go` 已设 `FlushInterval = -1`（每次 Write 立即 flush），勿改回 0
- [ ] 前端用 `getLoadoutBase()` 直连真实地址 + `getSseToken()` 拼 `?sse_token=`
- [ ] 路由注册时 `Auth: plugin.AuthSession`（Cookie 或 sse_token 均能过）

## 3. 桌面壳代理层对 SSE 的 flush 配置

`apps/desktop/backend/server/proxy.go` 的 `ReverseProxy.FlushInterval = -1` 是让 SSE 在
代理层「每次 Write 立即 flush」的关键。若改回默认 0，SSE 会卡在代理缓冲里。
`ModifyResponse` 只动 Cookie 的 Domain/Path，不读 body，不会缓冲 SSE body。
