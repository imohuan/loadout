# Wails v3 项目开发模板（面向 AI 助手）

> 本文档是项目的**精确源码参考**，同时包含架构说明和常见坑规避。所有代码块直接取自实际源文件。

---

## 一、架构总览

```
┌─────────────────────────────────────────────┐
│            app.exe  (单文件二进制)              │
│                                               │
│  ┌─────────────────────────────────────────┐ │
│  │ UI 层：WebView2 渲染 Vue 前端             │ │
│  │   Vite 构建 → go:embed 进 exe             │ │
│  └───────────────┬───────────────┬──────────┘ │
│                  │ ① Wails IPC   │ ② 本地HTTP │
│                  │ (@wailsio/    │ (fetch)    │
│                  │  runtime)     │            │
│  ┌───────────────┴───────────────┴──────────┐ │
│  │ 后端层（Go）：                              │ │
│  │  • 窗口控制：Window.Minimise/Close         │ │
│  │  • 自定义 HTTP 服务：/__api/*              │ │
│  │  • 配置持久化：%APPDATA%/<AppName>/        │ │
│  └──────────────────────────────────────────┘ │
└──────────────────────────────────────────────┘
```

**两套通信机制**：

```
WebView2 (host: wails.localhost)
  │
  ├─ ① 窗口控制
  │   @wailsio/runtime → WebView2 原生 IPC → Wails Go 进程
  │   非 HTTP，WebView2 内部拦截 /wails/runtime
  │   文件: frontend/src/composables/useWindow.js
  │
  └─ ② 业务 API
      fetch(http://localhost:{port}/__api/*) → 自定义 HTTP 服务器
      标准 HTTP fetch + JSON
      文件: frontend/src/composables/useApi.js
```

**为什么用绝对地址？** WebView host 是 `wails.localhost`，相对路径 `/__api/status` 会发到 `wails.localhost` 而非业务端口。绝对地址确保 API 打到自定义 HTTP 服务器。

---

## 二、文件清单

```
main.go                              # 入口：embed frontend/dist → app.Run()
wails.json                           # ★ 所有自定义配置集中地
go.mod / go.sum
.gitignore

backend/
  config.go                          # 读取 wails.json，提供 config.App 全局变量
  app/
    runner.go                        # Wails 生命周期：创建窗口 + 启动服务
    seticon_windows.go               # Windows 任务栏/Alt+Tab 图标（WM_SETICON）
  server/
    handler.go                       # HTTP 服务器 + REST API
    config.go                        # 运行时配置持久化（%APPDATA%/<AppName>/config.json）
  singleton/
    mutex.go                         # 单实例控制

icons/
  appicon.ico                        # exe + 任务栏图标源文件
  appicon-*.png                      # 各尺寸 PNG
  icons.go                           # embed 备用（当前未使用，图标从磁盘读取）

frontend/
  package.json
  vite.config.js
  index.html                         # 硬编码标题栏 + <div id="app">
  public/
    appicon-16x16.png                # 标题栏图标
  src/
    main.js                          # Vue 入口
    App.vue                          # 根组件
    style.css
    composables/
      useApi.js                      # fetch → HTTP 服务器
      useWindow.js                   # @wailsio/runtime → 窗口控制
  dist/                              # Vite 构建产物（Go embed 读取）

scripts/
  dev.ps1                            # 开发模式（热重载）
  pack.ps1                           # 生产打包

dist/                                # 构建输出（.gitignore）
```

---

## 三、配置系统

### 3.1 wails.json

所有自定义配置放在 `custom` 下，避免与 Wails 框架字段冲突：

```json
{
  "name": "myapp",
  "description": "Wails v3 desktop app template",
  "author": {},
  "custom": {
    "window": {
      "title": "MyApp",
      "width": 1100,
      "height": 720,
      "minWidth": 900,
      "minHeight": 600,
      "frameless": true
    },
    "server": {
      "port": 8866
    },
    "icons": {
      "exe": "icons/appicon.ico",
      "taskbar": "icons/appicon.ico",
      "titlebar": "frontend/public/appicon-16x16.png"
    }
  }
}
```

| 字段 | 说明 |
|------|------|
| `name` | 应用名，用于窗口标题、数据目录（`%APPDATA%/<name>/`） |
| `custom.window.title` | 窗口标题 |
| `custom.window.width/height` | 窗口默认尺寸 |
| `custom.window.minWidth/minHeight` | 窗口最小尺寸 |
| `custom.window.frameless` | 无边框模式（需配合 index.html 自绘标题栏） |
| `custom.server.port` | 自定义 HTTP 服务器端口 |
| `custom.icons.exe` | exe 图标（rsrc 生成 .syso 的源文件） |
| `custom.icons.taskbar` | 任务栏/Alt+Tab 图标 |
| `custom.icons.titlebar` | 标题栏图标 |

