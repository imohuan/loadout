# Loadout 桌面端 ⇄ 网页端免登录（SSO）机制

> 2026-08-24 实现。桌面托盘「打开网页」一键免登录的完整链路：设计动机、时序、各端实现、安全模型、踩过的坑。
> 适用读者：维护 Loadout 桌面端 / admin-api 认证 / 前端登录流程的人。

## 1. 为什么需要 SSO（问题根源）

桌面版（Wails v3 + WebView2）和浏览器（Chrome）访问同一个 `http://127.0.0.1:3000`，但**两者的 cookie 存储物理隔离**：

| | WebView2（桌面） | Chrome（浏览器） |
|---|---|---|
| cookie 存储 | 独立 profile（`%LOCALAPPDATA%` 下） | Chrome 自己的 profile |
| 登录态 | `loadout_session` cookie 只在 WebView2 里 | 只在 Chrome 里 |

叠加两个事实，导致「登录一次两边通用」这条路彻底走不通：

1. `loadout_session` 是 **HttpOnly** cookie —— 前端 JS 读不到值，无法复制。
2. Wails v3 **没有暴露 cookie 读取 API** —— 桌面 Go 侧也拿不到 WebView2 里的会话。

所以免登录只能走**应用层 token 传递**：桌面端用 Loadout 自己的密钥签一个短效 JWT，塞进 URL，网页版拿它换正式会话。

## 2. 整体设计

```
桌面 WebView（已登录）          桌面 Go 侧（托盘）                 系统浏览器（网页版）
       │                              │                                │
       │  POST /api/login             │                                │
       │  Origin: wails.localhost     │                                │
       │  → Set-Cookie + 写标记       │                                │
       │                              │                                │
       │                              │  读 desktop-session.json       │
       │                              │  (有标记? → 签 30s JWT)         │
       │                              │                                │
       │                              │ OpenURL(?sso=<jwt>) ──────────►│
       │                              │                                │  读 URL 里 sso token
       │                              │                                │  → 立即清地址栏(不刷新)
       │                              │                                │  → POST /api/sso/login
       │                              │                                │  → Set-Cookie（正式会话）
       │                              │                                │  → 进入主页 ✓
```

**核心约束：SSO 绑定桌面登录态**。只有桌面 WebView 登录过，托盘「打开网页」才签 token；桌面未登录则打开不带 token 的地址，网页保持自身登录态。

## 3. 各端实现

### 3.1 共享常量 `loadout/core/config/config.go`

```go
// DesktopSessionFile 桌面端登录标记文件：admin-api 在桌面 WebView 登录成功时写入、
// 登出时删除；desktop 托盘据此判断「软件中是否已登录」，决定是否签发免登录 token。
const DesktopSessionFile = "desktop-session.json"
```

### 3.2 后端 `plugins/admin-api/service.go`

三个改动点：

**a) `POST /api/sso/login`（换票接口，公开）** —— 校验短效 JWT，用 claims 里的用户名**重新签发**完整 TTL 会话写入 cookie：

```go
func (s *Service) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
    if !isLoopback(r.RemoteAddr) {                 // 仅本机回环可调
        writeError(w, http.StatusForbidden, "仅允许本机调用")
        return
    }
    // 校验短效 token → 用 claims.Username 重签完整 TTL 会话（关键！）
    claims, err := auth.ParseToken(s.st.SecretKey(), req.Token)
    token, err := auth.SignToken(s.st.SecretKey(), claims.Username,
        time.Duration(config.SessionTTLHours)*time.Hour)
    http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, ...})
}
```

> **为什么不能直接把短效 token 当 cookie 用**：cookie 会继承 30 秒过期时间，网页版 30 秒后掉线。换票时按用户名**重签**正式 TTL 的会话。

**b) 来源判断 `isDesktopOrigin(r)`** —— 用 Origin 头区分「桌面 WebView」与「浏览器」：

```go
func isDesktopOrigin(r *http.Request) bool {
    origin := r.Header.Get("Origin")
    if origin == "" { origin = r.Referer() }
    return strings.Contains(origin, "wails.localhost") || strings.Contains(origin, ":9245")
}
```

- 桌面 WebView 页面 origin：`wails.localhost`（生产）/ vite dev `:9245`
- 浏览器页面 origin：`127.0.0.1:3000`

**c) 桌面登录标记（增删对称）** —— 只有桌面来源的登录/登出才动标记文件：

```go
// handleLogin 成功时
if isDesktopOrigin(r) { s.markDesktopSession(req.Username) }   // 写标记

// handleLogout（公开、幂等，修 401）
if isDesktopOrigin(r) { s.clearDesktopSession() }               // 只桌面来源才删标记
```

标记文件 `config.DataDir/desktop-session.json`：
```json
{"username":"admin","login_at":"2026-08-24T00:42:00+08:00"}
```

### 3.3 桌面端 `apps/desktop/backend/app/tray.go`

托盘「打开网页」的地址生成 `ssoWebURL()`：

```go
func ssoWebURL() string {
    // 读标记文件：桌面未登录 → 打开不带 token 的地址
    sessionPath := filepath.Join(lconfig.DataDir, lconfig.DesktopSessionFile)
    username := ""   // 读标记 → 有则 username，无则 ""
    if username == "" { return webURL }

    st, _ := store.New(lconfig.DataDir)                       // 与后端同一数据目录 → 同一密钥
    token, _ := auth.SignToken(st.SecretKey(), username, 30*time.Second)
    return webURL + "/?sso=" + token                          // 30 秒短效
}
```

