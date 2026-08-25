// Package app 提供链接跳转守卫：限制 WebView 中点击链接的跳转方式。
//
// 背景：Wails v3（alpha2.119）在 Windows 上没有暴露「导航拦截」的公开 API
// （GitHub issue #5799 尚未实现）。WebView2 的 NewWindowRequested / 带 URI 的
// NavigationStarting 事件在 edge 封装里没有对外暴露，因此只能在 Go 侧注入
// 一段 JS 守卫脚本来实现：
//   - 应用内链接（wails.localhost / 以 / 开头的站内路径）→ 放行，在窗口内打开；
//   - 外部 http/https 链接（含 target=_blank / window.open）→ 拦截，转交给
//     系统默认浏览器打开，避免 WebView 窗口被外部页面替换或弹出新窗口。
//
// 守卫脚本通过 WebViewNavigationCompleted 事件在每次导航完成后注入（幂等），
// 覆盖刷新 / Ctrl+R / 站内跳转后的新页面。
package app

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// linkGuardJS 是注入到 WebView 的守卫脚本。
// 幂等：window.__loadoutLinkGuard 存在则不再重复注册，避免多次导航后叠加监听器。
const linkGuardJS = `
(() => {
  if (window.__loadoutLinkGuard) return;
  window.__loadoutLinkGuard = true;

  // 判断 URL 是否为应用内部地址：
  //  - 生产环境页面跑在 wails.localhost 上；
  //  - 开发模式页面跑在 http://localhost:9245 上；
  //  - 站内相对路径（/xxx）也算内部。
  const isInternal = (url) => {
    if (!url) return true;
    if (url.startsWith('#')) return true;
    if (url.startsWith('/')) return true;
    try {
      const u = new URL(url, window.location.href);
      const host = u.hostname;
      return host === 'wails.localhost' || host === 'localhost' || host === '127.0.0.1';
    } catch { return false; }
  };

  // 拦截 target="_blank" 的链接：站内 → 当前窗口导航；站外 → 系统浏览器。
  document.addEventListener('click', (e) => {
    const a = e.target && e.target.closest ? e.target.closest('a') : null;
    if (!a || !a.href) return;
    const href = a.getAttribute('href') || '';
    if (a.target === '_blank' || a.target === '_new' || (a.rel || '').includes('external')) {
      e.preventDefault();
      if (isInternal(href)) {
        window.location.href = href;
      } else {
        try { window.open(href, '_self'); } catch (err) {}
      }
    }
  }, true);

  // 拦截 window.open 调用（AxImageViewer 下载图片失败时会走这里）。
  const nativeOpen = window.open;
  window.open = function(url, name, features) {
    try {
      if (typeof url === 'string' && isInternal(url)) {
        window.location.href = url;
        return null;
      }
    } catch (err) {}
    // 外部链接：仍走系统默认浏览器（Wails/WebView2 默认行为）
    return nativeOpen.apply(this, arguments);
  };
})();
`

// injectLinkGuardScript 注册导航完成事件，每次页面加载完成后注入链接守卫脚本。
// 必须在窗口 Show 之前调用；事件在窗口创建后即生效。
func injectLinkGuardScript(win application.Window) {
	win.RegisterHook(events.Windows.WebViewNavigationCompleted, func(_ *application.WindowEvent) {
		win.ExecJS(linkGuardJS)
	})
	log.Println("链接守卫已启用：应用内链接窗口内打开，外部链接转系统浏览器")
}
