# Loadout Desktop

基于 Wails 的桌面版本，内嵌 Loadout Server 和前端界面。

## 架构说明

### 开发模式

```
┌─────────────────────────────────────────────────────┐
│  Desktop 窗口 (Wails)                                │
│  协议: wails.localhost:9245                          │
│  ├─ ProxyHandler (Go 层代理)                        │
│  │  ├─ /api/*, /v1/*, /mcp/*                        │
│  │  │  └─> 代理到 Loadout Server (127.0.0.1:3000)  │
│  │  └─ 其他请求                                      │
│  │     └─> Vite 开发服务器 (localhost:9245)         │
│  └─ 内嵌 Loadout Server: 127.0.0.1:3000             │
└─────────────────────────────────────────────────────┘
           │
           ▼ (静态资源)
┌─────────────────────────────────────────┐
│  Vite 开发服务器 (:9245)                │
│  ├─ 前端: web/                          │
│  └─ 热重载 (HMR)                        │
└─────────────────────────────────────────┘
```

**特点：**
- ✅ **API 请求**由 Go 层的 ProxyHandler 直接代理到 Loadout Server
- ✅ **静态资源**（HTML/JS/CSS）由 Wails 代理到 Vite 开发服务器
- ✅ **Cookie 正确传递**，无跨域问题
- ✅ 前端修改自动热重载（Vite HMR）
- ⚠️ 后端修改需要重启脚本

### 生产模式

```
┌─────────────────────────────────────────────────────┐
│  Desktop 窗口 (Wails)                                │
│  协议: wails.localhost                               │
│  ├─ ProxyHandler (Go 层代理)                        │
│  │  ├─ /api/*, /v1/*, /mcp/*                        │
│  │  │  └─> 代理到 Loadout Server (127.0.0.1:3000)  │
│  │  └─ 其他请求                                      │
│  │     └─> 静态资源 (embed frontend/dist)           │
│  └─ 内嵌 Loadout Server: 127.0.0.1:3000             │
└─────────────────────────────────────────────────────┘
           │
           ▼ (静态资源)
┌─────────────────────────────────────────┐
│  Embed FS (frontend/dist/)               │
│  ├─ index.html                           │
│  ├─ assets/*.js                          │
│  └─ assets/*.css                         │
└─────────────────────────────────────────┘
```

**特点：**
- ✅ **单一可执行文件**，包含前端和后端
- ✅ **API 请求**由 ProxyHandler 代理到内嵌的 Loadout Server
- ✅ **静态资源**从 embed FS 读取（编译时嵌入）
- ✅ 无外部依赖，开箱即用

## 快速开始

### 开发模式

```powershell
# 从根目录运行
powershell -File scripts/dev-desktop.ps1
```

这会：
1. 更新 Go 依赖
2. 检查并安装前端依赖
3. 清理端口冲突
4. 启动 Vite (localhost:9245)
5. 启动 Desktop 应用（内嵌 Loadout Server 在 127.0.0.1:3000）

**默认登录凭证：**
- 用户名：`admin`
- 密码：首次启动时随机生成，查看终端输出或日志文件

### 打包生产版本

```powershell
# 从根目录运行
powershell -File scripts/pack-desktop.ps1
```

这会：
1. 构建前端 (`web/` → `web/dist`)
2. 复制到 `apps/desktop/frontend/dist`
3. 生成 Windows 图标资源（可选，需要 rsrc）
4. 构建 Go 可执行文件到 `apps/desktop/dist/loadout-desktop.exe`

## 技术栈

- **Go 1.21+** - 后端和桌面应用框架
- **Wails v2** - Go + Web 桌面框架
- **Vue 3** - 前端框架
- **Vite** - 前端构建工具

## 目录结构