### 3.2 backend/config.go — 配置加载器

```go
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type AppConfig struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Custom      CustomConfig `json:"custom"`
}

type CustomConfig struct {
	Window WindowConfig `json:"window"`
	Server ServerConfig `json:"server"`
	Icons  IconsConfig  `json:"icons"`
}

type WindowConfig struct {
	Title     string `json:"title"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MinWidth  int    `json:"minWidth"`
	MinHeight int    `json:"minHeight"`
	Frameless bool   `json:"frameless"`
}

type ServerConfig struct {
	Port int `json:"port"`
}

type IconsConfig struct {
	Exe      string `json:"exe"`
	Taskbar  string `json:"taskbar"`
	Titlebar string `json:"titlebar"`
}

var App AppConfig

func init() {
	data, err := os.ReadFile("wails.json")
	if err != nil {
		setDefaults()
		return
	}
	json.Unmarshal(data, &App)
	if App.Custom.Window.Width == 0 {
		App.Custom.Window.Width = 1100
	}
	if App.Custom.Window.Height == 0 {
		App.Custom.Window.Height = 720
	}
	if App.Custom.Window.Title == "" {
		App.Custom.Window.Title = App.Name
	}
	if App.Custom.Server.Port == 0 {
		App.Custom.Server.Port = 8866
	}
}

func setDefaults() {
	App = AppConfig{
		Name:        "MyApp",
		Description: "Wails v3 Desktop App",
		Custom: CustomConfig{
			Window: WindowConfig{
				Title: "MyApp", Width: 1100, Height: 720,
				MinWidth: 900, MinHeight: 600, Frameless: true,
			},
			Server: ServerConfig{Port: 8866},
			Icons: IconsConfig{
				Exe:      "icons/appicon.ico",
				Taskbar:  "icons/appicon.ico",
				Titlebar: "frontend/public/appicon-16x16.png",
			},
		},
	}
}

func DataDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(d, App.Name)
	return dir, os.MkdirAll(dir, 0755)
}
```

**用法**：通过 `config.App.Custom.Window.Title`、`config.App.Custom.Server.Port` 等访问。

### 3.3 运行时配置持久化

`backend/server/config.go` — 用户运行时修改的配置保存到 `%APPDATA%/<AppName>/config.json`：

```go
package server

import (
	"encoding/json"
	"os"
	"path/filepath"

	"proxyui/backend"
)

type Config struct {
	Port int `json:"port"`
}

func DefaultConfig() Config {
	return Config{Port: config.App.Custom.Server.Port}
}

func configPath() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func LoadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return DefaultConfig(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultConfig(), nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), err
	}
	if cfg.Port == 0 {
		cfg.Port = config.App.Custom.Server.Port
	}
	return cfg, nil
}

func SaveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, data, 0644)
}
```

---

## 四、入口与启动

### 4.1 main.go

```go
package main

import (
	"embed"
	"proxyui/backend/app"
)

//go:embed frontend/dist
var assets embed.FS

func main() {
	app.Run(assets)
}
```

### 4.2 go.mod

```
module proxyui

go 1.26.3

require github.com/wailsapp/wails/v3 v3.0.0-alpha2.119

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
```

### 4.3 runner.go

```go
package app

import (
	"embed"
	"os"
	"runtime"

	"proxyui/backend"
	"proxyui/backend/server"
	"proxyui/backend/singleton"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func Run(assets embed.FS) {
	singleton.KillExistingInstance()

	svc := server.New()

	appIcon, _ := os.ReadFile(config.App.Custom.Icons.Taskbar)

	app := application.New(application.Options{
		Name:        config.App.Name,
		Description: config.App.Description,
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	svc.SetApp(app)
	svc.Start()

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     config.App.Custom.Window.Title,
		Width:     config.App.Custom.Window.Width,
		Height:    config.App.Custom.Window.Height,
		MinWidth:  config.App.Custom.Window.MinWidth,
		MinHeight: config.App.Custom.Window.MinHeight,
		URL:       "/",
		Frameless: config.App.Custom.Window.Frameless,
	})
	win.Show()

	win.OpenDevTools()

	if runtime.GOOS == "windows" {
		wndClass := config.App.Name + "Wnd"
		setWindowIconAfterCreate(wndClass, config.App.Custom.Window.Title, appIcon)
	}

	app.Run()
}
```

---

## 五、后端：HTTP 服务器

`backend/server/handler.go`：

```go
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

type Service struct {
	cfg     Config
	server  *http.Server
	running bool
	app     *wailsapp.App
}

func New() *Service { return &Service{} }

func (s *Service) SetApp(app *wailsapp.App) { s.app = app }

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS,PATCH")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if r.Method == "OPTIONS" { return }

	if strings.HasPrefix(r.URL.Path, "/__api/") {
		s.serveAPI(w, r)
		return
	}
	writeJSON(w, 404, map[string]string{"error": "not found"})
}

