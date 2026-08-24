# 模型测试页「快速模板」功能实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 模型测试页「用户输入」卡支持**用户自建模板**：一键保存当前输入区全部内容（文本 + 图片附件），点模板自动回填、可删除。替代最初方案中的两个写死快速按钮（用户已拍板废弃）。

**Architecture:** 纯前端改动。新增 `lib/templateStore.ts` 封装 IndexedDB（存文本 + Blob 图片）；`ModelTestView.vue` 加「保存为模板」按钮（发送按钮右侧）与「模板」按钮（添加图片或资源右侧，popover 列表）。图片以 Blob 存 IndexedDB（浏览器原生支持），回填时 `URL.createObjectURL` 还原。

**Tech Stack:** Vue 3 SFC、IndexedDB（零依赖）、shadcn-vue（Dialog/Popover/Input）、Tailwind 工具类、`@remixicon/vue`。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 快速按钮 | **废弃删除** | 用户原话：「删除这2个按钮吧」——「快速：你好」「快速：识图」及相关 handler/import 全删 |
| 模板存储 | **IndexedDB** | 用户原话：「包括图片（放在 blob 那个叫啥来着）」→ IndexedDB 原生支持存 Blob。DB `loadout-model-test` / store `templates`，keyPath `id` |
| 模板内容 | draft 文本 + 全部附件（含图片 Blob） | 图片 `preview` 是 blob URL → `fetch()` 取回 Blob 存入；回填时 `new File([blob])` 走 `addFiles` 还原 |
| 保存入口 | 「发送」按钮**右侧**加「保存为模板」 | 弹 Dialog 输入模板名（用户选「保存时输入名称」） |
| 模板展示 | 「添加图片或资源」**右侧** | 原快速按钮位置；popover 内列表：模板名 + 点击回填 + 删除按钮 |
| 点击行为 | 点击模板 → 自动回填输入区 | 用户选「点击自动回填」：清空当前输入区 → draft=模板文本、附件=模板图片 |
| 删除 | 每个模板项上删除按钮 | `deleteTemplate(id)` + 列表刷新 |
| 图标 | `RiSave3Line`（保存为模板）/ `RiBookmark2Line`（模板列表） | remixicon 同包 |

### 边界写死（不做）

- **不做** 模板改名 / 覆盖更新（删了重建即可）
- **不做** 模板持久化到后端（本地 IndexedDB 足够，符合"零依赖/最小改动"）
- **不做** 批量管理（全删）——列表尾部不加额外清空按钮（与用户"支持删除"最小语义对齐；需要再说）
- **不做** 单元测试——IndexedDB 封装小而独立，浏览器实测覆盖
- **不动** 左侧 Messages 列表、响应区、请求记录

---

## 背景事实（已核实，代码位置）

1. **输入态**：`ModelTestView.vue:150-152` `attachments = ref<Attachment[]>`、`draft = ref('')`。`Attachment = { id; name; kind: 'image'|'file'; preview? }`（:28）。
2. **附件添加入口**：`addFiles(files: FileList | File[])`（:291-301）→ 生成 `URL.createObjectURL(file)` 预览。回填模板图片要复用此函数（id/kind/preview 都自动）。
3. **附件清空**：`clearAttachments()`（:271-274）revoke 所有 preview URL。回填前先清空。
4. **blob URL → Blob**：现有 `blobUrlToDataUrl`（:323-335）fetch blob URL 转 dataURL；模板保存需要 `blobUrlToBlob`（fetch → blob 对象）——数据量更小、IndexedDB 直接存。
5. **底部按钮区**：`:901-925` 原 `justify-between` 行，左「添加图片或资源」、右「停止/清空/发送」。已改为左按钮组（添加资源 + 快速按钮），需**回滚**到去掉快速按钮，并在组内加「模板」按钮。
6. **shadcn-vue 全局注册**：Dialog/Input/Popover/Button 全局可用（项目惯例），无需 import 组件。
7. **remixicon 图标**：页面已 import 多个 `Ri*` 图标（:3-13）。新增 `RiSave3Line`、`RiBookmark2Line` 同包同模式。
8. **streaming 互锁**：`streaming.value` 存在（:158）；模板回填/保存不涉及发送，**无需互锁**（只是编辑输入态）。
9. **vue-tsc 严格模式**：IndexedDB 类型需在 `lib/templateStore.ts` 里写全（IDBDatabase 等 lib.dom 类型存在，无需额外依赖）。

---

