# 模型测试页快速测试按钮实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在「模型测试」页「用户输入」卡里、紧贴「添加图片或资源」按钮的右侧，新增两个**快速测试按钮**，覆盖最常见的两种自测路径：(1) 一键填"你好"；(2) 拉一张测试图 + 自动填图片识别描述。**只填充输入区，不自动发送**——发送由用户自己点「发送」。

**Architecture:** 完全前端 UI 改动，不动后端、不动类型、不动 composable。两个按钮只操作 `attachments` / `draft` 两个输入态，填充完即止，不调用 send()。**不污染左侧 Messages 列表**（与现有发文本一致：左侧只显示已编辑行，本次草稿只走右侧"用户输入"流程）。

**Tech Stack:** Vue 3 SFC（ModelTestView.vue 单一文件改动）+ Tailwind 工具类 + shadcn-vue Button（已有）+ `@remixicon/vue` 图标。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 贴图来源 | **远程 URL 临时拉** | picsum.photos 单图直链；用户已选项 `q-0`。不污染仓库，离线/抖动时按钮失败，提示但不阻塞 |
| 实现路径 | **inline 改造 ModelTestView.vue** | 用户已选项 `q-1`。零新文件、零 composable 抽象，最小改动 |
| 按钮位置 | 「添加图片或资源」**正右侧**（同一 flex 行） | 与原按钮同等大小、variant=outline |
| 文案 | 「快速：你好」/ 「快速：识图」 | 与现有 `RiImageAddLine 添加图片或资源` 同区域、视觉一体 |
| 文案输入 | "你好" → 直接 draft=「你好」；"识图" → draft=「请识别这张图片描述其内容」 | 文案写死在按钮 handler 中，无用户可编辑面板 |
| 点击行为 | 只填充输入态（draft + attachments），**不自动发送** | 用户拍板：发送由用户自己点「发送」按钮；「快速」按钮只是内容快捷填充 |
| 互锁 | streaming / 拉图进行中按钮 disabled | 复用现有 send() 头部的早退；防止重复发 |
| 图标 | `RiFlashlightLine`（你好）/ `RiCameraLensLine`（识图） | 新增两个 remixicon 引入；不引入其它图标库 |

### 边界写死（不做）

- **不做** 自定义文案输入（用户明确"目前只两个"）
- **不做** 持久化"最近一次跑的测试样本"——picsum 每次随机（picsum 的特点是"每次不同"，避免请求缓存命中用 `?random=Date.now()`）
- **不替换** 现有「发送」按钮
- **不动** 左侧 Messages 列表、不动右侧响应/请求记录
- **不引第三方图片代理**——只用 picsum.photos 直链
- **不写单元测试**——本任务纯 UI 增量，组件内增加 ~2 个函数，提交后由"前端 build + 浏览器手点"覆盖

---

## 背景事实（已核实，代码位置）

1. **底部按钮区位置**：`ModelTestView.vue:901-925`。现有结构 `flex flex-wrap items-center justify-between gap-3` 内，左侧是「添加图片或资源」`<Button variant="outline" size="sm">`，右侧是「停止/清空/发送」。
2. **现有 attachments 写入入口**：`addFiles(files: FileList | File[])` at `:291-301`（接收 File 对象）。**限制：远程 URL 不能直接进这条线**——File 对象必须来自 FileInput/Clipboard/Drop。要支持"远程拉图"必须新加一条："fetch 图片 → 转 Blob → 转 File → 喂给 addFiles"。
3. **草稿与附件同时存在时的行为**：`send():369` `const text = draft.value.trim() || (attachments.value.length ? '[附带资源]' : '')`，attachment 在前时 text 兜底；`:381-387` 若有图片，把 content 改为 `[{type:'text', text}, {type:'image_url',...}]` 多模态结构。
4. **send() 入口保护**：`send():339` `if (streaming.value) return` 同步早退；`:349-356` 检查 baseUrl/channelId/model/draft/attachments。所以按钮先 disabled 比直接调 send 更稳。
5. **addFiles 写入路径**：`addFiles(files)` 遍历 push 到 `attachments`，**预览用** `URL.createObjectURL(file)`。这一步对 Blob/File 都生效 → 我们 fetch 拿 Blob 后包 File 即可。
6. **`@remixicon/vue` 图标集**：项目已有图标 imports（`ModelTestView.vue:3-13`）。`RiFlashlightLine` 与 `RiCameraLensLine` 同包同 named export，未用到就新增 import。
7. **`picsum.photos` 行为**：直链 `https://picsum.photos/seed/test/512/512` 返回固定尺寸 JPEG；CORS 默认开启；体积适中（~30-80 KB）。无需任何 key。
8. **现有「按钮 disabled 写法」**：参考 `:906-924` 的 `streaming`/`!draft.trim() && !attachments.length` 模式。`:917-918` `v-bind` `:disabled`。
9. **CORS 兜底**：picsum 直链经浏览器 fetch 跨域时需 `mode: 'cors'`（默认就是）。若被公司代理/防火墙拦 CORS，按钮 catch 路径写到 `fetchError.value`，不抛。