```
apps/desktop/
├── backend/
│   ├── app/           # Wails 应用入口
│   ├── server/        # Loadout Server 生命周期管理
│   ├── singleton/     # 单实例互斥
│   └── config.go      # Desktop 配置
├── frontend/
│   └── dist/          # 生产模式的前端静态文件（从 web/dist 复制）
├── scripts/
│   └── pack.ps1       # 原有打包脚本（已废弃，使用根目录的）
├── wails.json         # Wails 配置
├── go.mod
└── README.md          # 本文件
```

## 常见问题

### 1. 登录失败 / Cookie 问题

**症状：**
- 登录请求返回 200，但前端仍显示未登录
- Cookie 无法保存
- 刷新后需要重新登录

**解决方案（已修复）：**

在 `apps/desktop/backend/server/proxy.go` 中实现了 **Go 层 API 代理**：
- ✅ API 请求 (`/api/*`, `/v1/*`, `/mcp/*`) 直接由 Go 层代理到 Loadout Server
- ✅ 避免了 Vite proxy 的跨域问题
- ✅ Cookie 在同一进程内传递，无跨域限制

**验证方法：**
1. 重启开发脚本：`powershell -File scripts/dev-desktop.ps1`
2. 打开浏览器开发者工具（F12）
3. 登录时查看 Network → Response Headers
4. 应该看到 `Set-Cookie: loadout_session=...; Path=/; SameSite=Lax`

### 2. 端口冲突

如果提示端口 9245 或 3000 被占用：

```powershell
# 检查占用进程
netstat -ano | findstr :9245
netstat -ano | findstr :3000

# 终止进程（替换 <PID>）
taskkill /PID <PID> /F

# 或使用脚本自动清理
powershell -File scripts/dev-desktop.ps1
```

### 3. 前端不更新

开发模式下前端修改应该自动热重载。如果没有：
1. 检查 Vite 进程是否正常运行
2. 查看浏览器控制台（F12）是否有 WebSocket 连接错误
3. 重启开发脚本

### 4. 后端修改不生效

后端代码（Go）修改需要重启开发脚本：
1. 关闭 Desktop 窗口
2. 重新运行 `scripts/dev-desktop.ps1`

### 5. rsrc 工具缺失

如果打包时提示 `rsrc` 未找到，这不影响功能，只是无法嵌入图标。

安装 rsrc：
```powershell
go install github.com/akavel/rsrc@latest
```

## 环境变量

开发模式下自动设置：
- `FRONTEND_DEVSERVER_URL=http://localhost:9245` - 指向 Vite 开发服务器
- `LOADOUT_RUN_MODE=desktop` - 标记为 Desktop 模式，后端监听 127.0.0.1

生产模式下：
- `LOADOUT_RUN_MODE=desktop` - 自动设置

## 调试技巧

### 查看 API 代理日志

ProxyHandler 会在终端输出所有 API 请求：
```
[API Request] POST /api/login
[Proxy] POST /api/login -> http://127.0.0.1:3000/api/login
```

如果看不到这些日志，说明请求没有经过代理，可能是路径匹配问题。

### 查看应用日志

Desktop 应用的日志位于：
- Windows: `%USERPROFILE%\.loadout\logs\loadout.log`

### 开发者工具

在 Desktop 窗口中按 `F12` 打开浏览器开发者工具，查看：
- **Console**：JavaScript 错误和日志
- **Network**：API 请求和响应
  - 检查请求 URL 是否正确（应该是 `wails.localhost:9245/api/...`）
  - 检查响应头是否包含 `Set-Cookie`
- **Application**：Cookie 和 LocalStorage
  - 确认 `loadout_session` Cookie 是否存在
  - 检查 Cookie 的 `Domain`, `Path`, `SameSite` 属性

### 后端调试

在 `apps/desktop/backend/server/proxy.go` 中已有详细日志。

如果需要调试 Loadout Server，在 `loadout.go` 中添加日志：
```go
log.Printf("调试信息: %v", someValue)
```

## 相关文档

- [Wails 文档](https://wails.io/docs/introduction)
- [Loadout Server 文档](../../core/README.md)
- [前端文档](../../web/README.md)
