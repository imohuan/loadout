<script setup>
import { ref, reactive, onMounted } from 'vue'
import McpManager from './McpManager.vue'
import AggregateManager from './AggregateManager.vue'

// ===== 通用 =====
const authed = ref(false)
const page = ref('overview')
const msg = ref('')       // 全局操作提示（成功/失败）
const msgKind = ref('ok')

function flash(text, kind = 'ok') {
  msg.value = text
  msgKind.value = kind
  setTimeout(() => (msg.value = ''), 4000)
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    ...opts
  })
  if (res.status === 401) {
    authed.value = false
    throw new Error('未登录或会话已过期')
  }
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error?.message || `HTTP ${res.status}`)
  return data
}

// ===== 登录 =====
const loginForm = reactive({ username: 'admin', password: '' })
const loginError = ref('')

async function login() {
  loginError.value = ''
  try {
    await api('/api/login', { method: 'POST', body: JSON.stringify(loginForm) })
    authed.value = true
    await loadAll()
  } catch (e) {
    loginError.value = e.message
  }
}

async function logout() {
  await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' })
  authed.value = false
}

// ===== 概览 =====
const overview = ref(null)

// ===== 渠道 =====
const channels = ref([])
const channelForm = reactive({ name: '', base_url: '', api_key: '', enabled: true })
const editingChannelId = ref(null)

async function loadChannels() {
  channels.value = await api('/api/channels')
}
function openAddChannel() {
  editingChannelId.value = null
  Object.assign(channelForm, { name: '', base_url: '', api_key: '', enabled: true })
}
async function saveChannel() {
  if (!channelForm.name || !channelForm.base_url) return flash('名称和地址必填', 'err')
  try {
    if (editingChannelId.value) {
      await api(`/api/channels/${editingChannelId.value}`, { method: 'PUT', body: JSON.stringify(channelForm) })
    } else {
      await api('/api/channels', { method: 'POST', body: JSON.stringify(channelForm) })
    }
    await loadChannels()
    openAddChannel()
    flash('渠道已保存')
  } catch (e) { flash(e.message, 'err') }
}
function editChannel(c) {
  editingChannelId.value = c.id
  Object.assign(channelForm, { name: c.name, base_url: c.base_url, api_key: '', enabled: c.enabled })
}
async function deleteChannel(c) {
  if (!confirm(`删除渠道「${c.name}」？`)) return
  try { await api(`/api/channels/${c.id}`, { method: 'DELETE' }); await loadChannels(); flash('已删除') }
  catch (e) { flash(e.message, 'err') }
}
async function moveChannel(c, dir) {
  try { await api(`/api/channels/${c.id}/move`, { method: 'POST', body: JSON.stringify({ direction: dir }) }); await loadChannels() }
  catch (e) { flash(e.message, 'err') }
}
async function refreshModels(c) {
  try { await api(`/api/channels/${c.id}/refresh-models`, { method: 'POST', body: JSON.stringify({}) }); await loadChannels(); flash('模型列表已刷新') }
  catch (e) { flash(e.message, 'err') }
}

// ===== 能力路由（视觉附加） =====
const capabilityRoutes = ref([])
const routeForm = reactive({ modelsText: '', capability: 'vision', route: 'proxy', viaOptions: [{ via_model: '', channel_id: '' }] })
const editingRouteKey = ref(null) // { models: string[], capability }
const routeLabel = { native: '原生透传', proxy: '附加代理', error: '拒绝' }

