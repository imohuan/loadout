// Package config 从 wails.json 读取应用配置，提供全局变量 App 供其他包使用。
// 所有自定义配置放在 wails.json 的 custom 字段下，避免与 Wails 框架字段冲突。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppConfig 应用顶层配置，对应 wails.json 根结构。
type AppConfig struct {
	Name        string       `json:"name"`        // 应用名称，用于窗口标题、数据目录
	Description string       `json:"description"` // 应用描述
	Custom      CustomConfig `json:"custom"`      // 自定义配置（窗口、服务、图标）
}

// CustomConfig 所有自定义配置，放在 wails.json 的 custom 字段下。
type CustomConfig struct {
	Window WindowConfig `json:"window"` // 窗口相关配置
	Server ServerConfig `json:"server"` // HTTP 服务相关配置
	Icons  IconsConfig  `json:"icons"`  // 图标路径配置
}

// WindowConfig 窗口配置。
type WindowConfig struct {
	Title     string `json:"title"`     // 窗口标题
	Width     int    `json:"width"`     // 窗口宽度（px）
	Height    int    `json:"height"`    // 窗口高度（px）
	MinWidth  int    `json:"minWidth"`  // 最小宽度（px）
	MinHeight int    `json:"minHeight"` // 最小高度（px）
	Frameless bool   `json:"frameless"` // 无边框模式（需配合 index.html 自绘标题栏）
}

// ServerConfig 自定义 HTTP 服务配置。
type ServerConfig struct {
	Port int `json:"port"` // 监听端口，默认 8866
}

// IconsConfig 各位置的图标文件路径。
type IconsConfig struct {
	Exe      string `json:"exe"`      // exe 文件图标（rsrc 生成 .syso 的源文件）
	Taskbar  string `json:"taskbar"`  // 任务栏和 Alt+Tab 图标
	Titlebar string `json:"titlebar"` // 前端标题栏图标
}

// App 全局配置实例，init() 自动从 wails.json 加载。
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

// setDefaults 设置默认配置（wails.json 不存在或读取失败时使用）。
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

// DataDir 返回应用数据目录（%APPDATA%/<AppName>），不存在则自动创建。
func DataDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(d, App.Name)
	return dir, os.MkdirAll(dir, 0755)
}