## 数据模型（IndexedDB）

DB 名 `loadout-model-test`，version 1，objectStore `templates`（keyPath `id`，自增 id 用 `Date.now()` + 随机后缀）。

```ts
export type TemplateAttachment = {
  name: string
  kind: 'image' | 'file'
  blob: Blob
}
export type TestTemplate = {
  id: string
  name: string
  text: string
  attachments: TemplateAttachment[]
  savedAt: number
}
```

---

## 文件清单

新增：
- `frontend/src/lib/templateStore.ts` — IndexedDB 封装（list/save/remove）

修改：
- `frontend/src/views/ModelTestView.vue` — 删快速按钮代码；加保存/回填/删除逻辑；加两个按钮 + 保存命名 Dialog + 模板 popover

---

## Task 1: 新建 templateStore.ts

**Files:**
- Add: `frontend/src/lib/templateStore.ts`

IndexedDB 封装，三函数：

```ts
// lib/templateStore.ts
const DB_NAME = 'loadout-model-test'
const STORE = 'templates'

export type TemplateAttachment = {
  name: string
  kind: 'image' | 'file'
  blob: Blob
}

export type TestTemplate = {
  id: string
  name: string
  text: string
  attachments: TemplateAttachment[]
  savedAt: number
}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1)
    request.onupgradeneeded = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: 'id' })
      }
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

export async function listTemplates(): Promise<TestTemplate[]> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly')
    const req = tx.objectStore(STORE).getAll()
    req.onsuccess = () =>
      resolve((req.result as TestTemplate[]).sort((a, b) => b.savedAt - a.savedAt))
    req.onerror = () => reject(req.error)
  })
}

export async function saveTemplate(template: TestTemplate): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).put(template)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

export async function removeTemplate(id: string): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).delete(id)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}
```

验证：`pnpm vue-tsc --noEmit`（IndexedDB 类型 lib.dom 自带）。

---

## Task 2: ModelTestView.vue 删除快速按钮代码

**Files:**
- Modify: `frontend/src/views/ModelTestView.vue`

1. import 去掉 `RiCameraLensLine`、`RiFlashlightLine`（上一轮加的），按需加 `RiSave3Line`、`RiBookmark2Line`。
2. 删 `quickFetching` / `loadRemoteImage` / `quickHello` / `quickVision` 整块。
3. 模板按钮区改回：左组 = 「添加图片或资源」+「模板」；右组 = 「停止/清空/发送」+「保存为模板」。

---

## Task 3: ModelTestView.vue 模板逻辑 + UI

**Files:**
- Modify: `frontend/src/views/ModelTestView.vue`

### Step 1: script 部分

```ts
import { listTemplates, removeTemplate, saveTemplate, type TestTemplate } from '@/lib/templateStore'

const templates = ref<TestTemplate[]>([])
const templatePopoverOpen = ref(false)
const saveDialogOpen = ref(false)
const templateName = ref('')
const savingTemplate = ref(false)

onMounted(async () => {
  loadSkKeys()
  templates.value = await listTemplates().catch(() => [])
})

async function reloadTemplates() {
  templates.value = await listTemplates().catch(() => [])
}

// blob URL → Blob（保存模板用）
function blobUrlToBlob(url: string): Promise<Blob> {
  return fetch(url).then((res) => res.blob())
}

// 「保存为模板」：把当前 draft + 附件（图片 blob）存 IndexedDB
async function saveAsTemplate() {
  const name = templateName.value.trim()
  if (!name) return
  savingTemplate.value = true
  try {
    const attachments: TemplateAttachment[] = []
    for (const a of attachmentsRef.value) {
      if (a.preview) {
        const blob = await blobUrlToBlob(a.preview)
        attachments.push({ name: a.name, kind: a.kind, blob })
      }
    }
    const template: TestTemplate = {
      id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      name,
      text: draft.value,
      attachments,
      savedAt: Date.now(),
    }
    await saveTemplate(template)
    templates.value = await listTemplates().catch(() => templates.value)
    saveDialogOpen.value = false
    templateName.value = ''
  } finally {
    savingTemplate.value = false
  }
}

// 点击模板 → 回填输入区
function applyTemplate(template: TestTemplate) {
  clearAttachments()
  draft.value = template.text
  for (const t of template.attachments) {
    const file = new File([t.blob], t.name, { type: t.blob.type })
    addFiles([file])
  }
  templatePopoverOpen.value = false
}

// 删除模板
async function deleteTemplateItem(id: string) {
  await removeTemplate(id)
  templates.value = await listTemplates().catch(() => templates.value)
}
```