func (s *Service) serveAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/__api")
	switch {
	case path == "/status" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"port": s.cfg.Port, "running": s.running})
	case path == "/config" && r.Method == "GET":
		cfg, _ := LoadConfig()
		writeJSON(w, 200, cfg)
	case path == "/config" && r.Method == "POST":
		var cfg Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		SaveConfig(cfg)
		s.restartServer()
		writeJSON(w, 200, map[string]string{"ok": "saved"})
	case path == "/window/min" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil { w.Minimise() }
		writeJSON(w, 200, map[string]string{"ok": "minimised"})
	case path == "/window/max" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil { w.ToggleMaximise() }
		writeJSON(w, 200, map[string]string{"ok": "toggled"})
	case path == "/window/close" && r.Method == "GET":
		if w := s.app.Window.Current(); w != nil { w.Close() }
		writeJSON(w, 200, map[string]string{"ok": "closed"})
	default:
		writeJSON(w, 404, map[string]string{"error": "unknown api"})
	}
}

func (s *Service) Start() {
	if s.server != nil { s.running = true; return }
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

func (s *Service) Stop() { s.running = false }

func (s *Service) restartServer() {
	s.running = false
	if s.server != nil { s.server.Close(); s.server = nil }
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
```

**API 端点**：

| 路径 | 方法 | 功能 |
|------|------|------|
| `/__api/status` | GET | `{port, running}` |
| `/__api/config` | GET | 运行时配置 |
| `/__api/config` | POST | 保存配置并重启 |
| `/__api/window/min` | GET | 最小化 |
| `/__api/window/max` | GET | 最大化/还原 |
| `/__api/window/close` | GET | 关闭 |

---

## 六、前端

### 6.1 窗口控制

`frontend/src/composables/useWindow.js`：

```js
import { Window } from '@wailsio/runtime'

export function setupWindowControls() {
  document.getElementById('btn-min')?.addEventListener('click', () => Window.Minimise())
  document.getElementById('btn-max')?.addEventListener('click', async () => {
    const isMax = await Window.IsMaximised()
    if (isMax) await Window.Restore()
    else await Window.Maximise()
  })
  document.getElementById('btn-close')?.addEventListener('click', () => Window.Close())
}

export function updateTitlebarPort(port) {
  const el = document.getElementById('titlebar-port')
  if (el) el.textContent = 'localhost:' + port
}
```

### 6.2 业务 API

`frontend/src/composables/useApi.js`：

```js
import { ref, computed } from 'vue'

export function useApi(apiPort) {
  const apiBase = computed(() => 'http://localhost:' + apiPort.value)

  async function api(path, opts) {
    try {
      const res = await fetch(apiBase.value + '/__api' + path, opts)
      return await res.json()
    } catch (e) {
      console.warn('API error:', path, e)
      return null
    }
  }

  return { api, apiBase }
}
```

### 6.3 App.vue

```vue
<script setup>
import { ref, reactive, onMounted, onUnmounted } from 'vue'
import { useApi } from './composables/useApi.js'
import { updateTitlebarPort } from './composables/useWindow.js'

const savedPort = parseInt(localStorage.getItem('myapp_port') || '8866')
const apiPort = ref(savedPort)
const { api } = useApi(apiPort)
const running = ref(false)
const showSettings = ref(false)
const form = reactive({ port: savedPort })

async function refreshStatus() {
  const s = await api('/status')
  if (s !== null) {
    running.value = s.running
    if (s.port) apiPort.value = s.port
  }
  updateTitlebarPort(apiPort.value)
}

async function openSettings() {
  showSettings.value = true
  const c = await api('/config')
  if (c) form.port = c.port || 8866
}

async function saveSettings() {
  await api('/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ port: form.port })
  })
  localStorage.setItem('myapp_port', form.port)
  apiPort.value = form.port
  showSettings.value = false
  setTimeout(refreshStatus, 1500)
}

let interval = null
onMounted(() => {
  updateTitlebarPort(apiPort.value)
  refreshStatus()
  interval = setInterval(refreshStatus, 5000)
})
onUnmounted(() => clearInterval(interval))
</script>

<template>
  <div style="height:calc(100vh - 32px);display:flex;align-items:center;justify-content:center;flex-direction:column;gap:16px;">
    <div style="font-size:24px;font-weight:600;">Wails v3 App Template</div>
    <div style="color:#666;">
      服务状态: <span :style="{color:running?'#16a34a':'#dc2626',fontWeight:600}">{{ running ? '运行中' : '已停止' }}</span>
      端口: {{ apiPort }}
    </div>
    <button @click="openSettings" style="padding:8px 16px;cursor:pointer;">打开设置</button>

    <div v-if="showSettings" style="position:fixed;inset:0;background:rgba(0,0,0,0.3);display:flex;align-items:center;justify-content:center;z-index:50;" @click.self="showSettings=false">
      <div style="background:#fff;padding:24px;border-radius:8px;min-width:300px;">
        <h3 style="margin:0 0 16px;">设置</h3>
        <label style="display:block;margin-bottom:8px;">端口: <input v-model.number="form.port" type="number" min="1" max="65535" style="width:80px;" /></label>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px;">
          <button @click="showSettings=false" style="padding:6px 16px;cursor:pointer;">取消</button>
          <button @click="saveSettings" style="padding:6px 16px;cursor:pointer;background:#000;color:#fff;border:none;border-radius:2px;">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>
```

### 6.4 index.html

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width,initial-scale=1.0" />
<title>MyApp</title>
</head>
<body style="margin:0;font-family:system-ui,-apple-system,sans-serif;">

<div id="titlebar">
  <span style="font-size:13px;font-weight:600;display:flex;align-items:center;gap:8px;">
    <img src="/appicon-16x16.png" width="16" height="16" style="flex-shrink:0" alt="" />
    MyApp
    <span style="font-weight:400;color:#888;font-size:11px;margin-left:4px;" id="titlebar-port">localhost:8866</span>
  </span>
  <span style="flex:1"></span>
  <button id="btn-min"  class="tb-btn">─</button>
  <button id="btn-max"  class="tb-btn">□</button>
  <button id="btn-close" class="tb-btn">✕</button>
</div>

<div id="app"></div>

<script type="module" src="/src/main.js"></script>

<style>
  #titlebar { --wails-draggable: drag; height:32px; background:#f5f5f5; border-bottom:1px solid #ddd; display:flex; align-items:center; padding:0 0 0 12px; user-select:none; flex-shrink:0; }
  .tb-btn { --wails-draggable: no-drag; width:46px; height:100%; border:none; background:transparent; color:#333; cursor:pointer; display:flex; align-items:center; justify-content:center; font-size:12px; }
  .tb-btn:hover { background:#e0e0e0; }
  #btn-close:hover { background:#e81123 !important; color:white; }
</style>
</body>
</html>
```

### 6.5 main.js

```js
import { createApp } from 'vue'
import App from './App.vue'
import { setupWindowControls } from './composables/useWindow.js'
import '@wailsio/runtime'

import './style.css'

setupWindowControls()
createApp(App).mount('#app')
```

### 6.6 vite.config.js

```js
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  root: '.',
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  resolve: {
    alias: {
      vue: 'vue/dist/vue.esm-bundler.js',
    },
  },
})
```

### 6.7 package.json

```json
{
  "name": "myapp-frontend",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite --port 9245",
    "build": "vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "@wailsio/runtime": "^3.0.0-alpha.2",
    "vue": "^3.5.40"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": "^6.0.8",
    "vite": "^6.0.0"
  }
}
```

### 6.8 style.css

```css
body { margin: 0; overflow: hidden; }
```

---

## 七、辅助模块

### 7.1 Windows 任务栏图标

`backend/app/seticon_windows.go`：

```go
//go:build windows

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procFindWindowW  = user32.NewProc("FindWindowW")
	procSendMessageW = user32.NewProc("SendMessageW")
	procLoadImageW   = user32.NewProc("LoadImageW")
)

const (
	WM_SETICON      = 0x0080
	ICON_SMALL      = 0
	ICON_BIG        = 1
	IMAGE_ICON      = 1
	LR_LOADFROMFILE = 0x00000010
)

func setWindowIconAfterCreate(className, windowTitle string, icoBytes []byte) {
	go func() {
		tmpDir := os.TempDir()
		icoPath := filepath.Join(tmpDir, "app_icon.ico")
		if err := os.WriteFile(icoPath, icoBytes, 0644); err != nil {
			return
		}
		defer os.Remove(icoPath)

		cls, _ := syscall.UTF16PtrFromString(className)
		var hwnd uintptr
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			hwnd, _, _ = procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
			if hwnd != 0 { break }
		}
		if hwnd == 0 { return }

		path, _ := syscall.UTF16PtrFromString(icoPath)
		hIcon, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(path)), IMAGE_ICON, 0, 0, LR_LOADFROMFILE)
		if hIcon != 0 {
			procSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, hIcon)
			procSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, hIcon)
		}
	}()
}
```

**坑**：`application.Options.Icon` 在 Windows 上只影响 system tray，不影响任务栏/Alt+Tab，必须用 `WM_SETICON`。

### 7.2 单实例控制

`backend/singleton/mutex.go`：

```go
package singleton

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func KillExistingInstance() {
	currentExe, _ := os.Executable()
	currentName := strings.ToLower(fileName(currentExe))

	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil { return }

	pid := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 { continue }
		name := strings.Trim(parts[0], `"`)
		if !strings.EqualFold(name, currentName) { continue }
		pidStr := strings.Trim(parts[1], `"`)
		var otherPid int
		if _, err := fmt.Sscanf(pidStr, "%d", &otherPid); err != nil { continue }
		if otherPid == pid { continue }
		proc, _ := os.FindProcess(otherPid)
		if proc != nil { proc.Kill(); proc.Wait() }
	}
}

