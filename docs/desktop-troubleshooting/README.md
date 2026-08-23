# Loadout 桌面端 & 3000 端口故障排查手册

> 2026-08-23 ~ 08-24 遇到的三类问题复盘：现象、根因、修复、验证，以及排查方法。
> 适用读者：维护 Loadout 桌面端（wails）+ 单端口分发（:3000）的人。

## 0. 架构速览（背景）

```
┌─────────────────────────────── apps/desktop（Wails v3 壳） ───────────────────────────────┐
│  main.go                        //go:embed all:frontend/dist  → 前端产物打进 exe            │
│  backend/app/runner.go          Wails 应用入口：Assets.Handler 三层包                       │
│    ├─ NewProxyHandler           拦截 /api /v1 /mcp → 反向代理 127.0.0.1:3000               │
│    ├─ SPAFallback                404 + 无扩展名 → 改写 index.html（SPA history 模式）      │
│    └─ BundledAssetFileServer    wails 内置文件服务（含 /wails/runtime.js）                 │
│  窗口内嵌 Loadout Server（同进程 goroutine）                                                │
└───────────────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────── apps/server（单端口 :3000） ───────────────────────────────┐
│  core/servercore/server.go      mux.Handle("/", spaFileServer(dist))                      │
│    └─ frontend.Dist             //go:embed all:dist（根 frontend/embed.go）               │
└───────────────────────────────────────────────────────────────────────────────────────────┘
```

关键事实：桌面端和 :3000 **各自独立 embed 一份前端产物**，但都来自同一份 `frontend/dist` 源码构建。因此两类服务的静态资源问题会**同时出现但互相掩盖**（见第 4 节）。

---

## 1. 问题 A：浏览器访问 :3000 报 `_plugin-vue_export-helper-*.js` 404

### 现象

- 桌面端 exe 一切正常；浏览器访问 `http://localhost:3000` 白屏/报错。
- DevTools 显示 `GET /assets/_plugin-vue_export-helper-BDNMzG2s.js → 404`。
- 前端控制台报「Failed to load module script」之类的资源加载失败。

### 根因

`frontend/embed.go` 用的是：

```go
//go:embed dist          // ← 默认会过滤下划线开头（_ 和 .）的文件！
var Dist embed.FS
```

Go 的 `//go:embed` **默认排除**文件名以 `_` 或 `.` 开头的条目（防止误嵌 Git 元数据等）。而 Vite 在构建时**必然**生成一个 `_plugin-vue_export-helper-<hash>.js`（Vue 插件内部共享 helper 的 vendor chunk）。embed 时被静默丢弃 → 前端 JS 执行到一半找不到这个 chunk → 崩。

`apps/desktop/main.go` 之所以没炸，是因为它早就用了 `//go:embed all:frontend/dist`（`all:` 前缀明确要求包含 `_`/`.` 开头文件）。两处写法不一致，导致「桌面端好、浏览器坏」。

### 修复（一行）

```go
//go:embed all:dist
var Dist embed.FS
```

### 验证

1. 编译后列出 embed 内容，确认 `_plugin-vue_export-helper-*.js` 在：

```go
//go:build ignore
package main

import (
    "fmt"
    "io/fs"
    "loadout/frontend"
)

func main() {
    _ = fs.WalkDir(frontend.Dist, "dist", func(p string, d fs.DirEntry, err error) error {
        if err == nil && !d.IsDir() {
            fmt.Println("EMBED:", p)
        }
        return err
    })
}
```

2. `curl -I http://localhost:3000/assets/_plugin-vue_export-helper-*.js` → `200`。

---

## 2. 问题 B：桌面端刷新任意前端路由报 `wails.localhost/xxx 404`

### 现象

- 桌面端内正常导航没问题（前端路由是 SPA 内部跳转，不重新发请求）。
- **直接刷新 / 手动输入 URL / 按 F5** 时，webview 加载 `wails.localhost/settings` → 404 错误页。
- 弹的是 Chromium 自己的错误页，不是应用内页面。

### 根因

wails 的 `BundledAssetFileServer` / `AssetFileServerFS` 本质是**裸 http.FileServer**：

- `/assets/xxx.js` → 文件存在 → 正常返回
- `/settings` → 磁盘上没有 `settings` 文件（也没有 `settings.html`）→ 404

SPA（Vue Router history 模式）要求：**服务端对任意「无扩展名且不存在」的路径回退到 index.html**，由前端 router 接管。裸 FileServer 没有这个行为。

### 修复

新增 `apps/desktop/backend/server/spa.go`：

- `SPAFallback(handler, fsys)`：用 buffered `ResponseWriter` 拦截上游响应。
  - 上游 2xx/3xx/其它 4xx → 原样透传。
  - 上游 404 且 URL 末段**含扩展名**（`/assets/missing.js`）→ 透传真实 404（不能把资源缺失伪装成 HTML 200）。
  - 上游 404 且 URL 末段**无扩展名**（`/settings`）→ 改写为 index.html + 200。

`runner.go` 组装：

```go
Handler: server.NewProxyHandler(
    server.SPAFallback(
        application.BundledAssetFileServer(assets),
        assets,
    ),
    "http://127.0.0.1:3000",
),
```

### 验证

- `GET /settings` → 200 + index.html（curl 或浏览器刷新）
- `GET /assets/_nonexistent.js` → **仍 404**（不被伪装成 HTML）

---

## 3. 问题 B′：修完 B 还 404 —— `readIndexHTML` 踩的第二个坑

### 现象

- 问题 B 修复合并后，重新打包桌面端，刷新 `/login` **仍然 404**。
- 但 :3000 端口刷新同一路由是正常的。两个入口行为不一致。

### 根因（这才是最阴的）