---

## 改动文件清单

只改一个文件：

- `frontend/src/views/ModelTestView.vue`
  - script 块：新增 `RiFlashlightLine` / `RiCameraLensLine` import；新增 `quickHello()` / `quickVision()` 两个 handler；新增 `quickFetching` ref 防重。
  - template 块：底部按钮行左侧加两个 outline 按钮。

**无新增文件、无新增依赖。** `picsum.photos` 走浏览器 fetch，无 npm 依赖。

---

## Task 1: 加两个 handler + 两个按钮

**Files:**
- Modify: `frontend/src/views/ModelTestView.vue`

**Step 1: 扩 imports**

`ModelTestView.vue:3-13` remixicon block 末尾追加：
```ts
  RiCameraLensLine,
  RiFlashlightLine,
```
（按字母序插入，RiFlashlight 在 RiImageAddLine 之后，RiCameraLens 在 RiCloseLine 之前，自行调整；保持 sort 风格一致即可。）

**Step 2: 加 handler（含状态与按钮保护）**

`ModelTestView.vue` script 段（在 `clearDraft()` `:280` 之后、`onFileChange()` 上方或下方案区皆可，建议放在 `clearDraft()` 后）新增：

```ts
// 快速测试：只填输入态，不自动发送（发送由用户点「发送」按钮触发）。
const quickFetching = ref(false)

async function loadRemoteImage(url: string): Promise<File> {
  const response = await fetch(url, { mode: 'cors' })
  if (!response.ok) throw new Error(`图片拉取失败 (${response.status})`)
  const blob = await response.blob()
  const ext = blob.type.split('/')[1]?.split(';')[0] || 'jpg'
  return new File([blob], `quick-test-${Date.now()}.${ext}`, { type: blob.type })
}

function quickHello() {
  if (streaming.value || quickFetching.value) return
  draft.value = '你好'
}

async function quickVision() {
  if (streaming.value || quickFetching.value) return
  quickFetching.value = true
  fetchError.value = ''
  try {
    const file = await loadRemoteImage(
      `https://picsum.photos/seed/loadout-quicktest-${Date.now()}/512/512`,
    )
    addFiles([file])
    draft.value = '请识别这张图片，简要描述其内容'
  } catch (error) {
    fetchError.value =
      error instanceof Error ? error.message : '快速测试（识图）准备失败'
  } finally {
    quickFetching.value = false
  }
}
```

**注意**：`quickFetching` 不用于禁用"你好"按钮（你好的 write 是同步），只用于禁"识图"按钮的 fetch 异步段。**两个 handler 都不调用 send()**——用户点完快速按钮后自行点「发送」。

**Step 3: 加按钮（位置 = 「添加图片或资源」正右侧）**

`ModelTestView.vue:902-904` 原：

```vue
<Button type="button" variant="outline" size="sm" @click="openFilePicker">
  <RiImageAddLine size="16" />添加图片或资源
</Button>
```

改为（一组 wrap，三个一起，构成"添加资源 + 快速测试"区）：

```vue
<div class="flex flex-wrap items-center gap-2">
  <Button type="button" variant="outline" size="sm" @click="openFilePicker">
    <RiImageAddLine size="16" />添加图片或资源
  </Button>
  <Button
    type="button"
    variant="outline"
    size="sm"
    :disabled="streaming || quickFetching"
    aria-label="快速测试：发送你好"
    @click="quickHello"
  >
    <RiFlashlightLine size="16" />快速：你好
  </Button>
  <Button
    type="button"
    variant="outline"
    size="sm"
    :disabled="streaming || quickFetching"
    aria-label="快速测试：发送图片识别请求"
    @click="quickVision"
  >
    <RiCameraLensLine size="16" />快速：识图
  </Button>