func fileName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	i := strings.LastIndex(path, "/")
	if i >= 0 { return path[i+1:] }
	return path
}
```

### 7.3 .gitignore

```
# Binaries
/dist/
*.exe
*.exe~
*.dll
*.so
*.dylib

# Go
vendor/
go.sum

# Wails
wailsjs/

# Frontend
frontend/dist/
frontend/node_modules/

# IDE
.idea/
.vscode/
*.swp
*.swo
*~

# OS
.DS_Store
Thumbs.db
desktop.ini

# Build artifacts
build/bin/
*.nsis

# Env
.env
.env.local

# Test
coverage.out

.cursor
.codegraph
backend/app/appicon_windows_amd64.syso
```

---

## 八、图标配置

图标在三个层面，路径均通过 `wails.json` 的 `custom.icons` 配置：

| 位置 | 配置字段 | 方式 |
|------|----------|------|
| exe 文件 | `custom.icons.exe` | `rsrc` 读取此路径生成 `.syso` |
| 任务栏/Alt+Tab | `custom.icons.taskbar` | `runner.go` 读取后传给 `WM_SETICON` |
| 标题栏 | `custom.icons.titlebar` | `index.html` 中 `<img src>` 引用 |

**更换图标**：替换 `icons/` 下的文件，更新 `wails.json` 中的路径即可。

**坑**：
- `application.Options.Icon` 在 Windows 上只影响 system tray
- `.syso` 必须和 Go 源文件同目录（`backend/app/`）
- `LoadImageW` 的 `cx`/`cy` 参数传 `0` 表示加载实际尺寸，不要传指针

---

## 九、构建

### 开发模式

```powershell
.\scripts\dev.ps1
```

流程：npm install → vite build → 启动 Vite → 设置 `FRONTEND_DEVSERVER_URL` → go build → 启动

`scripts/dev.ps1`：

```powershell
# 开发模式 — Vite 热重载 + Go 后端
param([int]$VitePort = 9245)