注意命名冲突：组件已有 `attachments` ref，`TemplateAttachment` 类型名与模板附件类型同名不同义——类型别名导入时用 `type TemplateAttachment`，循环附件用 `attachmentsRef` 不行（组件内是 `attachments`），直接用 `attachments.value`。

### Step 2: 模板按钮（添加图片或资源右侧）

```vue
<Popover v-model:open="templatePopoverOpen">
  <PopoverTrigger as-child>
    <Button type="button" variant="outline" size="sm">
      <RiBookmark2Line size="16" />模板
    </Button>
  </PopoverTrigger>
  <PopoverContent class="p-2 w-72" align="start" :side-offset="6">
    <div class="flex flex-col gap-1">
      <p v-if="!templates.length" class="px-2 py-3 text-center text-xs text-muted-foreground">
        暂无模板。点右侧「保存为模板」保存当前输入。
      </p>
      <div v-for="t in templates" :key="t.id"
           class="group flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-muted cursor-pointer"
           @click="applyTemplate(t)">
        <span class="min-w-0 truncate">{{ t.name }}</span>
        <Button type="button" size="icon" variant="ghost" class="size-6 shrink-0 opacity-0 group-hover:opacity-100"
                aria-label="删除模板" @click.stop="deleteTemplateItem(t.id)">
          <RiCloseLine size="14" />
        </Button>
      </div>
    </div>
  </PopoverContent>
</Popover>
```

### Step 3: 保存为模板按钮（发送按钮右侧）+ 命名 Dialog

```vue
<Button type="button" variant="outline" :disabled="!draft.trim() && !attachments.length" @click="saveDialogOpen = true">
  <RiSave3Line size="16" />保存为模板
</Button>
```

```vue
<Dialog v-model:open="saveDialogOpen">
  <DialogContent class="sm:max-w-sm">
    <DialogHeader>
      <DialogTitle>保存为模板</DialogTitle>
      <DialogDescription>将当前文本与附件保存为模板，之后可一键回填。</DialogDescription>
    </DialogHeader>
    <Input v-model="templateName" placeholder="模板名称" @keydown.enter="saveAsTemplate" />
    <DialogFooter>
      <Button type="button" variant="outline" @click="saveDialogOpen = false">取消</Button>
      <Button type="button" :disabled="!templateName.trim() || savingTemplate" @click="saveAsTemplate">保存</Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
```

### Step 4: 验证

- `pnpm vue-tsc --noEmit` PASS
- `NODE_OPTIONS="--use-system-ca" pnpm build` PASS（safe-delete 绕过）
- 浏览器实测（Chrome MCP）：
  1. 输入文本 + 加一张图 → 点「保存为模板」→ 输入名 → 保存
  2. 清空输入区 → 点「模板」→ 点模板项 → 文本 + 图片回填
  3. 回填后点「发送」→ 多模态请求正常
  4. 删除模板 → 列表移除、刷新后仍在/不在（删除不持久化）
  5. 刷新页面 → 模板仍在（IndexedDB 持久）

---

## 明确不做的事（scope 边界）

- **不做** 模板改名/覆盖更新
- **不做** 模板同步后端
- **不做** 全删/批量管理
- **不做** 单元测试
- **不改** 后端、类型（RouteLog 等）、路由
- **不改** 左侧 Messages / 响应 / 请求记录

---

## 风险与决策点

1. **IndexedDB 可用性**：localhost 正常；隐私模式/禁用存储时 `openDB` reject → `listTemplates().catch(() => [])` 兜底，模板按钮显示空态，不崩。
2. **图片 Blob 大小**：单张 512 图几十 KB，IndexedDB 无压力。不设容量上限（浏览器配额内）。
3. **模板 id 冲突**：`Date.now()` + 随机后缀，同毫秒保存两个概率极低；冲突时 IndexedDB put 覆盖——可接受。
4. **回填时 `new File([blob])`**：addFiles 的 `file.type.startsWith('image/')` 判定基于 blob.type，IndexedDB 存的原样保留，无类型丢失。
5. **组件卸载**：`onBeforeUnmount` 已 clearAttachments（revoke URL），回填产生的 preview 也走 clearAttachments 释放，无泄漏。
6. **真实环境验证**（用户强需求）：Chrome MCP 走通 保存→回填→发送→删除→刷新持久 五步。