</div>
```

外层 justify-between 不动，让这一组按钮整体留在原 flex-row 左侧；右侧的「停止/清空/发送」三个按钮不受影响。

**Step 4: 视觉自检**

构建并启动 vite dev，按以下清单肉眼/截图比对：
- 三个按钮宽度自适应：最窄不挤、最宽不顶；
- 同一行展示时（≥768px）三按钮全在一行；
- 移动端 / 缩窄时（<640px）允许 wrap（`flex-wrap` 已设）；
- `quickFetching` 进行中"识图"按钮 disabled；
- 点击"快速：你好"：draft 填入"你好"，**不自动发送**，发送按钮变为可用；
- 点击"快速：识图"：fetch + addFiles 后 draft 填入"请识别这张图片…"，附件区出现图片缩略图，**不自动发送**；
- 点击"快速：你好"后点「发送」→ 响应区出现回复；
- 点击"快速：识图"后点「发送」→ 响应区出现多模态回复；
- fetchError 在 catch 分支触发时显示在 send() 上方（同 `fetchError` ref 共用）。

**Step 5: 验证类型与构建**

Run（任一，按用户偏好）：
- `pnpm vue-tsc --noEmit`（类型检查）
- `pnpm build`（产物构建，验证不破 dist）

Expected：PASS。如果 vue-tsc 报 addFiles/addMessage 类型不匹配，回到 `:291-301` 加 `: File[]` 显式标注（高度可能不需要，FileList 已可迭代）。

**Step 6: Commit**

```bash
git add frontend/src/views/ModelTestView.vue
git commit -m "feat(frontend): quick-test buttons on model test page"
```

---

## 明确不做的事（scope 边界）

- **不做** 自定义快速测试 panel（用户明确"目前只两个"）
- **不做** 用户编辑 quick test 文案（用户明确）
- **不做** 后端 / 类型 / composable 改动
- **不做** 单元测试——UI 增量且行为可肉眼验证（用户偏好真实环境验证）
- **不做** 视觉缓存/离线 fallback——网络挂了就在 fetchError 显示「图片拉取失败」即可
- **不动** Messages / 响应 / 请求日志三块的任何渲染
- **不替换** 现有「发送」按钮——快速按钮只是「等价的快捷入口」

---

## 风险与决策点

1. **picsum.photos 在公司代理下 CORS 失败**：极少数场景会触发；行为落 `fetchError.value`，UI 上有红字提示，不抛异常。最坏体验：按钮点完「快速：识图」显示失败文案，用户改用「添加图片或资源」手动选图。
2. **addFiles 的 id 重复**：`nextAttachmentId` 单调递增，无 reset；快速按钮与手动添加都走这个分配，**不会撞**。
3. **快速按钮与 streaming 互锁**：按钮 disabled = `streaming || quickFetching`。由于按钮不自动发送，streaming 时禁按钮是为了防止在流式进行中覆盖 draft / 追加附件导致用户误操作；解除 disabled 后用户自行点「发送」，send() 内 `if (streaming.value) return` 仍兜底。
4. **draft 在快速按钮后被覆盖**：若用户在 draft 写了内容，再点「快速：你好」，**draft 会被覆盖成"你好"**。这是预期行为（快速按钮就是覆盖式快捷），与「手动改 draft」一致；若用户希望保留草稿，先点「清空」再点快速按钮。
5. **按钮命名**：用「快速：你好」/「快速：识图」而不是「Run Hello」/「Run Vision」，遵循用户中文产品文案偏好与项目内已有按钮中英混合风格（参见「添加图片或资源」「获取所有模型」皆中文）。
6. **真实环境验证**：改完后必须重启 vite dev（或 pnpm build + 打开 dist），手点两个按钮分别走通：
   - 「快速：你好」→ 输入区出现"你好"，**不发请求**；点「发送」→ 响应区出现回复，请求记录列表新增一行；
   - 「快速：识图」→ 输入区出现"请识别这张图片…"+ 附件缩略图，**不发请求**；点「发送」→ 响应区出现多模态回复，请求记录列表新增一行；
   - 拉图进行中「快速：识图」被 disable 拦住；streaming 期间按钮 disabled。

---

## Task 列表（执行顺序）

1. ModelTestView.vue 加 import + handler + 两个按钮（Task 1）
2. pnpm vue-tsc 类型验证
3. pnpm build 构建验证
4. 重启 vite dev（或开 dist），手点两个按钮真实环境验证
5. git commit + push
