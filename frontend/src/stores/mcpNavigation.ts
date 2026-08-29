// 跨组件跳转信号：ProcessFooter 点击 MCP 进程 → McpPanel 切到 logs tab → McpLogsTab 选中对应 server。
// 走 store 而非 props/ref 是因为 ProcessFooter（侧边栏底部）和 McpPanel（主区）隔了多层组件与路由。
//
// 触发流程：
//   1. ProcessFooter 调用 gotoServerLogs(name)，写入 pendingServer 并自增 requestId。
//   2. McpPanel 监听 (pendingServer, requestId) → 切换 activeTab 到 'logs'。
//   3. McpLogsTab 监听 (pendingServer, requestId) → 调用 selectServer(pendingServer)。
//
// requestId 用来在重复点击同一 server 时也能触发响应（响应式 watcher 对相同值不会触发）。
import { ref } from 'vue'
import { defineStore } from 'pinia'

export const useMcpNavStore = defineStore('mcp-navigation', () => {
  /** 要选中的 MCP server name（对应后端日志 API 里的 server 名，不含 "MCP: " 前缀）。 */
  const pendingServer = ref<string | null>(null)
  /** 自增序号：保证即便 server name 重复也会让监听方触发一次新请求。 */
  const requestId = ref(0)

  /** 请求跳转到日志面板并选中指定 server。 */
  function gotoServerLogs(serverName: string) {
    pendingServer.value = serverName
    requestId.value += 1
  }

  /** 消费一次跳转请求：McpLogsTab 调用 selectServer 后清空，防止重复触发。 */
  function consume() {
    pendingServer.value = null
  }

  return { pendingServer, requestId, gotoServerLogs, consume }
})
