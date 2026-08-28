import { createRouter, createWebHistory } from 'vue-router'
import DashboardLayout from '@/layout/DashboardLayout.vue'
const LoginView = () => import('@/views/LoginView.vue')
const OverviewView = () => import('@/views/OverviewView.vue')
const ChannelsView = () => import('@/views/ChannelsView.vue')
const ModelTestView = () => import('@/views/ModelTestView.vue')
const AggregatesView = () => import('@/views/AggregatesView.vue')
const ModelStatusView = () => import('@/views/ModelStatusView.vue')
const RouteLogsView = () => import('@/views/RouteLogsView.vue')
const RequestLogDetailView = () => import('@/views/RequestLogDetailView.vue')
const McpView = () => import('@/views/McpView.vue')
const CapabilityRoutesView = () => import('@/views/CapabilityRoutesView.vue')
const ManagementView = () => import('@/views/ManagementView.vue')
const SkillsView = () => import('@/views/SkillsView.vue')
const TranslateView = () => import('@/views/TranslateView.vue')
const UnifyaiView = () => import('@/views/UnifyaiView.vue')
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginView, meta: { public: true, title: '登录' } },
    {
      path: '/',
      component: DashboardLayout,
      children: [
        { path: '', name: 'overview', component: OverviewView, meta: { title: '概览' } },
        {
          path: 'channels',
          name: 'channels',
          component: ChannelsView,
          meta: { title: '渠道与模型' },
        },
        {
          path: 'model-test',
          name: 'model-test',
          component: ModelTestView,
          meta: { title: '模型测试' },
        },
        {
          path: 'aggregates',
          name: 'aggregates',
          component: AggregatesView,
          meta: { title: '聚合模型' },
        },
        {
          path: 'model-status',
          name: 'model-status',
          component: ModelStatusView,
          meta: { title: '模型状态' },
        },
        {
          path: 'route-logs',
          name: 'route-logs',
          component: RouteLogsView,
          meta: { title: '转发日志' },
        },
        {
          path: 'request-logs/:id',
          name: 'request-log-detail',
          component: RequestLogDetailView,
          meta: { title: '完整请求日志' },
        },
        {
          path: 'integrations',
          name: 'integrations',
          component: McpView,
          meta: { title: 'MCP 管理' },
        },
        {
          path: 'capability-routes',
          name: 'capability-routes',
          component: CapabilityRoutesView,
          meta: { title: '能力路由' },
        },
        { path: 'settings', name: 'settings', component: ManagementView, meta: { title: '设置' } },
        { path: 'skills', name: 'skills', component: SkillsView, meta: { title: 'Skills' } },
        {
          path: 'translations',
          name: 'translations',
          component: TranslateView,
          meta: { title: '翻译' },
        },
        {
          path: 'unifyai',
          name: 'unifyai',
          component: UnifyaiView,
          meta: { title: 'UnifyAI 配置同步' },
        },
        { path: 'keys', redirect: { name: 'settings' } },
        { path: 'plugins', redirect: { name: 'settings' } },
      ],
    },
  ],
})

// 桌面端「打开网页」免登录 token：模块加载时（最早时机）从 URL 读取并缓存，
// 同时立刻把 ?sso= 从地址栏清掉。先读后清，保证清理不丢登录凭证。
// 之后 beforeEach 只用缓存的 token，不再依赖 URL。
let ssoToken: string | null = (() => {
  const t = new URLSearchParams(window.location.search).get('sso')
  if (t) {
    const url = new URL(window.location.href)
    url.searchParams.delete('sso')
    window.history.replaceState({}, '', url.pathname + url.search)
  }
  return t
})()
router.beforeEach(async (to) => {
  const auth = useAuthStore()
  // ssoLogin 只跑一次（页面加载时自动换票）：无论成败都置空 token，
  // 避免用户登出后被 beforeEach 反复重试触发「登出 → 自动登录」死循环。
  if (ssoToken && !auth.authenticated) {
    const token = ssoToken
    ssoToken = null
    try {
      await auth.ssoLogin(token)
    } catch {
      // token 过期/无效则静默忽略，走正常登录页
    }
  }
  if (!auth.checked) await auth.check()
  if (!to.meta.public && !auth.authenticated) return { name: 'login' }
  if (to.meta.public && auth.authenticated) return { name: 'overview' }
})

// 导航完成后兜底再清一次（处理浏览器历史前进/后退把 sso 带回地址栏等边界）。
router.afterEach(() => {
  if (new URLSearchParams(window.location.search).get('sso')) {
    const url = new URL(window.location.href)
    url.searchParams.delete('sso')
    window.history.replaceState({}, '', url.pathname + url.search)
  }
})

export default router
