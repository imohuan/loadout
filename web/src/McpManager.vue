<script setup>
import { ref, reactive, computed, onMounted } from 'vue'

// App.vue 注入的请求与提示函数（复用其 session 处理与统一报错）。
const props = defineProps({
  api: { type: Function, required: true },
  flash: { type: Function, required: true }
})

const transportLabel = { http: 'streamable HTTP', sse: 'SSE (2024-11-05)', stdio: 'stdio 命令' }

// ===== 数据 =====
const servers = ref([])      // 上游 MCP 服务器
const groups = ref([])       // 分组
const allTools = ref([])     // 每个 enabled 上游的工具（聚合，供分组挑选）
const skills = ref([])       // 已安装技能（用于 $smart 计数）

async function loadServers() { servers.value = await props.api('/api/mcp-servers') }
async function loadGroups() { groups.value = await props.api('/api/groups') }
async function loadAllTools() {
  try { allTools.value = await props.api('/api/mcp-tools') }
  catch { allTools.value = [] }
}
async function loadSkills() {
  try { skills.value = await props.api('/api/skills') }
  catch { skills.value = [] }
}
async function loadAll() {
  await Promise.all([loadServers(), loadGroups(), loadAllTools(), loadSkills()])
}

// ===== 服务器表单 =====
const form = reactive({ name: '', transport: 'http', command: '', args: '', url: '', enabled: true })
const editingId = ref(null)
const headerRows = ref([])   // [{key,value}]
const envRows = ref([])      // [{key,value}]
const testing = ref(false)
const connResult = ref(null) // { name, ok, error, tools }

function addRow(list) { list.push({ key: '', value: '' }) }
function removeRow(list, i) { list.splice(i, 1) }
function rowsToObj(rows) {
  const o = {}
  for (const r of rows) if (r.key && r.key.trim()) o[r.key.trim()] = r.value
  return o
}
function objToRows(obj) { return Object.entries(obj || {}).map(([key, value]) => ({ key, value })) }

function openAdd() {
  editingId.value = null
  Object.assign(form, { name: '', transport: 'http', command: '', args: '', url: '', enabled: true })
  headerRows.value = []
  envRows.value = []
  connResult.value = null
}
function openEdit(s) {
  editingId.value = s.id
  Object.assign(form, {
    name: s.name,
    transport: s.transport || 'http',
    command: s.command || '',
    args: (s.args || []).join(' '),
    url: s.url || '',
    enabled: s.enabled !== false
  })
  headerRows.value = objToRows(s.headers)
  envRows.value = objToRows(s.env)
  connResult.value = null
}

function buildPayload() {
  return {
    name: form.name,
    transport: form.transport,
    command: form.command,
    args: form.args ? form.args.split(/\s+/).filter(Boolean) : [],
    env: rowsToObj(envRows.value),
    url: form.url,
    headers: rowsToObj(headerRows.value),
    enabled: form.enabled
  }
}

function validateForm() {
  if (!form.name) return '名称必填'
  if ((form.transport === 'http' || form.transport === 'sse') && !form.url) return 'URL 必填'
  if (form.transport === 'stdio' && !form.command) return '命令必填'
  return ''
}

// 连接测试：请求体为完整配置，无需先保存。
async function testConfig(payload, name) {
  testing.value = true
  connResult.value = null
  try {
    const r = await props.api('/api/mcp-servers/test', { method: 'POST', body: JSON.stringify(payload) })
    connResult.value = { name, ...r }
  } catch (e) {
    connResult.value = { name, ok: false, error: e.message }
  } finally {
    testing.value = false
  }
}
function testForm() {
  const err = validateForm()
  if (err) return props.flash(err, 'err')
  return testConfig(buildPayload(), form.name)
}
function testServer(s) { return testConfig(s, s.name) }

async function saveServer() {
  const err = validateForm()
  if (err) return props.flash(err, 'err')
  try {
    if (editingId.value) {
      await props.api(`/api/mcp-servers/${editingId.value}`, { method: 'PUT', body: JSON.stringify(buildPayload()) })
    } else {
      const created = await props.api('/api/mcp-servers', { method: 'POST', body: JSON.stringify(buildPayload()) })
      editingId.value = created.id // 转为编辑态，便于随后自动连接测试
    }
    await loadAll()
    props.flash('MCP 服务器已保存，正在连接测试…')
    await testConfig(buildPayload(), form.name) // 保存后自动连接并列出工具
  } catch (e) { props.flash(e.message, 'err') }
}