$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot\..

$viteProc = $null
$goProc   = $null

function Cleanup {
    if ($goProc -and !$goProc.HasExited)   { Stop-Process -Id $goProc.Id -Force -ErrorAction SilentlyContinue }
    if ($viteProc -and !$viteProc.HasExited) { Stop-Process -Id $viteProc.Id -Force -ErrorAction SilentlyContinue }
    Pop-Location
}

try { [Console]::TreatControlCAsInput = $false } catch {}
$null = Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action { if (Get-Command Cleanup -ea 0) { Cleanup } }

Push-Location frontend
if (-not (Test-Path "node_modules/vue")) { npm install 2>&1 | Out-Null }
npx vite build 2>&1 | Out-Null
Pop-Location

$viteProc = Start-Process -FilePath "cmd" -ArgumentList "/c","npx vite --port $VitePort --strictPort" -WorkingDirectory "frontend" -PassThru -NoNewWindow
Start-Sleep -Seconds 3

$config = Get-Content wails.json | ConvertFrom-Json
$sysoOut = "backend/app/appicon_windows_amd64.syso"
if (-not (Test-Path $sysoOut)) {
    & rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 2>&1 | Out-Null
}

$env:CGO_ENABLED = "0"
$env:FRONTEND_DEVSERVER_URL = "http://localhost:$VitePort"

