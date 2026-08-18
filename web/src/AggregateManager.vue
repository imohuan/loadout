<script setup>
import { ref, reactive, onMounted, onBeforeUnmount } from 'vue'

const props = defineProps(['api', 'flash'])

const aggregates = ref([])
const channels = ref([])
const modelHealth = ref({})
let healthTimer = null
const aggregateForm = reactive({
  name: '',
  targets: [{ model: '', channel_id: '' }]
})
const editingAggregateName = ref(null)

async function loadAggregates() {
  try {
    aggregates.value = await props.api('/api/aggregates')
  } catch (e) {
    props.flash(e.message, 'err')
  }
}

async function loadChannels() {
  try {
    channels.value = await props.api('/api/channels')
  } catch (e) {
    props.flash(e.message, 'err')
  }
}

async function loadModelHealth() {
  try {
    const list = await props.api('/api/model-health')
    modelHealth.value = Object.fromEntries((list || []).map(h => [h.model, h]))
  } catch (e) {
    props.flash(e.message, 'err')
  }
}

function openAddAggregate() {
  editingAggregateName.value = null
  Object.assign(aggregateForm, {
    name: '',
    targets: [{ model: '', channel_id: '' }]
  })
}

function editAggregate(agg) {
  editingAggregateName.value = agg.name
  Object.assign(aggregateForm, {
    name: agg.name,
    targets: (agg.targets || []).map(t => ({ model: t.model, channel_id: t.channel_id }))
  })
}

function addTarget() {
  aggregateForm.targets.push({ model: '', channel_id: '' })
}

function removeTarget(index) {
  if (aggregateForm.targets.length > 1) {
    aggregateForm.targets.splice(index, 1)
  }
}

async function saveAggregate() {
  if (!aggregateForm.name) {
    return props.flash('聚合模型名称必填', 'err')
  }
  const validTargets = aggregateForm.targets.filter(t => t.model && t.channel_id)
  if (validTargets.length === 0) {
    return props.flash('至少需要一个有效的目标（模型+渠道）', 'err')
  }

  const payload = {
    name: aggregateForm.name,
    targets: validTargets
  }

  try {
    if (editingAggregateName.value) {
      // 更新：先删除旧的，再添加新的
      let current = await props.api('/api/aggregates')
      current = current.filter(a => a.name !== editingAggregateName.value)
      current.push(payload)
      await props.api('/api/aggregates', { method: 'PUT', body: JSON.stringify(current) })
    } else {
      // 新增
      const current = await props.api('/api/aggregates')
      if (current.some(a => a.name === payload.name)) {
        return props.flash(`聚合模型「${payload.name}」已存在`, 'err')
      }
      current.push(payload)
      await props.api('/api/aggregates', { method: 'PUT', body: JSON.stringify(current) })
    }
    await loadAggregates()
    openAddAggregate()
    props.flash('聚合模型已保存')
  } catch (e) {
    props.flash(e.message, 'err')
  }
}

async function deleteAggregate(agg) {
  if (!confirm(`删除聚合模型「${agg.name}」？`)) return
  try {
    let current = await props.api('/api/aggregates')
    current = current.filter(a => a.name !== agg.name)
    await props.api('/api/aggregates', { method: 'PUT', body: JSON.stringify(current) })
    await loadAggregates()
    props.flash('已删除')
  } catch (e) {
    props.flash(e.message, 'err')
  }
}

function channelName(id) {
  const c = channels.value.find(x => x.id === id)
  return c ? c.name : id
}

function healthFor(target) {
  return modelHealth.value[target.model + '@' + target.channel_id] || null
}

function healthLabel(health) {
  if (!health) return '未检查'
  if (health.status === 'available') return '可用'
  if (health.status === 'cooling') return '冷却中'
  if (health.status === 'disabled') return '已禁用'
  return health.status || '未知'
}

function healthClass(health) {
  if (!health) return 'health-unknown'
  if (health.status === 'available') return 'health-ok'
  if (health.status === 'cooling') return 'health-warn'
  return 'health-error'
}

function healthTitle(health) {
  if (!health) return '尚未记录健康状态'
  const parts = []
  if (health.last_error) parts.push('失败原因：' + health.last_error)
  if (health.disabled_until) parts.push('冷却至：' + new Date(health.disabled_until).toLocaleString())
  if (health.fail_count) parts.push('连续失败：' + health.fail_count + ' 次')
  if (health.last_checked) parts.push('最后检查：' + new Date(health.last_checked).toLocaleString())
  return parts.join('；') || '暂无附加信息'
}

onMounted(async () => {
  await loadChannels()
  await loadAggregates()
  await loadModelHealth()
  healthTimer = setInterval(loadModelHealth, 30000)
})

