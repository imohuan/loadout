// 临时验证页：三种失败形态各渲染一格 RouteLogErrorCell（列表 trigger 模式），
// 用于人工/自动化 hover 检查「是否都能弹出详情卡片」。
import { createApp, h } from 'vue'
import '@/style.css'
import 'shadcn-vue-cdn/style.css'
import RouteLogErrorCell from '@/components/route-logs/RouteLogErrorCell.vue'

const cases = [
  {
    id: 'json',
    json: '{"error":{"data":{"code":14018,"msg":"额度已用尽，请访问 https://example.com 购买加量包","requestId":"3e8372f47d572bfa"}}}',
    message: '上游返回错误(429): 额度已用尽',
  },
  { id: 'message-only', json: '', message: '聚合模型 "deepseek-auto" 的所有目标当前不可用：目标 "glm-5.3-flash" 不可用：模型免费额度用完冷却中' },
  { id: 'plain', json: 'gateway timeout after 30s', message: 'force-stream: 读取上游流式响应失败: context deadline exceeded' },
]

createApp({
  render: () =>
    h(
      'div',
      { class: 'p-6 space-y-4' },
      cases.map((c) =>
        h('div', { 'data-case': c.id, class: 'border p-2' }, [
          h(RouteLogErrorCell, { json: c.json, message: c.message, label: '上游原始响应' }),
        ]),
      ),
    ),
}).mount('#app')
