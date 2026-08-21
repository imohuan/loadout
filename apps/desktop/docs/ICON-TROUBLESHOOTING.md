# Loadout Desktop 窗口图标排查记录（Windows / Wails v3）

> 结论先行：**Wails v3 alpha2.119 窗口标题栏左侧小图标（ICON_SMALL）默认是系统占位图标（IDI_APPLICATION），必须自己补 WM_SETICON ICON_SMALL 才能换成应用图标**。本项目的 seticon_windows.go 一直在做这件事，但 FindWindowW 的窗口类名写错了（`"LoadoutWnd"` vs Wails 实际的 `"WailsWebviewWindow"`），导致补丁从未生效。
>
> 本文记录根因、排查过程与预防清单，避免以后再踩。

---

## 一、问题现象

- 窗口标题栏左侧图标、任务栏小图标一直显示"浅色方块"（Windows 系统默认占位图标）。
- 替换 `apps/desktop/icons/appicon.ico`（Loadout 紫底白 L 品牌图标）并重新打包后，图标依然不生效。
- 窗口标题正常（Loadout），说明 exe 资源 / 窗口创建本身没问题，纯粹是**小图标没有被设置**。

## 二、结论（TL;DR）

Wails v3 alpha 有两个坑叠加：

1. **窗口类注册时 `IconSm = IDI_APPLICATION`**（`application_windows.go:209,220`）→ 标题栏左侧小图标默认就是系统占位。
2. **Wails 内部 `setIcon` 只发 `WM_SETICON ICON_BIG`**（`webview_window_windows.go:1432-1434`），**从不设 ICON_SMALL**。

→ 必须自己在窗口创建后补发 `WM_SETICON ICON_SMALL`。

而项目自带的 `seticon_windows.go` 本来就在补 ICON_SMALL（line 62），但 **FindWindowW 用的窗口类名是 `config.App.Name + "Wnd"`（"LoadoutWnd"）**，Wails v3 实际默认窗口类名是 **`"WailsWebviewWindow"`**（`application.go:208-209`）——类名对不上 → 5 秒轮询找不到 HWND → 静默 return，ICON_SMALL 从未设上。

## 三、排查过程（时间线）

1. **直觉层**：先怀疑图标文件没换对 → 用 Pillow 生成 Loadout 品牌多分辨率 `.ico` 替换 `appicon.ico` → 重打包 → **依然浅色方块**。
2. **HTML 层**：怀疑 webview2 用页面 favicon 当标题栏图标（`frontend/index.html` 原本引用 `favicon.svg`）→ 换成 `favicon.ico` 并同步副本 → **依然无效**（后确认与 HTML 无关）。
3. **Wails 源码层**（关键，`go env GOMODCACHE` 定位）：
   - `webview_window_windows.go:1432` → `setIcon` 只发 `ICON_BIG`；
   - `application_windows.go:209-220` → 窗口类 `Icon = IconSm = IDI_APPLICATION`；
   - `application.go:208-209` → 默认窗口类名 `"WailsWebviewWindow"`。
4. **对照项目代码**：`seticon_windows.go` 类名传参 = `config.App.Name + "Wnd"`（runner.go:68 原代码）→ **类名不匹配，补丁从未生效**。
5. 修正类名 → 重新打包 → 生效。

## 四、根因（逐条展开）

### 4.1 Wails v3 alpha 的窗口图标机制

| 层 | 源码位置 | 行为 |
|---|---|---|
| `application.Options.Icon` | `application.go` / issue #3487 | **v3 alpha 上不驱动 webview2 窗口图标**（官方 issue 至今未修） |
| 窗口类注册 | `application_windows.go:209-220` | `Icon = IconSm = LoadIconWithResourceID(instance, IDI_APPLICATION)` = 系统默认占位 |
| 创建后设置 | `webview_window_windows.go:545-557` | `NewIconFromResource(instance, 3)`（exe 资源 ID 3，rsrc 生成）→ fallback `application.Options.Icon` → `setIcon` |
| `setIcon` | `webview_window_windows.go:1432-1434` | **只发 `WM_SETICON ICON_BIG`**，不设 ICON_SMALL |
| `WebviewWindowOptions` | `webview_window_options.go:43-170` | **没有 Icon 字段**（Windows 段只有 `DisableIcon`，是"禁用"不是"设置"） |

**结论**：Wails v3 alpha 在 Windows 上没有任何 API 能把小图标（标题栏左侧/任务栏）设成应用图标——要么用窗口类 IconSm（被 IDI_APPLICATION 占了），要么自己补 WM_SETICON ICON_SMALL。**必须自己补**。

### 4.2 项目侧：类名错误导致补丁静默失效

- `runner.go`（原）：`wndClass := config.App.Name + "Wnd"` → `"LoadoutWnd"`
- Wails v3 alpha 默认窗口类名：`"WailsWebviewWindow"`（`application.go:208-209`，`application.Options.Windows.WndClass` 为空时的默认值）
- `seticon_windows.go` 的 `FindWindowW(cls, 0)` 用类名精确匹配，5 秒（50×100ms）找不到就 return——**没有任何报错**，属于典型的"静默失败"。