async function loadRoutes() { capabilityRoutes.value = await api('/api/capability-routes') }
function openAddRoute() {
  editingRouteKey.value = null
  Object.assign(routeForm, { modelsText: '', capability: 'vision', route: 'proxy', viaOptions: [{ via_model: '', channel_id: '' }] })
}
function editRoute(r) {
  editingRouteKey.value = { models: [...(r.models || [])], capability: r.capability }
  const opts = (r.via_options && r.via_options.length)
    ? r.via_options.map(o => ({ via_model: o.via_model || '', channel_id: o.channel_id || '' }))
    : [{ via_model: '', channel_id: '' }]
  Object.assign(routeForm, {
    modelsText: (r.models || []).join(', '),
    capability: r.capability,
    route: r.route,
    viaOptions: opts
  })
}
function addViaOption() { routeForm.viaOptions.push({ via_model: '', channel_id: '' }) }
function removeViaOption(i) { routeForm.viaOptions.splice(i, 1) }
function channelName(id) {
  if (!id) return '自动路由'
  const c = channels.value.find(x => x.id === id)
  return c ? c.name : id
}
function sameModels(a, b) {
  if (!a || !b || a.length !== b.length) return false
  return a.every((v, i) => v === b[i])
}
async function saveRoute() {
  const models = routeForm.modelsText.split(/[,\s]+/).filter(Boolean)
  if (!models.length) return flash('目标模型必填', 'err')
  const item = {
    models,
    capability: routeForm.capability,
    route: routeForm.route,
    via_options: routeForm.route === 'proxy'
      ? routeForm.viaOptions.map(o => ({ via_model: o.via_model.trim(), channel_id: o.channel_id })).filter(o => o.via_model)
      : []
  }
  let next = capabilityRoutes.value.filter(r =>
    !(editingRouteKey.value && r.capability === editingRouteKey.value.capability && sameModels(r.models, editingRouteKey.value.models))
  )
  if (next.some(r => r.capability === item.capability && sameModels(r.models, item.models))) {
    return flash(`路由「${item.models.join(',')} × ${item.capability}」已存在`, 'err')
  }
  next = [...next, item]
  try {
    await api('/api/capability-routes', { method: 'PUT', body: JSON.stringify(next) })
    await loadRoutes(); openAddRoute(); flash('能力路由已保存')
  } catch (e) { flash(e.message, 'err') }
}
async function deleteRoute(r) {
  if (!confirm(`删除路由「${(r.models || []).join(',')} × ${r.capability}」？`)) return
  const next = capabilityRoutes.value.filter(x => !(x.capability === r.capability && sameModels(x.models, r.models)))
  try {
    await api('/api/capability-routes', { method: 'PUT', body: JSON.stringify(next) })
    await loadRoutes(); flash('已删除')
  } catch (e) { flash(e.message, 'err') }
}

// ===== 测试模型 =====
const testModal = reactive({ show: false, channel: null, model: '', vision: false, loading: false, result: null })

function openTest(channel) {
  Object.assign(testModal, { show: true, channel, model: '', vision: false, loading: false, result: null })
}
async function runTest() {
  testModal.loading = true
  testModal.result = null
  try {
    const r = await api('/api/channels/test', {
      method: 'POST',
      body: JSON.stringify({ id: testModal.channel?.id || '', model: testModal.model, vision: testModal.vision })
    })
    testModal.result = r
  } catch (e) {
    testModal.result = { ok: false, error: e.message }
  } finally {
    testModal.loading = false
  }
}

// ===== Skills =====
const skills = ref([])
const presets = ref([])
const skillForm = reactive({ name: '', source: '', version: '' })
const skillFile = ref(null)
const presetForm = reactive({ name: '', skillsText: '' })

async function loadSkills() { skills.value = await api('/api/skills') }
async function loadPresets() { presets.value = await api('/api/presets') }
async function installSkill() {
  if (!skillForm.name) return flash('技能名必填', 'err')
  if (!skillForm.source) return flash('来源必填', 'err')
  try {
    await api('/api/skills/install', { method: 'POST', body: JSON.stringify(skillForm) })
    await loadSkills(); Object.assign(skillForm, { name: '', source: '', version: '' }); flash('已下载安装')
  } catch (e) { flash(e.message, 'err') }
}
function onSkillFile(e) { skillFile.value = e.target.files?.[0] || null }
async function importSkillZip() {
  if (!skillFile.value) return flash('请选择 zip 文件', 'err')
  const name = skillForm.name || skillFile.value.name.replace(/\.zip$/i, '')
  const fd = new FormData()
  fd.append('file', skillFile.value)
  fd.append('name', name)
  try {
    const res = await fetch('/api/skills/import-zip', { method: 'POST', credentials: 'same-origin', body: fd })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(data.error?.message || `HTTP ${res.status}`)
    await loadSkills(); skillFile.value = null; flash(`已导入技能「${name}」`)
  } catch (e) { flash(e.message, 'err') }
}
async function deleteSkill(s) {
  if (!confirm(`移除技能「${s.name}」？`)) return
  try { await api(`/api/skills/${s.name}`, { method: 'DELETE' }); await loadSkills(); flash('已移除') }
  catch (e) { flash(e.message, 'err') }
}
async function createPreset() {
  if (!presetForm.name) return flash('预设名必填', 'err')
  const list = presetForm.skillsText.split(/[,\s]+/).filter(Boolean)
  try {
    await api('/api/presets', { method: 'POST', body: JSON.stringify({ name: presetForm.name, skills: list }) })
    await loadPresets(); Object.assign(presetForm, { name: '', skillsText: '' }); flash('预设已创建')
  } catch (e) { flash(e.message, 'err') }
}
async function applyPreset(p) {
  try { await api('/api/presets/apply', { method: 'POST', body: JSON.stringify({ name: p.name }) }); flash(`已切换到预设「${p.name}」`); await loadOverview() }
  catch (e) { flash(e.message, 'err') }
}
async function deletePreset(p) {
  if (!confirm(`删除预设「${p.name}」？`)) return
  try { await api('/api/presets', { method: 'DELETE', body: JSON.stringify({ name: p.name }) }); await loadPresets(); flash('已删除') }
  catch (e) { flash(e.message, 'err') }
}