async function toggleServer(s, enabled) {
  const payload = { ...s, enabled }
  try { await props.api(`/api/mcp-servers/${s.id}`, { method: 'PUT', body: JSON.stringify(payload) }); await loadAll() }
  catch (e) { props.flash(e.message, 'err') }
}
async function deleteServer(s) {
  if (!confirm(`删除 MCP「${s.name}」？`)) return
  try { await props.api('/api/mcp-servers', { method: 'DELETE', body: JSON.stringify({ id: s.id }) }); await loadAll(); props.flash('已删除') }
  catch (e) { props.flash(e.message, 'err') }
}

function serverAddress(s) {
  if (s.transport === 'stdio') return (s.command || '') + ' ' + (s.args || []).join(' ')
  return s.url || ''
}

// ===== 分组 =====
const groupForm = reactive({ name: '' })
const editingGroupName = ref(null)
const selectedTools = ref([]) // [{server_id, tool_name}]

function isSelected(serverId, toolName) {
  return selectedTools.value.some(t => t.server_id === serverId && t.tool_name === toolName)
}
function toggleTool(serverId, toolName) {
  if (isSelected(serverId, toolName)) {
    selectedTools.value = selectedTools.value.filter(t => !(t.server_id === serverId && t.tool_name === toolName))
  } else {
    selectedTools.value.push({ server_id: serverId, tool_name: toolName })
  }
}
function openAddGroup() {
  editingGroupName.value = null
  groupForm.name = ''
  selectedTools.value = []
}
function openEditGroup(g) {
  editingGroupName.value = g.name
  groupForm.name = g.name
  selectedTools.value = (g.tools || []).map(t => ({ server_id: t.server_id, tool_name: t.tool_name }))
}
async function saveGroup() {
  if (!groupForm.name) return props.flash('分组名必填', 'err')
  const group = { name: groupForm.name, tools: selectedTools.value }
  const next = [...groups.value.filter(g => g.name !== groupForm.name), group]
  try {
    await props.api('/api/groups', { method: 'PUT', body: JSON.stringify(next) })
    await loadAll()
    openAddGroup()
    props.flash('分组已保存')
  } catch (e) { props.flash(e.message, 'err') }
}
async function deleteGroup(g) {
  if (!confirm(`删除分组「${g.name}」？`)) return
  try { await props.api('/api/groups', { method: 'DELETE', body: JSON.stringify({ name: g.name }) }); await loadAll(); props.flash('已删除') }
  catch (e) { props.flash(e.message, 'err') }
}

// ===== 端点 =====
function toolCountForServer(id) {
  const entry = allTools.value.find(x => x.id === id)
  return entry ? entry.tools.length : 0
}
const endpoints = computed(() => {
  const totalToolCount = allTools.value.reduce((sum, e) => sum + (e.tools || []).length, 0) + skills.value.length
  const out = []
  for (const s of servers.value) {
    if (!s.enabled) continue
    out.push({ path: '/mcp/' + s.name, kind: '单 MCP', label: s.name, count: toolCountForServer(s.id) })
  }
  for (const g of groups.value) {
    out.push({ path: '/mcp/' + g.name, kind: '分组', label: g.name, count: (g.tools || []).length })
  }
  out.push({ path: '/mcp/$smart', kind: '聚合', label: '$smart', count: totalToolCount })
  return out
})
function endpointURL(path) { return location.origin + path }
async function copyURL(path) {
  const url = endpointURL(path)
  try { await navigator.clipboard.writeText(url); props.flash('已复制 ' + url) }
  catch (e) { props.flash('复制失败：' + e.message, 'err') }
}

onMounted(loadAll)
</script>