go build -ldflags "-H windowsgui" -o dist/app-dev.exe .
if ($LASTEXITCODE -ne 0) { Write-Host "Go build FAIL" -ForegroundColor Red; Cleanup; exit 1 }

$goProc = Start-Process -FilePath "dist/app-dev.exe" -PassThru

try {
    while (-not $goProc.HasExited) { Start-Sleep -Seconds 1 }
} finally { Cleanup }
```

### 生产打包

```powershell
.\scripts\pack.ps1
```

流程：npm run build → rsrc 生成 .syso → go build -tags production

`scripts/pack.ps1`：

```powershell
# 打包脚本 — 构建前端(Vite) → 生成图标资源 → 构建 Go
$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot\..

$config = Get-Content wails.json | ConvertFrom-Json

Push-Location frontend
npm run build 2>&1 | Out-Null
Pop-Location

$sysoOut = "backend/app/appicon_windows_amd64.syso"
& rsrc -ico $config.custom.icons.exe -o $sysoOut -arch amd64 | Out-Null

$env:CGO_ENABLED = "0"
go build -tags production -ldflags "-w -s -H windowsgui" -o dist/app.exe .
```

输出：`dist/app.exe`

---

## 十、硬性约束

1. **业务 HTTP 服务器不能注册为 Wails Service** — 会拦截 `/wails/runtime`，导致 Window API 失效
2. **标题栏硬编码在 index.html** — 不能放 Vue SFC 中，Wails drag 在 Vue mount 前就需要标题栏 DOM
3. **WebView URL 始终 `"/"`** — 用 `FRONTEND_DEVSERVER_URL` 环境变量启用热重载，不要直连 `localhost:9245`
4. **业务 API 用绝对 URL** — `fetch('http://localhost:' + port + '/__api/...')`，因为 WebView host 是 `wails.localhost`
5. **Go embed 路径 `frontend/dist`** — 对应 Vite `outDir: 'dist'`
6. **`.syso` 必须和 `.go` 同目录** — `backend/app/`
7. **不需要 `fs.Sub`** — Wails `AssetFileServerFS` 自动处理 `frontend/dist` 前缀
8. **vite.config.js 不需要 `server.proxy`** — Wails 自动代理到 Vite
9. **所有自定义配置放 `wails.json` 的 `custom` 下** — 避免与 Wails 框架字段冲突
10. **`@wailsio/runtime` 只在 WebView2 内部有效** — 普通浏览器中无效

---

## 十一、常见坑

1. **Wails v3 仍是 alpha** — API 可能变动，锁版本
2. **改了 Go 方法前端不更新** — 如果用 Wails Service 绑定模式，改 Go 暴露的方法后必须重跑 `wails3 generate bindings`（本项目用 HTTP API 模式，不受此限制）
3. **WebView2 缺失** — 用户机器没装 Edge/WebView2 会白屏，可用 Wails bootstrapper 内嵌自动安装
4. **静态编译 `CGO_ENABLED=0`** — 用纯 Go 依赖没问题；若引入需要 CGO 的库，交叉编译会失败，换纯 Go 实现
5. **CSS `--wails-draggable`** — 不是 `-webkit-app-region`（那是 Electron 的）

---

## 十二、添加新功能

### 添加自定义配置

`wails.json` 的 `custom` 下添加字段 → `backend/config.go` 的 `CustomConfig` 添加对应 struct → 代码通过 `config.App.Custom.XXX` 访问。

### 添加 API 端点

`backend/server/handler.go` 的 `serveAPI()` switch 中添加 case → 前端 `api('/新路径', { method: '...' })` 调用。

### 添加前端组件

`frontend/src/components/` 创建 `.vue` 文件 → `<script setup>` + Composition API → 父组件 import 使用。

### 修改窗口

修改 `wails.json` 中 `custom.window` 字段即可。

### 调试

- `runner.go` 中 `win.OpenDevTools()` 自动打开 DevTools
- 配置路径：`%APPDATA%/<AppName>/config.json`