`SPAFallback` 第一版内部直接：

```go
indexBytes, err := fs.ReadFile(fsys, "index.html")
```

而 `fsys` 是 `//go:embed all:frontend/dist` 出来的 **embed.FS**。注意：**embed.FS 的根目录就是你 `//go:embed` 后面的那个目录**。

| embed 写法 | fsys 根 | 直接 ReadFile(fsys,"index.html") |
|---|---|---|
| `//go:embed dist` | `dist/*` | ✅ 读到 |
| `//go:embed all:frontend/dist` | `frontend/dist/*` | ❌ `file does not exist`（要 Sub 一层） |

项目里 `apps/desktop/main.go` 用的正是第二种。所以第一版 SPA fallback 在**构造时**就读 index.html 失败 → 退化成 `return handler`（透传）→ **整个 fallback 静默失效**，只剩 BundledAssetFileServer 的裸行为，刷新照样 404。而且**没有日志、没有 panic**，看起来就像「没修」。

### 为什么 :3000 正常掩盖了它

`:3000` 的 `core/servercore/server.go` 是另一条独立的链路，它**自己先做了一次 `fs.Sub`**：

```go
dist, _ := fs.Sub(frontend.Dist, "dist")
mux.Handle("/", spaFileServer(dist))
```

所以 :3000 端 `spaFileServer` 读 index.html 是成功的，SPA fallback 正常工作。**桌面端修复坏了、:3000 是好的，一好一坏互相掩盖，单独测任何一端都发现不了。**

### 修复

`SPAFallback` 内新增 `readIndexHTML(fsys)`，**兼容两种 embed 形态**：

```go
func readIndexHTML(fsys fs.FS) ([]byte, error) {
    // 形态 1：fsys 根就是 dist（直接读）
    if data, err := fs.ReadFile(fsys, "index.html"); err == nil {
        return data, nil
    }
    // 形态 2：fsys 根是 frontend/dist/*，WalkDir 找 index.html → fs.Sub → 读
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
        return fs.ReadFile(fsys, "index.html")
    }
    sub, err := fs.Sub(fsys, dir)
    if err != nil {
        return nil, err
    }
    return fs.ReadFile(sub, "index.html")
}
```

### 验证

```go
// 探测脚本，坐实根因：
fs.ReadFile(frontend.Dist, "index.html")   // FAIL file does not exist
sub, _ := fs.Sub(frontend.Dist, "dist")
fs.ReadFile(sub, "index.html")             // OK 690 bytes
```

修复后重新打包桌面端 → 刷新 `/login` 返回 200 + index.html，Vue Router 接管。

---

## 4. 问题 C（顺带）：关闭 webview 默认右键菜单 + Ctrl+R 刷新

### 需求

- 桌面端右键弹出的是 Chromium 内置菜单（返回/刷新/另存为/打印…），要关掉。
- 提供全局快捷键 `Ctrl+R` 执行刷新。

### 实现（runner.go 两个配置项）

```go
win := app.Window.NewWithOptions(application.WebviewWindowOptions{
    ...
    DefaultContextMenuDisabled: true,   // 关掉 webview 默认右键菜单

    KeyBindings: map[string]func(window application.Window){
        "Ctrl+R": func(w application.Window) {
            w.Reload()
        },
    },
})
```

要点：

- `DefaultContextMenuDisabled` 是 wails 一行开关，从 webview 配置层禁菜单；**别去前端 `oncontextmenu preventDefault`**——Chromium 内置菜单是它自己画的，前端拦不住。
- `KeyBindings` 走 wails 内置 accelerator 解析（大小写不敏感，如 `"Ctrl+R"`、`"CmdOrCtrl+R"`），跨平台自动适配（macOS 的 `Cmd` / Linux 的 `Ctrl`）。
- 语义选项：应用前台用 `KeyBindings`；若要「即使应用不在前台也响应」需用 `app.GlobalShortcut.Register`（OS 级 RegisterHotKey），有冲突风险，按需选。

---

## 5. 排查方法论（这次踩出来的通用流程）

1. **先分清「谁在服务」**：同一个 URL 在 dev / production、桌面端 / 浏览器下的服务方不同。先确认你测的是哪一条链路（vite dev server？wails asset server？Go :3000？）。
2. **不要只测一条链路**：桌面端和 :3000 是两套 embed + 两个 handler 栈。只修一处、只测一处，另一处坏了也不会发现（问题 B′ 就是这么漏的）。
3. **embed 的内容要「眼见为实」**：写一个临时 WalkDir 程序把 embed.FS 内容列出来，别猜。`//go:embed` 的过滤规则和根路径非常容易踩。
4. **buffered ResponseWriter 能救 404 但会杀流式**：SPA fallback 用缓冲拦截 404 没问题，但 SSE / 长连接必须绕过（本项目的 `/api` `/v1` `/mcp` 已在 ProxyHandler 层劫走，fallback 只碰静态资源，安全）。

## 6. 附：相关文件索引

| 文件 | 作用 |
|---|---|
| `frontend/embed.go` | 问题 A：`//go:embed all:dist` |
| `apps/desktop/main.go` | 桌面端 embed：`//go:embed all:frontend/dist`（本来就对） |
| `apps/desktop/backend/app/runner.go` | 三层 handler 组装 + 右键菜单/快捷键 |
| `apps/desktop/backend/server/spa.go` | SPAFallback + readIndexHTML（问题 B / B′） |
| `apps/desktop/backend/server/proxy.go` | API 反代劫持 |
| `core/servercore/server.go` | :3000 的 spaFileServer（独立链路，作为对照） |
| `scripts/pack-desktop.ps1` | 桌面端打包：pnpm build → robocopy → wails build |
