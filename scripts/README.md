# Loadout 打包和开发脚本

本目录包含 Loadout 项目的打包和开发脚本。

## 目录结构

```
scripts/
├── pack.ps1              # 主打包脚本 (Windows)
├── pack.sh               # 主打包脚本 (Linux/macOS)
├── pack-desktop.ps1      # Desktop 应用打包 (Windows)
├── pack-desktop.sh       # Desktop 应用打包 (Linux/macOS)
├── dev-desktop.ps1       # Desktop 开发模式 (Windows)
├── dev-desktop.sh        # Desktop 开发模式 (Linux/macOS)
└── README.md             # 本文件
```

## 开发模式

### Desktop 应用开发

**Windows:**
```powershell
powershell -File scripts/dev-desktop.ps1
```

**Linux/macOS:**
```bash
bash scripts/dev-desktop.sh
```

**特性：**
- ✅ 自动启动 Vite 开发服务器（端口 9245）
- ✅ 前端热重载（修改 `web/` 下的代码会自动刷新）
- ✅ 内嵌 Loadout Server（无需单独启动后端）
- ✅ 自动传递 `FRONTEND_DEVSERVER_URL` 环境变量
- ✅ 关闭窗口自动清理所有进程

**工作流程：**
1. 更新 Desktop 的 Go 依赖
2. 检查前端依赖（如需要会自动安装）
3. 启动 Vite 开发服务器
4. 构建并启动 Desktop 应用（内嵌 Server）

**开发时前端不需要预构建**，Vite 会实时提供热重载。

## 生产打包

### Desktop 应用打包

**Windows:**
```powershell
powershell -File scripts/pack-desktop.ps1
```

**Linux/macOS:**
```bash
bash scripts/pack-desktop.sh
```

**输出：** `apps/desktop/dist/loadout-desktop.exe`（单个可执行文件，内嵌 Server）

**特性：**
- ✅ 单个 exe，无需外部依赖
- ✅ 内嵌 Loadout Server（自动启动）
- ✅ 包含图标资源

**工作流程：**
1. 更新 Desktop 的 Go 依赖
2. 构建前端（`web/` → `apps/desktop/frontend/dist`）
3. 生成 Windows 图标资源（rsrc）
4. 构建 Desktop 应用（Go）

### Server 应用打包

**Windows:**
```powershell
powershell -File scripts/pack.ps1 server
```

**Linux/macOS:**
```bash
bash scripts/pack.sh server
```

**输出：** `bin/loadout.exe`

### 打包所有目标

**Windows:**
```powershell
powershell -File scripts/pack.ps1
# 或
powershell -File scripts/pack.ps1 all
```

**Linux/macOS:**
```bash
bash scripts/pack.sh
# 或
bash scripts/pack.sh all
```

**输出：**
- `bin/loadout.exe` - Server 应用
- `apps/desktop/dist/loadout-desktop.exe` - Desktop 应用（内嵌 Server）

## 前置要求

### 所有平台
- **Node.js** 18+ 和 **npm**（前端构建）
- **Go** 1.21+（后端构建）

### Windows 额外要求
- **rsrc**（可选，用于嵌入图标）
  ```powershell
  go install github.com/akavel/rsrc@latest
  ```

## 架构说明

### Desktop 应用架构

Desktop 应用使用以下架构：

```
loadout-desktop.exe
├── Wails v3 窗口管理
├── 前端 (web/ 构建产物)
│   └── Vue + Vite
└── 后端
    ├── Desktop API (/__api/*)  - 窗口控制
    └── Loadout Server (内嵌)   - 完整后端服务
        ├── /api/*
        ├── /v1/*
        └── /mcp/*
```

**开发模式：**
- 前端：Vite 开发服务器（`http://localhost:9245`）
- Desktop 通过 `FRONTEND_DEVSERVER_URL` 环境变量连接到 Vite
- 修改 `web/` 下的代码会自动热重载
- 无需预构建前端

**生产模式：**
- 前端：嵌入到 exe 中的静态文件
- Desktop 从 `frontend/dist/` 读取

## 常见问题

### Q: Desktop 应用启动后看不到前端？

**A:** 检查以下几点：
1. 开发模式：确保 Vite 启动成功（控制台会显示 `http://localhost:9245`）
2. 开发模式：检查 Desktop 应用控制台日志，确认是否接收到 `FRONTEND_DEVSERVER_URL` 环境变量
3. 开发模式：打开 DevTools（F12）查看是否有网络请求错误
4. 生产模式：确保 `apps/desktop/frontend/dist/` 目录存在且包含 `index.html`

### Q: 如何查看 Desktop 应用的日志？

**A:** 
- 开发模式：日志会输出到启动脚本的控制台
- 生产模式：去掉 `-ldflags "-H windowsgui"` 重新构建，可以看到控制台窗口和日志

### Q: 如何修改 Vite 开发服务器端口？

**A:** 传递端口参数：
```powershell
# Windows
powershell -File scripts/dev-desktop.ps1 -VitePort 3000

# Linux/macOS
bash scripts/dev-desktop.sh 3000
```

### Q: Desktop 应用需要单独启动 Server 吗？

**A:** **不需要！** Desktop 应用启动时会自动在内部启动 Loadout Server。这是通过 `core/servercore` 包实现的，Desktop 和 Server 共享同一套核心逻辑。

### Q: 开发模式下前端修改没有热重载？

**A:** 检查：
1. Vite 是否正常运行（应该显示 `http://localhost:9245`）
2. 浏览器控制台是否有 WebSocket 连接错误
3. 尝试手动刷新页面（Ctrl+R）

## 许可证

与 Loadout 项目相同。