### 4.3 图标文件本身是 Wails 模板

- `apps/desktop/icons/appicon.ico` 是 Wails v3 模板自带（紫底白 W），**不是 Loadout 品牌**。
- `frontend/public/favicon.ico` 是 `appicon.ico` 的副本——**改 appicon.ico 后若不同步，webview2 兜底路径仍会用旧图标**（本次 md5 不一致已确认）。

## 五、修复方案

### 5.1 类名修正（关键）

`apps/desktop/backend/app/runner.go`：

```go
// Wails v3 窗口图标：application.Options.Icon 在 v3 alpha 上不完全生效，
// 且 Wails 内部 setIcon 只设 ICON_BIG（不设 ICON_SMALL），而注册窗口类时
// IconSm = IDI_APPLICATION（系统默认占位）→ 标题栏左侧小图标是浅色方块。
// 我们补 ICON_SMALL：用 Wails v3 默认窗口类名 WailsWebviewWindow 找 HWND。
if runtime.GOOS == "windows" {
    setWindowIconAfterCreate("WailsWebviewWindow", config.App.Custom.Window.Title, appIcon)
}
```

`seticon_windows.go` 保持原有逻辑（ICON_BIG + ICON_SMALL 都发），注释改为说明这是"补 ICON_SMALL 的关键路径"而非 FALLBACK。

### 5.2 图标源

- `appIcon := icons.AppIcon`（`//go:embed appicon.ico`，不依赖运行 cwd——exe 从 dist/ 启动也拿得到）。
- `appicon.ico` 必须包含 16/32/48/64/128/256 多分辨率（PNG-in-ICO 即可，Windows Vista+ 原生支持）。

### 5.3 重新打包

`scripts/pack-desktop.ps1`：[4/5] rsrc 用新 `appicon.ico` 生成 `backend/app/appicon_windows_amd64.syso`（资源 ID 3 = 图标 group，Wails `NewIconFromResource(instance, 3)` 可读）→ [5/5] go build 编译进 exe。

## 六、关键知识点速查

- **Windows 窗口图标三层**：
  1. exe 资源图标（资源管理器文件图标、未显式设置时窗口图标兜底）
  2. `WM_SETICON ICON_BIG` → Alt+Tab / 任务栏预览大图标
  3. `WM_SETICON ICON_SMALL` → 标题栏左侧 / 任务栏小图标（**最容易被忽略**）
- **窗口类 `IconSm`**：注册窗口类时不设置就会用 `IDI_APPLICATION`（系统默认占位 = 浅色方块）。
- **Wails v3 alpha 已知坑**：
  - `application.Options.Icon` 不驱动 webview2 窗口图标（issue #3487，未修）
  - `WebviewWindowOptions` 没有 Icon 字段
  - `setIcon` 只设 ICON_BIG
  - 默认窗口类名 `"WailsWebviewWindow"`（`application.go:208-209`）
- **rsrc 生成的 .syso**：图标 group 资源 ID = 3，Wails 从 exe 资源 `NewIconFromResource(hInstance, 3)` 加载（`webview_window_windows.go:548`）。
- **svg → ico 的坑**（生成 Loadout 品牌图标时踩到）：
  - `cairosvg`：Windows 缺 cairo 系统 DLL（`OSError: no library called "cairo-2"`）
  - `resvg-py`：对含 `<mask>`/`<filter>` 的复杂 svg 渲染会出错（出白块）
  - 最稳：**Pillow 直接画**（紫底 + 白字，多分辨率合成 PNG-in-ICO），或用户提供现成 `.ico`

## 七、预防清单（换图标 / 升级 Wails 时照此检查）

1. **替换图标**：`apps/desktop/icons/appicon.ico`（必须含 16x16 层，否则小图标糊）+ 同步 `frontend/public/favicon.ico`（md5 应一致）。
2. **确认类名**：grep Wails 源码 `WndClass` 默认值（`application.go`），与 `seticon_windows.go` 的 `FindWindowW` 入参一致。**升级 Wails 版本后必查**——类名可能变。
3. **确认 exe 资源**：`backend/app/appicon_windows_amd64.syso` 时间戳应晚于 appicon.ico 修改时间；[4/5] rsrc 步骤输出 OK 而非 WARN。
4. **三处验证**：启动后依次看
   - 标题栏左侧小图标（ICON_SMALL）
   - Alt+Tab / 任务栏预览（ICON_BIG）
   - 资源管理器里 exe 文件图标（exe 资源）
   三处都是 Loadout 图标才算完成。
5. **若仍不生效**：
   - 用 Spy++ / `GetWindowLong` 确认实际窗口类名，先排除类名不匹配；
   - 查 `.syso` 资源 ID 是否为 3（`ResourceHacker` 可看）；
   - 查 `seticon_windows.go` 临时文件写入与 `LoadImageW` 是否成功（可临时打日志）。