onBeforeUnmount(() => {
  if (healthTimer) clearInterval(healthTimer)
})
</script>

<template>
  <div>
    <div class="card">
      <h2>{{ editingAggregateName ? '编辑聚合模型' : '添加聚合模型' }}</h2>
      <p class="muted" style="margin:0 0 14px">
        聚合模型（如 auto）会按优先级顺序轮询多个真实模型：第一个失败自动换第二个，直到成功或全部失败。
        每个目标必须指定模型名和渠道 ID，数组顺序即优先级（第一个最优先）。
      </p>
      
      <div class="field">
        <label>虚拟模型名（用户请求时填这个）</label>
        <input v-model="aggregateForm.name" placeholder="auto" :disabled="!!editingAggregateName" />
      </div>

      <div style="margin-top:14px">
        <label>目标列表（按优先级从上到下排列）</label>
        <div v-for="(target, i) in aggregateForm.targets" :key="i" class="form-row" style="margin-bottom:8px">
          <div class="field">
            <input v-model="target.model" placeholder="真实模型名，如 gpt-4" />
          </div>
          <div class="field">
            <select v-model="target.channel_id">
              <option value="">选择渠道</option>
              <option v-for="c in channels.filter(x => x.enabled)" :key="c.id" :value="c.id">
                {{ c.name }}（{{ c.base_url }}）
              </option>
            </select>
          </div>
          <div class="field" style="flex:0 0 auto">
            <button class="btn ghost sm" @click="removeTarget(i)" :disabled="aggregateForm.targets.length <= 1">移除</button>
          </div>
        </div>
        <button class="btn ghost sm" @click="addTarget">+ 添加目标</button>
      </div>

      <div style="display:flex;gap:8px;margin-top:14px">
        <button class="btn" @click="saveAggregate">{{ editingAggregateName ? '保存' : '添加' }}</button>
        <button class="btn ghost" v-if="editingAggregateName" @click="openAddAggregate">取消</button>
      </div>
    </div>

    <div class="card">
      <h2>聚合模型列表</h2>
      <table v-if="aggregates.length">
        <thead>
          <tr>
            <th>虚拟模型名</th>
            <th>目标列表（优先级顺序）</th>
            <th style="text-align:right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="agg in aggregates" :key="agg.name">
            <td class="code">{{ agg.name }}</td>
            <td class="muted">
              <div v-for="(t, i) in (agg.targets || [])" :key="i" style="margin:2px 0">
                <span>{{ i + 1 }}. <strong>{{ t.model }}</strong> @ {{ channelName(t.channel_id) }}</span>
                <span
                  class="health-badge"
                  :class="healthClass(healthFor(t))"
                  :title="healthTitle(healthFor(t))"
                >
                  {{ healthLabel(healthFor(t)) }}
                </span>
                <span
                  v-if="healthFor(t) && healthFor(t).status !== 'available' && healthFor(t).last_error"
                  class="health-error-text"
                >
                  {{ healthFor(t).last_error }}
                </span>
              </div>
            </td>
            <td>
              <div class="actions">
                <button class="btn ghost sm" @click="editAggregate(agg)">编辑</button>
                <button class="btn danger sm" @click="deleteAggregate(agg)">删除</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="empty" v-else>还没有聚合模型，先在上方添加。</p>
    </div>

    <div class="card">
      <h2>使用方式</h2>
      <p class="muted" style="margin:0;line-height:1.8">
        <strong>API 请求：</strong><br>
        向 <code>/v1/chat/completions</code> 发请求，model 填虚拟模型名（如 <code>auto</code>），系统会自动按优先级轮询。<br><br>
        <strong>示例：</strong><br>
        <code>curl http://localhost:3000/v1/chat/completions \<br>
        &nbsp;&nbsp;-H "Content-Type: application/json" \<br>
        &nbsp;&nbsp;-H "Authorization: Bearer YOUR_KEY" \<br>
        &nbsp;&nbsp;-d '{"model": "auto", "messages": [{"role": "user", "content": "你好"}]}'</code>
      </p>
    </div>
  </div>
</template>

<style scoped>
code {
  background: #f5f5f5;
  padding: 2px 6px;
  border-radius: 3px;
  font-family: 'Courier New', monospace;
  font-size: 0.9em;
}

.health-badge {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 6px;
  border: 1px solid currentColor;
  border-radius: 3px;
  font-size: 12px;
  line-height: 18px;
  cursor: help;
}

.health-ok { color: var(--ok); }
.health-warn { color: #a86400; }
.health-error { color: var(--danger); }
.health-unknown { color: var(--muted); }

.health-error-text {
  display: block;
  margin: 2px 0 4px 18px;
  color: var(--danger);
  font-size: 12px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}
</style>