要点：桌面 Go 侧**直接用 Loadout 的密钥**（`store.SecretKey()`，与后端同一 `config.DataDir`）签 token，**不依赖 WebView2 的登录态**——这正是「绑定桌面登录态」由标记文件来实现的原因。

### 3.4 前端 `frontend/src/router/index.ts`

**先读后清**（顺序必须这样，否则 token 丢失）：

```ts
// 模块加载时（最早时机）读取并缓存 token，同时立刻清掉地址栏 ?sso=
let ssoToken: string | null = (() => {
    const t = new URLSearchParams(window.location.search).get('sso')
    if (t) {
        const url = new URL(window.location.href)
        url.searchParams.delete('sso')
        window.history.replaceState({}, '', url.pathname + url.search)
    }
    return t
})()

router.beforeEach(async (to) => {
    const auth = useAuthStore()
    if (ssoToken && !auth.authenticated) {
        const token = ssoToken
        ssoToken = null          // 无论成败只跑一次！防止登出后被反复重试
        try { await auth.ssoLogin(token) } catch { /* 过期/无效 → 正常登录页 */ }
    }
    ...
})

// 导航完成后兜底清一次（历史前进/后退把 sso 带回地址栏等边界）
router.afterEach(() => { /* 有 ?sso= 就清 */ })
```

`ssoLogin`（`stores/auth.ts`）只是调换票接口 + 重新 check：

```ts
async function ssoLogin(token: string) {
    await request('/api/sso/login', 'POST', { token })
    await check()
}
```

## 4. 安全模型

| 风险 | 对策 |
|---|---|
| token 泄露（地址栏残留） | 三重清理：模块加载清 + beforeEach + afterEach；token 30 秒短效 |
| 局域网盗用换票接口 | `handleSSOLogin` 仅限 `isLoopback`（127.0.0.1 / ::1 / localhost） |
| 伪造 token | JWT 由 HMAC-SHA256 签名，密钥在 `config.DataDir/.secret`，非本机不可读 |
| 未登录被自动登录 | SSO 绑定标记文件：仅桌面 WebView 登录过才签发 token |

语义约定：**本机是可信区域**。任何能运行桌面程序的人，在桌面登录后即可免密打开网页版（等同本机免密）。

## 5. 踩过的坑（回归记录）

### 5.1 登出被自动登录回来

**现象**：SSO 登录后点登出，立刻又被自动登录（Network 面板见 `POST /api/sso/login` + `token: eyJ...`）。

**根因**：`beforeEach` 条件是 `if (ssoToken && !auth.authenticated)`——登出后 `authenticated=false`、`ssoToken` 仍持有旧值 → 每次导航都重新换票。

**修复**：进入 if 块立刻 `ssoToken = null`（无论成败），保证只跑一次。

### 5.2 浏览器登出误删桌面登录标记

**现象**：网页端退出登录后，托盘「打开网页」进入网页仍是未登录。

**根因**：`handleLogout` 无条件 `clearDesktopSession()`——浏览器登出把「桌面 webview 登录」的标记也删了。

**修复**：`handleLogout` 也加 `isDesktopOrigin(r)` 判断，与 `handleLogin` 对称——只桌面来源的登出才删标记。

### 5.3 先清后读导致 token 丢失

**现象**（设计时规避）：如果先清 URL 再读 token，模块加载后 `window.location.search` 已无 `sso`，`ssoToken` 为 null，免登录静默失效。

**对策**：模块加载时用 IIFE「读 → 缓存 → 清」一次性完成，顺序保证。不要在 `main.ts` 挂载前手动清 URL。

## 6. 验证

后端测试（`plugins/admin-api/admin_api_test.go` 的 `TestDesktopSessionMark` / `TestSSOLogin`）：

1. 桌面来源登录 → 标记文件生成且 username 正确
2. 浏览器来源登录 → 不写标记
3. 桌面登出 → 标记删除
4. **浏览器登出 → 标记保留**（关键回归）
5. SSO 换票：有效 token → 200 + 新 cookie 可访问受保护接口；无效 401；缺参 400

手动链路验证（需重新跑 `scripts/pack-desktop.ps1` 打包）：

```
打开软件 → 不登录 → 托盘「打开网页」→ 浏览器应显示登录页
软件里登录 → 托盘「打开网页」→ 浏览器应直接进主页
网页端登出 → 托盘「打开网页」→ 浏览器应仍免登录（标记保留）
软件里登出 → 托盘「打开网页」→ 浏览器应显示登录页
```

## 7. 相关文件清单

| 文件 | 职责 |
|---|---|
| `loadout/core/config/config.go` | `DesktopSessionFile` 常量 |
| `plugins/admin-api/service.go` | `handleSSOLogin` / `isDesktopOrigin` / `markDesktopSession` / `clearDesktopSession` |
| `apps/desktop/backend/app/tray.go` | `ssoWebURL()` 生成免登录地址 |
| `frontend/src/router/index.ts` | 读缓存 token + 清 URL + beforeEach/afterEach |
| `frontend/src/stores/auth.ts` | `ssoLogin()` 换票 |
| `plugins/admin-api/admin_api_test.go` | 标记机制 + SSO 换票测试 |