// ===== 密钥 =====
const keys = ref({ sk_keys: [], mcp_keys: [] })
const skForm = reactive({ name: '', models: '' })
const newKey = ref('')
const mcpKeyForm = reactive({ endpoint: '' })

async function loadKeys() { keys.value = await api('/api/keys') }
async function createSkKey() {
  try {
    const r = await api('/api/keys/sk', {
      method: 'POST',
      body: JSON.stringify({ name: skForm.name, models: skForm.models ? skForm.models.split(/[,\s]+/).filter(Boolean) : [] })
    })
    newKey.value = r.key
    skForm.name = ''
    await loadKeys()
  } catch (e) { flash(e.message, 'err') }
}
async function deleteSkKey(k) {
  if (!confirm(`删除 key「${k.name}」？`)) return
  try { await api(`/api/keys/sk/${k.id}`, { method: 'DELETE' }); await loadKeys(); flash('已删除') }
  catch (e) { flash(e.message, 'err') }
}
async function createMcpKey() {
  if (!mcpKeyForm.endpoint) return flash('端点必填（如 /mcp/group1）', 'err')
  try {
    const r = await api('/api/keys/mcp', { method: 'POST', body: JSON.stringify({ endpoint: mcpKeyForm.endpoint }) })
    newKey.value = r.key
    await loadKeys()
  } catch (e) { flash(e.message, 'err') }
}
async function deleteMcpKey(k) {
  try { await api('/api/keys/mcp', { method: 'DELETE', body: JSON.stringify({ endpoint: k.endpoint }) }); await loadKeys(); flash('已关闭') }
  catch (e) { flash(e.message, 'err') }
}

// ===== 设置 =====
const settings = ref({ active_preset: '', default_model: '' })
const pwdForm = reactive({ old: '', new: '' })

// ===== 插件 =====
const plugins = ref([])

async function loadPlugins() {
  const r = await api('/api/plugins')
  plugins.value = r.plugins || []
}

async function loadSettings() { settings.value = await api('/api/settings') }
async function saveSettings() {
  try { await api('/api/settings', { method: 'PUT', body: JSON.stringify(settings.value) }); flash('设置已保存') }
  catch (e) { flash(e.message, 'err') }
}
async function changePassword() {
  if (!pwdForm.new) return flash('新密码必填', 'err')
  try {
    await api('/api/change-password', { method: 'POST', body: JSON.stringify({ old: pwdForm.old, new: pwdForm.new }) })
    Object.assign(pwdForm, { old: '', new: '' }); flash('密码已修改')
  } catch (e) { flash(e.message, 'err') }
}

// ===== 页面加载 =====
async function loadOverview() { overview.value = await api('/api/overview') }

const loaders = {
  overview: loadOverview,
  channels: async () => { await loadChannels(); await loadRoutes() },
  aggregates: async () => {},
  mcp: async () => {},
  skills: async () => { await loadSkills(); await loadPresets() },
  keys: loadKeys,
  settings: loadSettings,
  plugins: loadPlugins
}

async function go(p) {
  page.value = p
  msg.value = ''
  try { await loaders[p]() } catch (e) { flash(e.message, 'err') }
}

async function loadAll() {
  await loadOverview()
  await loaders[page.value]()
}

onMounted(async () => {
  try {
    await loadOverview()
    authed.value = true
  } catch {
    authed.value = false
  }
})
</script>

