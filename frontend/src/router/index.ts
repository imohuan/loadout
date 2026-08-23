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

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!auth.checked) await auth.check()
  if (!to.meta.public && !auth.authenticated) return { name: 'login' }
  if (to.meta.public && auth.authenticated) return { name: 'overview' }
})

export default router
