// Package app 提供系统托盘能力：窗口关闭时驻留托盘、托盘菜单恢复窗口/打开网页/退出。
package app

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"loadout/core/auth"
	lconfig "loadout/core/config"
	"loadout/core/store"
	config "proxyui/backend"
	"proxyui/icons"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// webURL 托盘「打开网页」指向的地址：桌面内嵌的 Loadout Web 版。
// 后端服务默认监听 127.0.0.1:3000（LOADOUT_SERVER_ADDR 可改，暂未联动）。
const webURL = "http://127.0.0.1:3000"

// ssoTokenTTL SSO 短效 token 有效期：只作为浏览器换取会话 Cookie 的「入场券」，
// 够 30 秒打开页面即可，过期即失效。
const ssoTokenTTL = 30 * time.Second

// ssoWebURL 生成网页地址。
// 仅当桌面端 WebView 已登录（后端在登录时写入 desktop-session.json 标记）才签发
// 短效 JWT 拼到 ?sso=，让浏览器自动登录；桌面未登录则返回不带 token 的地址，
// 网页保持自身的登录态（浏览器已登录则登录，否则显示登录页）。
func ssoWebURL() string {
	sessionPath := filepath.Join(lconfig.DataDir, lconfig.DesktopSessionFile)
	username := ""
	if data, err := os.ReadFile(sessionPath); err == nil {
		var sess struct {
			Username string `json:"username"`
		}
		if json.Unmarshal(data, &sess) == nil {
			username = sess.Username
		}
	}
	if username == "" {
		// 软件中未登录 → 打开未登录的网页版
		return webURL
	}

	st, err := store.New(lconfig.DataDir)
	if err != nil {
		log.Printf("打开网页: 读取配置失败，回退无 token 地址: %v", err)
		return webURL
	}
	token, err := auth.SignToken(st.SecretKey(), username, ssoTokenTTL)
	if err != nil {
		log.Printf("打开网页: 签发免登录 token 失败，回退无 token 地址: %v", err)
		return webURL
	}
	return webURL + "/?sso=" + token
}

// setupTray 创建系统托盘并绑定菜单与点击行为。
// win 是主窗口引用：托盘左键单击恢复窗口，菜单项可控制窗口显隐。
func setupTray(app *application.App, win application.Window) {
	tray := app.SystemTray.New()
	tray.SetIcon(icons.AppIcon)
	tray.SetTooltip(config.App.Name)

	// 左键单击托盘图标 → 显示并聚焦主窗口（等价于「打开软件」）
	tray.OnClick(func() {
		win.Show().Focus()
	})

	menu := application.NewMenu()
	// 恢复桌面版应用
	menu.Add("打开软件").OnClick(func(*application.Context) {
		win.Show().Focus()
	})
	// 打开网页版（默认浏览器访问本机 Loadout Web 服务，自动免登录）
	menu.Add("打开网页").OnClick(func(*application.Context) {
		if err := app.Browser.OpenURL(ssoWebURL()); err != nil {
			log.Printf("打开网页失败: %v", err)
		}
	})
	menu.AddSeparator()
	// 真正退出：直接销毁应用，不经过窗口关闭拦截
	menu.Add("退出").OnClick(func(*application.Context) {
		app.Quit()
	})

	tray.SetMenu(menu)
}