<template>
  <!-- 登录页 -->
  <div v-if="!authed" class="login-wrap">
    <div class="card">
      <h1>Loadout</h1>
      <p class="sub">能力配装 · MCP 聚合网关 · skills 预设管理</p>
      <p class="error" v-if="loginError">{{ loginError }}</p>
      <div class="field" style="margin-top:16px">
        <label>用户名</label>
        <input v-model="loginForm.username" autocomplete="username" />
      </div>
      <div class="field">
        <label>密码</label>
        <input v-model="loginForm.password" type="password" autocomplete="current-password" @keyup.enter="login" />
      </div>
      <button class="btn" @click="login">登录</button>
    </div>
  </div>

  <!-- 主界面 -->
  <div v-else class="layout">
    <aside class="sidebar">
      <div class="brand">Loadout</div>
      <button class="nav-item" :class="{ active: page === 'overview' }" @click="go('overview')">概览</button>
      <button class="nav-item" :class="{ active: page === 'channels' }" @click="go('channels')">渠道 / 模型</button>
      <button class="nav-item" :class="{ active: page === 'aggregates' }" @click="go('aggregates')">聚合模型</button>
      <button class="nav-item" :class="{ active: page === 'mcp' }" @click="go('mcp')">MCP 管理</button>
      <button class="nav-item" :class="{ active: page === 'skills' }" @click="go('skills')">Skills</button>
      <button class="nav-item" :class="{ active: page === 'keys' }" @click="go('keys')">密钥</button>
      <button class="nav-item" :class="{ active: page === 'settings' }" @click="go('settings')">设置</button>
      <button class="nav-item" :class="{ active: page === 'plugins' }" @click="go('plugins')">插件</button>
    </aside>

    <main class="main">
      <div class="main-head">
        <h1>{{ {
          overview: '概览', channels: '渠道 / 模型', aggregates: '聚合模型', mcp: 'MCP 管理',
          skills: 'Skills 预设', keys: '密钥', settings: '设置', plugins: '插件'
        }[page] }}</h1>
        <button class="btn ghost sm" @click="logout">登出</button>
      </div>

      <p :class="msgKind" v-if="msg">{{ msg }}</p>

      <!-- 概览 -->
      <div v-if="page === 'overview'">
        <div class="grid" v-if="overview">
          <div class="stat"><div class="k">应用</div><div class="v">{{ overview.app }}</div></div>
          <div class="stat"><div class="k">版本</div><div class="v">{{ overview.version }}</div></div>
          <div class="stat"><div class="k">插件数</div><div class="v">{{ overview.plugins }}</div></div>
          <div class="stat"><div class="k">渠道数</div><div class="v">{{ overview.channels }}</div></div>
          <div class="stat"><div class="k">当前预设</div><div class="v">{{ overview.active_preset || '—' }}</div></div>
        </div>
        <div class="card">
          <h2>快速开始</h2>
          <p class="muted" style="margin:0;line-height:1.8">
            1. 在「渠道 / 模型」添加上游 provider（NewAPI 或任意 OpenAI 兼容地址）并测试连通性；<br>
            2. 在「MCP 管理」添加上游 MCP 服务器；<br>
            3. 在「Skills」登记技能并创建/切换预设；<br>
            4. 在「密钥」签发 sk- key 与 MCP endpoint key。
          </p>
        </div>
      </div>

      <!-- 渠道 -->
      <div v-if="page === 'channels'">
        <div class="card">
          <h2>{{ editingChannelId ? '编辑渠道' : '添加渠道' }}</h2>
          <div class="form-row">
            <div class="field"><label>名称</label><input v-model="channelForm.name" placeholder="本地 NewAPI" /></div>
            <div class="field"><label>Base URL</label><input v-model="channelForm.base_url" placeholder="http://127.0.0.1:3001/v1" /></div>
          </div>
          <div class="form-row">
            <div class="field"><label>API Key（编辑留空则不修改）</label><input v-model="channelForm.api_key" type="password" /></div>
            <div class="field"><label>状态</label>
              <select v-model="channelForm.enabled">
                <option :value="true">启用</option>
                <option :value="false">禁用</option>
              </select>
            </div>
          </div>
          <div style="display:flex;gap:8px">
            <button class="btn" @click="saveChannel">{{ editingChannelId ? '保存' : '添加' }}</button>
            <button class="btn ghost" v-if="editingChannelId" @click="openAddChannel">取消</button>
          </div>
        </div>

        <div class="card">
          <h2>渠道列表</h2>
          <table v-if="channels.length">
            <thead><tr><th>名称</th><th>Base URL</th><th>模型</th><th>状态</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="(c, i) in channels" :key="c.id">
                <td>{{ c.name }}</td>
                <td class="code">{{ c.base_url }}</td>
                <td>
                  <span v-if="c.models && c.models.length" class="muted">{{ c.models.length }} 个</span>
                  <span v-else class="muted" :title="c.models_error || '未探测，路由时作为兜底匹配所有模型'">未知{{ c.models_error ? '（探测失败）' : '' }}</span>
                </td>
                <td><span class="badge" :class="c.enabled ? 'on' : 'off'">{{ c.enabled ? '启用' : '禁用' }}</span></td>
                <td><div class="actions">
                  <button class="btn sm" @click="openTest(c)">测试</button>
                  <button class="btn ghost sm" @click="refreshModels(c)">刷新模型</button>
                  <button class="btn ghost sm" @click="moveChannel(c, 'up')" :disabled="i === 0" title="上移（提高优先级）">↑</button>
                  <button class="btn ghost sm" @click="moveChannel(c, 'down')" :disabled="i === channels.length - 1" title="下移（降低优先级）">↓</button>
                  <button class="btn ghost sm" @click="editChannel(c)">编辑</button>
                  <button class="btn danger sm" @click="deleteChannel(c)">删除</button>
                </div></td>
              </tr>
            </tbody>
          </table>
          <p class="empty" v-else>还没有渠道，先在上方添加。</p>
        </div>

        <div class="card">
          <h2>能力路由（视觉附加）</h2>
          <p class="muted" style="margin:0 0 14px">
            给不支持视觉的模型附加视觉能力：拦截图片 → 按 via_options 依次调视觉模型 → 文字描述替换图片。
            目标模型支持逗号分隔多个与 * 通配；未命中默认原生透传；当前仅 vision 能力生效。
          </p>
          <div class="form-row">
            <div class="field"><label>目标模型（逗号/空格分隔，支持 * 通配）</label><input v-model="routeForm.modelsText" placeholder="deepseek-v4-flash, deepseek-*" /></div>
            <div class="field"><label>能力</label>
              <select v-model="routeForm.capability">
                <option value="vision">vision（视觉）</option>
              </select>
            </div>
            <div class="field"><label>路由方式</label>
              <select v-model="routeForm.route">
                <option value="native">原生透传</option>
                <option value="proxy">附加代理</option>
                <option value="error">拒绝</option>
              </select>
            </div>
          </div>

          <div v-if="routeForm.route === 'proxy'" style="margin-bottom:12px">
            <label>视觉候选（从上到下依次请求，失败换下一个）</label>
            <div class="form-row" v-for="(opt, i) in routeForm.viaOptions" :key="i" style="margin-bottom:6px">
              <div class="field"><input v-model="opt.via_model" placeholder="视觉模型，如 qwen3-vl-flash" /></div>
              <div class="field">
                <select v-model="opt.channel_id">
                  <option value="">自动路由（按视觉模型找渠道）</option>
                  <option v-for="c in channels" :key="c.id" :value="c.id">{{ c.name }}（{{ c.base_url }}）</option>
                </select>
              </div>
              <div class="field" style="flex:0 0 auto"><button class="btn ghost sm" @click="removeViaOption(i)" :disabled="routeForm.viaOptions.length <= 1">移除</button></div>
            </div>
            <button class="btn ghost sm" @click="addViaOption">+ 添加候选</button>
          </div>

          <div style="display:flex;gap:8px">
            <button class="btn" @click="saveRoute">{{ editingRouteKey ? '保存' : '添加' }}</button>
            <button class="btn ghost" v-if="editingRouteKey" @click="openAddRoute">取消</button>
          </div>

          <table v-if="capabilityRoutes.length" style="margin-top:14px">
            <thead><tr><th>目标模型</th><th>能力</th><th>路由</th><th>视觉候选</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="r in capabilityRoutes" :key="(r.models || []).join(',') + ':' + r.capability">
                <td class="code">{{ (r.models || []).join(', ') }}</td>
                <td>{{ r.capability }}</td>
                <td><span class="badge" :class="{ on: r.route === 'proxy' }">{{ routeLabel[r.route] || r.route }}</span></td>
                <td class="muted">{{ (r.via_options || []).map(o => o.via_model + (o.channel_id ? ' @' + channelName(o.channel_id) : '')).join(' → ') || '—' }}</td>
                <td><div class="actions">
                  <button class="btn ghost sm" @click="editRoute(r)">编辑</button>
                  <button class="btn danger sm" @click="deleteRoute(r)">删除</button>
                </div></td>
              </tr>
            </tbody>
          </table>
          <p class="empty" v-else>还没有能力路由。</p>
        </div>
      </div>

      <!-- MCP -->
      <McpManager v-if="page === 'mcp'" :api="api" :flash="flash" />

      <!-- 聚合模型 -->
      <AggregateManager v-if="page === 'aggregates'" :api="api" :flash="flash" />

      <!-- Skills -->
      <div v-if="page === 'skills'">
        <div class="card">
          <h2>下载安装技能</h2>
          <div class="form-row">
            <div class="field"><label>技能名</label><input v-model="skillForm.name" placeholder="git-tools" /></div>
            <div class="field"><label>来源</label><input v-model="skillForm.source" placeholder="owner/repo" /></div>
            <div class="field"><label>版本 / 分支</label><input v-model="skillForm.version" placeholder="main" /></div>
          </div>
          <button class="btn" @click="installSkill">下载安装</button>
          <p class="muted" style="margin:12px 0 4px">或从 zip 导入（内含 SKILL.md 的技能包）：</p>
          <div class="form-row">
            <div class="field"><label>zip 文件</label><input type="file" accept=".zip" @change="onSkillFile" /></div>
          </div>
          <button class="btn" @click="importSkillZip">导入 zip</button>
        </div>

        <div class="card">
          <h2>技能列表</h2>
          <table v-if="skills.length">
            <thead><tr><th>名称</th><th>来源</th><th>版本</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="s in skills" :key="s.name">
                <td>{{ s.name }}</td>
                <td class="code">{{ s.source }}</td>
                <td>{{ s.version }}</td>
                <td><div class="actions"><button class="btn danger sm" @click="deleteSkill(s)">移除</button></div></td>
              </tr>
            </tbody>
          </table>
          <p class="empty" v-else>还没有技能。</p>
        </div>

        <div class="card">
          <h2>预设</h2>
          <div class="form-row">
            <div class="field"><label>预设名</label><input v-model="presetForm.name" placeholder="编程向" /></div>
            <div class="field"><label>技能清单（逗号/空格分隔）</label><input v-model="presetForm.skillsText" placeholder="git-tools web-design" /></div>
          </div>
          <button class="btn" @click="createPreset">创建预设</button>

          <table v-if="presets.length" style="margin-top:14px">
            <thead><tr><th>预设名</th><th>技能</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="p in presets" :key="p.name">
                <td>{{ p.name }}</td>
                <td class="muted">{{ (p.skills || []).join(', ') || '—' }}</td>
                <td><div class="actions">
                  <button class="btn sm" @click="applyPreset(p)">切换</button>
                  <button class="btn danger sm" @click="deletePreset(p)">删除</button>
                </div></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 密钥 -->
      <div v-if="page === 'keys'">
        <div class="card" v-if="newKey">
          <h2>新密钥（仅显示一次，请复制保存）</h2>
          <pre>{{ newKey }}</pre>
          <button class="btn ghost sm" @click="newKey = ''">关闭</button>
        </div>

        <div class="card">
          <h2>创建 sk- key（模型 API）</h2>
          <div class="form-row">
            <div class="field"><label>名称</label><input v-model="skForm.name" placeholder="本机调用" /></div>
            <div class="field"><label>允许模型（空 = 不限）</label><input v-model="skForm.models" placeholder="* 或 gpt-4o,deepseek-chat" /></div>
          </div>
          <button class="btn" @click="createSkKey">创建</button>

          <table v-if="keys.sk_keys.length" style="margin-top:14px">
            <thead><tr><th>名称</th><th>前缀</th><th>模型</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="k in keys.sk_keys" :key="k.id">
                <td>{{ k.name }}</td>
                <td class="code">{{ k.prefix }}</td>
                <td class="muted">{{ (k.models || []).join(', ') || '*' }}</td>
                <td><div class="actions"><button class="btn danger sm" @click="deleteSkKey(k)">删除</button></div></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card">
          <h2>MCP endpoint key</h2>
          <div class="form-row">
            <div class="field"><label>端点</label><input v-model="mcpKeyForm.endpoint" placeholder="/mcp/group1" /></div>
          </div>
          <button class="btn" @click="createMcpKey">签发 / 重置</button>

          <table v-if="keys.mcp_keys.length" style="margin-top:14px">
            <thead><tr><th>端点</th><th>Header</th><th style="text-align:right">操作</th></tr></thead>
            <tbody>
              <tr v-for="k in keys.mcp_keys" :key="k.endpoint">
                <td class="code">{{ k.endpoint }}</td>
                <td class="code">{{ k.header_name }}</td>
                <td><div class="actions"><button class="btn danger sm" @click="deleteMcpKey(k)">关闭认证</button></div></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 设置 -->
      <div v-if="page === 'settings'">
        <div class="card">
          <h2>运行时设置</h2>
          <div class="form-row">
            <div class="field"><label>默认模型</label><input v-model="settings.default_model" placeholder="deepseek-chat" /></div>
            <div class="field"><label>当前预设</label><input v-model="settings.active_preset" placeholder="编程向" /></div>
          </div>
          <button class="btn" @click="saveSettings">保存</button>
        </div>

        <div class="card">
          <h2>修改密码</h2>
          <div class="form-row">
            <div class="field"><label>旧密码</label><input v-model="pwdForm.old" type="password" /></div>
            <div class="field"><label>新密码</label><input v-model="pwdForm.new" type="password" /></div>
          </div>
          <button class="btn" @click="changePassword">修改</button>
        </div>
      </div>

      <!-- 插件 -->
      <div v-if="page === 'plugins'">
        <div class="card">
          <h2>插件自检（{{ plugins.length }} 个插件）</h2>
          <button class="btn ghost sm" @click="loadPlugins">重新检查</button>

          <table v-if="plugins.length" style="margin-top:14px">
            <thead><tr><th>插件</th><th>检查项</th><th>结果</th></tr></thead>
            <tbody>
              <template v-for="p in plugins" :key="p.plugin">
                <tr v-for="(ck, ci) in (p.checks || [])" :key="ci">
                  <td>{{ p.plugin }}</td>
                  <td>{{ ck.name }}</td>
                  <td>
                    <template v-if="(ck.issues || []).length">
                      <div v-for="(iss, ii) in ck.issues" :key="ii"
                        class="badge" :class="iss.level === 'error' ? 'off' : (iss.level === 'warn' ? '' : 'on')"
                        style="margin:2px 0;white-space:normal;text-align:left">
                        {{ iss.level }}: {{ iss.message }}
                      </div>
                    </template>
                    <span v-else class="badge on">通过</span>
                  </td>
                </tr>
                <tr v-if="!(p.checks || []).length">
                  <td>{{ p.plugin }}</td>
                  <td colspan="2" class="muted">未注册自检项</td>
                </tr>
              </template>
            </tbody>
          </table>
          <p class="empty" v-else>暂无插件信息。</p>
        </div>
      </div>
    </main>
  </div>

  <!-- 测试模型弹窗 -->
  <div v-if="testModal.show" class="modal-mask" @click.self="testModal.show = false">
    <div class="modal">
      <div class="modal-head">
        <h2 style="margin:0">测试模型</h2>
        <button class="btn ghost sm" @click="testModal.show = false">关闭</button>
      </div>
      <p class="muted" style="margin:0 0 12px">
        渠道：{{ testModal.channel ? testModal.channel.name : '默认渠道' }}
      </p>
      <div class="form-row">
        <div class="field"><label>模型名</label><input v-model="testModal.model" placeholder="gpt-4o / qwen-vl-max" /></div>
        <div class="field" style="align-self:flex-end">
          <label><input type="checkbox" v-model="testModal.vision" style="width:auto;margin-right:6px" />视觉测试（带图）</label>
        </div>
      </div>
      <button class="btn" @click="runTest" :disabled="testModal.loading">
        {{ testModal.loading ? '测试中…' : '开始测试' }}
      </button>

      <div v-if="testModal.result" style="margin-top:14px">
        <p :class="testModal.result.ok ? 'ok' : 'error'">
          {{ testModal.result.ok ? '✓ 连通' : '✗ 失败' }} · {{ testModal.result.latency_ms }}ms
        </p>
        <pre>{{ testModal.result.ok ? testModal.result.reply : (testModal.result.error || testModal.result.body) }}</pre>
      </div>
    </div>
  </div>
</template>
