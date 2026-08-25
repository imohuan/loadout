// Loadout 后端「暴露给外部客户端 / 落盘」的绝对地址基址。
//
// 桌面壳(WebView)的 origin 是框架注入的伪 host(http://wails.localhost)，只在壳内可解析，
// 复制到外部 / 写进 mcp.json 就失效；且端口可由 --port / LOADOUT_SERVER_ADDR 指定。
// 因此前端不写死，而是启动时向后端拉一次真实地址（/api/desktop-base 返回
// http://127.0.0.1:<port>），缓存后复用。拉取失败则回退到 location.origin
//（纯 server 网页模式下 origin 本身就是真实可达地址）。

let cachedBase: string | null = null
let inflight: Promise<string> | null = null

export async function getLoadoutBase(): Promise<string> {
  if (cachedBase) return cachedBase
  if (!inflight) {
    inflight = fetch('/api/desktop-base')
      .then((r) => (r.ok ? r.json() : Promise.reject()))
      .then((d: { base_url?: string }) => {
        cachedBase = d.base_url || window.location.origin
        return cachedBase
      })
      .catch(() => {
        inflight = null
        return window.location.origin
      })
  }
  return inflight
}

// 同步读取已缓存的 base（预热后才有真实值，否则回退 origin）。供模板等同步上下文使用。
export function getLoadoutBaseSync(): string {
  return cachedBase ?? window.location.origin
}

// 同步兜底常量：优先用已缓存值，否则回退 origin。
export const LOADOUT_BASE: string = window.location.origin