<template>
  <!-- 连接测试结果 -->
  <div class="card" v-if="connResult">
    <h2>连接测试 · {{ connResult.name }}</h2>
    <p :class="connResult.ok ? 'ok' : 'error'">
      {{ connResult.ok ? '✓ 连通' : '✗ 失败' }}{{ connResult.error ? ' · ' + connResult.error : '' }}
    </p>
    <div v-if="connResult.ok && (connResult.tools || []).length" class="tool-chips">
      <span class="chip" v-for="t in connResult.tools" :key="t.name" :title="t.description">
        {{ t.name }}
      </span>
      <span class="muted">共 {{ connResult.tools.length }} 个工具</span>
    </div>
    <p class="empty" v-else-if="connResult.ok">连接成功，但未发现任何工具。</p>
  </div>

  <!-- 添加上游 MCP -->
  <div class="card">
    <h2>{{ editingId ? '编辑上游 MCP' : '添加上游 MCP' }}</h2>
    <div class="form-row">
      <div class="field">
        <label>名称（决定 /mcp/{name} 端点）</label>
        <input v-model="form.name" placeholder="github" />
      </div>
      <div class="field">
        <label>传输</label>
        <select v-model="form.transport">
          <option value="http">streamable HTTP</option>
          <option value="sse">SSE (2024-11-05)</option>
          <option value="stdio">stdio 命令</option>
        </select>
      </div>
      <div class="field">
        <label>状态</label>
        <select v-model="form.enabled">
          <option :value="true">启用</option>
          <option :value="false">禁用</option>
        </select>
      </div>
    </div>

    <!-- http / sse -->
    <div v-if="form.transport === 'http' || form.transport === 'sse'">
      <div class="field">
        <label>URL（streamable HTTP / SSE 地址）</label>
        <input v-model="form.url" placeholder="http://127.0.0.1:8000/mcp" />
      </div>
      <div class="kv">
        <label>Headers（可选，附加到每个请求）</label>
        <div class="kv-row" v-for="(r, i) in headerRows" :key="i">
          <input v-model="r.key" placeholder="Header-Name" />
          <input v-model="r.value" placeholder="value" />
          <button class="btn ghost sm" @click="removeRow(headerRows, i)">−</button>
        </div>
        <button class="btn ghost sm" @click="addRow(headerRows)">+ 添加 Header</button>
      </div>
    </div>

    <!-- stdio -->
    <div v-else>
      <div class="form-row">
        <div class="field"><label>命令</label><input v-model="form.command" placeholder="npx" /></div>
        <div class="field"><label>参数（空格分隔）</label><input v-model="form.args" placeholder="-y @modelcontextprotocol/server-github" /></div>
      </div>
      <div class="kv">
        <label>Env（可选，附加环境变量）</label>
        <div class="kv-row" v-for="(r, i) in envRows" :key="i">
          <input v-model="r.key" placeholder="KEY" />
          <input v-model="r.value" placeholder="value" />
          <button class="btn ghost sm" @click="removeRow(envRows, i)">−</button>
        </div>
        <button class="btn ghost sm" @click="addRow(envRows)">+ 添加 Env</button>
      </div>
    </div>

    <div style="display:flex;gap:8px">
      <button class="btn" @click="saveServer">{{ editingId ? '保存' : '添加' }}</button>
      <button class="btn ghost" @click="testForm" :disabled="testing">{{ testing ? '连接中…' : '测试连接' }}</button>
      <button class="btn ghost" v-if="editingId" @click="openAdd">取消</button>
    </div>
  </div>

  <!-- 上游列表 -->
  <div class="card">
    <h2>上游列表</h2>
    <table v-if="servers.length">
      <thead><tr><th>名称</th><th>传输</th><th>地址 / 命令</th><th>状态</th><th style="text-align:right">操作</th></tr></thead>
      <tbody>
        <tr v-for="s in servers" :key="s.id">
          <td>{{ s.name }}</td>
          <td>{{ transportLabel[s.transport] || s.transport }}</td>
          <td class="code">{{ serverAddress(s) }}</td>
          <td><span class="badge" :class="s.enabled ? 'on' : 'off'">{{ s.enabled ? '启用' : '禁用' }}</span></td>
          <td><div class="actions">
            <button class="btn ghost sm" @click="testServer(s)">测试</button>
            <button class="btn ghost sm" @click="openEdit(s)">编辑</button>
            <button class="btn ghost sm" @click="toggleServer(s, !s.enabled)">{{ s.enabled ? '禁用' : '启用' }}</button>
            <button class="btn danger sm" @click="deleteServer(s)">删除</button>
          </div></td>
        </tr>
      </tbody>
    </table>
    <p class="empty" v-else>还没有上游 MCP，先在上方添加。</p>
  </div>

  <!-- 分组 -->
  <div class="card">
    <h2>分组</h2>
    <p class="muted" style="margin:0 0 14px">
      分组 = 从各上游 MCP 勾选工具，组合成 /mcp/{分组名} 端点暴露的工具视图。
    </p>
    <div class="form-row">
      <div class="field">
        <label>分组名（决定 /mcp/{分组名} 端点）</label>
        <input v-model="groupForm.name" placeholder="编程向" />
      </div>
      <div class="field" style="align-self:flex-end">
        <button class="btn" @click="saveGroup">{{ editingGroupName ? '保存分组' : '创建分组' }}</button>
        <button class="btn ghost" v-if="editingGroupName" @click="openAddGroup" style="margin-left:8px">取消</button>
      </div>
    </div>

    <!-- 工具挑选 -->
    <div class="picker" v-if="allTools.length">
      <p class="muted" style="margin:0 0 8px">已勾选 {{ selectedTools.length }} 个工具</p>
      <div class="picker-group" v-for="entry in allTools" :key="entry.id">
        <div class="picker-head">
          <strong>{{ entry.name }}</strong>
          <span class="badge off">{{ transportLabel[entry.transport] || entry.transport }}</span>
          <span class="muted">{{ entry.tools.length }} 工具</span>
        </div>
        <p class="error" v-if="entry.error">连接失败：{{ entry.error }}</p>
        <label class="tool-option" v-for="t in entry.tools" :key="t.name">
          <input type="checkbox" :checked="isSelected(entry.id, t.name)" @change="toggleTool(entry.id, t.name)" />
          <span class="code">{{ t.name }}</span>
          <span class="muted">{{ t.description }}</span>
        </label>
      </div>
    </div>
    <p class="empty" v-else>没有可挑选的工具：请先添加并启用上游 MCP，保存后会自动连接并刷新工具库。</p>

    <!-- 分组列表 -->
    <table v-if="groups.length" style="margin-top:14px">
      <thead><tr><th>分组名</th><th>端点</th><th>工具数</th><th style="text-align:right">操作</th></tr></thead>
      <tbody>
        <tr v-for="g in groups" :key="g.name">
          <td>{{ g.name }}</td>
          <td class="code">{{ endpointURL('/mcp/' + g.name) }}</td>
          <td>{{ (g.tools || []).length }}</td>
          <td><div class="actions">
            <button class="btn ghost sm" @click="openEditGroup(g)">编辑</button>
            <button class="btn danger sm" @click="deleteGroup(g)">删除</button>
          </div></td>
        </tr>
      </tbody>
    </table>
  </div>

  <!-- 端点一览 -->
  <div class="card">
    <h2>端点一览</h2>
    <p class="muted" style="margin:0 0 14px">
      客户端以 streamable HTTP（兼容 SSE）连接。单 MCP / 分组端点直接暴露其工具；$smart 固定暴露 status/get/invoke 三个入口，默认返回全部工具，可用 Header「X-Loadout-Group」指定分组名。
    </p>
    <table v-if="endpoints.length">
      <thead><tr><th>路由方式</th><th>名称</th><th>工具数</th><th>连接地址</th><th style="text-align:right">操作</th></tr></thead>
      <tbody>
        <tr v-for="e in endpoints" :key="e.path">
          <td><span class="badge" :class="{ on: e.kind === '聚合' }">{{ e.kind }}</span></td>
          <td class="code">{{ e.label }}</td>
          <td>{{ e.count }}</td>
          <td class="code">{{ endpointURL(e.path) }}</td>
          <td><div class="actions"><button class="btn ghost sm" @click="copyURL(e.path)">复制</button></div></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.kv { margin: 0 0 12px; }
.kv > label { margin-bottom: 6px; }
.kv-row { display: flex; gap: 8px; margin-bottom: 6px; }
.kv-row input { flex: 1; }

.tool-chips { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.chip {
  font-family: ui-monospace, Consolas, monospace;
  font-size: 12px;
  background: #0b0d11;
  border: 1px solid var(--border);
  border-radius: 20px;
  padding: 3px 10px;
}

.picker { border: 1px solid var(--border); border-radius: 8px; padding: 12px; max-height: 320px; overflow: auto; margin-bottom: 12px; }
.picker-group { margin-bottom: 14px; }
.picker-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.tool-option { display: flex; align-items: center; gap: 8px; padding: 4px 0; }
.tool-option input { width: auto; margin: 0; }
.tool-option .code { flex: 0 0 auto; }
.tool-option .muted { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
