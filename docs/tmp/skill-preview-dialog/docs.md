# Skill 预览 Dialog —— 调研文档

调研日期：2026-09-10
调研方式：context7 MCP（resolve-library-id + query-docs）+ tavily/Exa 联网 + 本地 node_modules 实证
目标：在一个 Dialog 里做「左侧文件树（可展开折叠）+ 右侧文件内容预览（行号、等宽字体、横向滚动）」

---

## 0. 先说结论（TL;DR）

| 问题 | 结论 |
| --- | --- |
| reka-ui 有没有 Tree 组件？ | **有。** `TreeRoot` / `TreeItem` / `TreeVirtualizer`，官方文档标了 **Alpha** 状态 |
| 本项目现在能用 reka-ui Tree 吗？ | **不能直接用。** 我们通过 `shadcn-vue-cdn` 用组件，而 cdn 包只转出 54 个 ui 组件，**不含 tree**（也没有 collapsible 之外的树相关组件） |
| 推荐实现 | **自己写递归组件**（不引新依赖）。理由见 §5 |
| 语法高亮 | **不需要新依赖**。项目已装 `highlight.js@^11.12.0`，`frontend/src/components/chat/CodeBlock.vue` 就是现成范例 |
| vueuse 有 useTree 吗？ | **没有。** `@vueuse/core` 不提供任何树结构 composable；`useElementSize` / `useClipboard` 可用 |

---

## 1. reka-ui

### 1.1 明确回答：Tree 组件存在

**存在。** context7（库 ID `/unovue/reka-ui`，2222 条代码片段）和官方文档都明确列出：

> The Tree component displays a hierarchical list of items that can be expanded or collapsed, **similar to a file system navigator**.

官方文档页面标题下方直接标注 **Alpha**。

证据链接：
- 官方文档：<https://reka-ui.com/docs/components/tree>
- context7 源：`https://github.com/unovue/reka-ui/blob/v2/docs/content/docs/components/tree.md`
- 源码实证（GitHub 代码搜索 `repo:unovue/reka-ui path:packages/core/src/Tree`）：
  - `packages/core/src/Tree/TreeRoot.vue`
  - `packages/core/src/Tree/TreeItem.vue`
  - `packages/core/src/Tree/TreeVirtualizer.vue`
  - `packages/core/src/Tree/index.ts`

三件套：`TreeRoot`（容器，默认渲染 `<ul>`）、`TreeItem`（节点，默认 `<li>`）、`TreeVirtualizer`（长列表虚拟化）。
`TreeItem` 暴露 `data-indent` / `data-expanded` / `data-selected` 三个 data attribute，可用 CSS 选择器做样式；`data-indent` 可以直接换算成 `padding-left` 实现缩进。

### 1.2 TreeRoot 关键 props（context7 拉到的 API 表）

| prop | 类型 | 说明 |
| --- | --- | --- |
| `items` | `T[]` | 顶层节点数组 |
| `getKey` | `(val: T) => string` | **必填**，返回唯一 key |
| `getChildren` | `(val: T) => T[]` | 默认取 `val.children`；**无子节点时必须返回 `undefined` 而不是 `[]`** |
| `expanded` / `defaultExpanded` | `string[]` | 展开项（受控 / 非受控），受控可 `v-model:expanded` |
| `modelValue` / `defaultValue` | 单个或数组 | 选中项，受控可 `v-model` |
| `multiple` | `boolean` | 多选 |
| `selectionBehavior` | `'replace' \| 'toggle'` | 默认 `toggle` |
| `propagateSelect` / `bubbleSelect` | `boolean` | 父子选择联动，需 `multiple` |
| `disabled` / `dir` / `as` / `asChild` | | 常规 |

Slots：`flattenItems`、`modelValue`、`expanded`。
`flattenItems` 是**扁平的**节点列表（每项带 `_id`、`bind`、`hasChildren`、`value` 等），`TreeItem` 上 `v-bind="item.bind"` 即可。

### 1.3 两种写法（官方原文，`<script setup>` 风格）

**(A) 扁平 + virtualizer 版**（官方默认示例，适合长列表）

```vue
<script setup lang="ts">
import { TreeItem, TreeRoot, TreeVirtualizer } from 'reka-ui'
// items: 任意树形数组
</script>

<template>
  <TreeRoot
    v-slot="{ flattenItems }"
    :items="items"
    :get-key="(item) => item.path"
    :get-children="(item) => item.children"
    class="list-none select-none"
  >
    <TreeItem
      v-for="item in flattenItems"
      :key="item._id"
      v-bind="item.bind"
      v-slot="{ isExpanded, isSelected, handleSelect }"
      class="flex items-center gap-1 rounded px-2 py-1 hover:bg-accent data-[selected]:bg-accent"
      :style="{ paddingLeft: `${item.level * 12 + 8}px` }"
      @select="handleSelect"
    >
      <span class="truncate">{{ item.value.name }}</span>
    </TreeItem>
  </TreeRoot>
</template>
```

**(B) 嵌套 DOM 递归版**（官方 "Nested Tree Node" 示例，文件树首选）

```vue
<!-- Tree.vue -->
<script setup lang="ts">
import { TreeItem } from 'reka-ui'

interface TreeNode {
  path: string
  name: string
  children?: TreeNode[]
}

withDefaults(defineProps<{ treeItems: TreeNode[]; level?: number }>(), { level: 0 })
</script>

<template>
  <li v-for="tree in treeItems" :key="tree.path">
    <TreeItem v-slot="{ isExpanded }" as-child :level="level" :value="tree">
      <button class="flex w-full items-center gap-1 rounded px-2 py-1 hover:bg-accent">
        <ChevronRight v-if="tree.children?.length" class="size-4" :class="isExpanded && 'rotate-90'" />
        <span class="truncate">{{ tree.name }}</span>
      </button>
      <ul v-if="isExpanded && tree.children" class="list-none">
        <Tree :tree-items="tree.children" :level="level + 1" />
      </ul>
    </TreeItem>
  </li>
</template>
```

```vue
<!-- CustomTree.vue -->
<template>
  <TreeRoot :items="items" :get-key="(item) => item.path">
    <Tree :tree-items="items" />
  </TreeRoot>
</template>
```

注意官方示例的 `:get-key` 用的是 `item.title`（假设唯一）；文件树里**必须换成 path 之类的唯一键**，同名文件会 key 冲突。

### 1.4 本项目现状：**目前无法直接使用 reka-ui 的 Tree**

实证（读本地 node_modules）：

- `frontend/package.json` **没有** `reka-ui` 依赖；`frontend/node_modules/reka-ui` **不存在**。
- 项目通过 **`shadcn-vue-cdn@^0.0.4`** 用组件：`frontend/src/main.ts` 里 `createApp(App).use(ShadcnVue)`（全局注册），`ConfirmDialog.vue` 从 `'shadcn-vue-cdn'` 按名导入 `AlertDialog`。
- `shadcn-vue-cdn/dist-lib/components/ui/` 下共 **54 个**组件目录，完整清单里 **没有 tree**，也没有 `collapsible` 之外的层级组件。
- `dist-lib/index.d.ts` 的 `export *` 列表里没有 tree。

> 也就是说：想用 reka-ui Tree，得**新装 `reka-ui` 依赖**（cdn 包自带 reka-ui，但把它打包进去了，不对外透出 Tree）。

---

## 2. VueUse（`@vueuse/core@^14.4.0`，项目已装）

### 2.1 有没有树结构的 composable？

**没有。** 查了 `/websites/vueuse`（2991 条片段）与 `/vueuse/vueuse`：VueUse 没有 `useTree` 或任何等价的文件树 / 树形辅助。它管的是「浏览器 API / 状态 / 元素」这类通用工具，不管数据结构。

替代：树形展开状态自己用 `Set<string>` / `reactive` 维护即可，或直接用「递归组件 + 每层自己的 `ref<boolean>`」。

### 2.2 已知会被用到的工具

**`useElementSize`**（context7 原文示例）

```vue
<script setup lang="ts">
import { useElementSize } from '@vueuse/core'
import { useTemplateRef } from 'vue'

const el = useTemplateRef('el')
const { width, height } = useElementSize(el)
</script>

<template>
  <div ref="el">Height: {{ height }} Width: {{ width }}</div>
</template>
```

**`useClipboard`**（context7 原文示例）

```vue
<script setup lang="ts">
import { useClipboard } from '@vueuse/core'

const source = ref('Hello')
const { text, copy, copied, isSupported } = useClipboard({ source })
</script>

<template>
  <div v-if="isSupported">
    <button @click="copy(source)">
      <span v-if="!copied">Copy</span>
      <span v-else>Copied!</span>
    </button>
    <p>Current copied: <code>{{ text || 'none' }}</code></p>
  </div>
  <p v-else>Your browser does not support Clipboard API</p>
</template>
```
（`copied` 默认 1.5 秒后自动复位。）

注：项目里 `CodeBlock.vue` 手写了 copy 逻辑（含 `execCommand` 兜底），没走 `useClipboard`；新组件用哪个都行，但要保持一致。

### 2.3 本项目是否已用到？

`@vueuse/core` 已在 `dependencies`，但本次只在 `frontend/src` 内看到 `shadcn-vue-cdn` / `reka-ui` 相关引用出现在 `ConfirmDialog.vue`。**没有在业务组件里找到 `useClipboard` / `useElementSize` 的既有用法**（本仓库有自研的 `SplitPane.vue`，用的是原生 `ResizeObserver` + `window.resize`，没走 VueUse）。
→ 结论：这两个都可安全引入，但**不是必须**，`SplitPane.vue` 已证明原生写法在本项目就是既定风格。

---

## 3. 语法高亮 / 代码预览

### 3.1 项目已有的东西（关键）

`frontend/package.json` dependencies 里**已经有**：

- `highlight.js: ^11.12.0`
- `marked: ^18.0.10`
- `dompurify: ^3.4.14`
- `marked` + `highlight.js` 已在 `StreamMarkdownBlock.vue` / `CodeBlock.vue` 链路里用起来

**现成范例**：`frontend/src/components/chat/CodeBlock.vue`

```vue
<script setup lang="ts">
import { computed } from 'vue'
import hljs from 'highlight.js/lib/common'

const props = defineProps<{ code: string; lang?: string }>()

const highlighted = computed(() => {
  const language = props.lang && hljs.getLanguage(props.lang) ? props.lang : 'plaintext'
  return hljs.highlight(props.code, { language }).value
})
</script>

<template>
  <pre class="overflow-x-auto px-4 pb-3 m-0 font-mono leading-relaxed"><code v-html="highlighted" /></pre>
</template>
```

用 `highlight.js/lib/common`（约 40 种常用语言的精简包）而不是全量 `highlight.js`，这条在本项目已经是既定做法，**照抄即可**。

### 3.2 Shiki 值不值得引入？

- Shiki（`/shikijs/shiki`，575 片段）质量最高（TextMate 语法、VS Code 主题），但：
  - 体积大（完整 bundle 带 wasm/js regex engine + 语言/主题 chunk）
  - 典型用法是 `await createHighlighter(...)`，**异步初始化**，在 Vue 里要配合 `shallowRef` + `onMounted`，比同步的 hljs 麻烦一档
  - 细粒度包要通过 `shiki/core` + `@shikijs/langs/*` + `@shikijs/themes/*` 手动拼，配置成本明显更高

```ts
// Shiki 细粒度写法（供对比）
import { createHighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'

const highlighter = await createHighlighterCore({
  themes: [import('@shikijs/themes/nord')],
  langs: [import('@shikijs/langs/typescript')],
  engine: createJavaScriptRegexEngine(),
})
```

**建议：不引。** 项目已经有 hljs 且已有统一封装，再引 Shiki 会出现两套高亮器、两套主题 CSS，得不偿失。

### 3.3 「不引依赖、纯 `<pre>` 等宽字体」可接受吗？

**可接受，而且这是兜底路径。** 一个文件预览器的核心价值是「行号 + 等宽 + 横向滚动 + 不丢格式」，语法着色是加分项。做法：
- 外层 `overflow-auto`，内层 `<pre class="font-mono whitespace-pre">`（**必须 `whitespace-pre` 或 `pre` 默认值**，否则缩进会被折叠）
- 行号用「左侧独立列 + `select-none` + 右侧 `pre`」的 flex 双列同行高，或渲染成 `<div v-for>` 一行一个以便单行高亮/复制
- 注意：**带行号的方案最好按行渲染**（`code.split('\n')`），否则行号和换行对不齐；如果用整块 `hljs.highlight()` 的 HTML 输出再按行切，会把跨行 token 的 span 切坏

结论：**第一版直接用 hljs + 行号即可**，不需要任何新依赖。

---

## 4. 联网补充：社区怎么写文件树

搜了 `reka-ui tree component vue 3`、`shadcn-vue tree view component`：

1. **Reka UI 的 Tree 就是官方答案**，第三方 Vue 生态里的树组件（如 Origin UI Vue 的 Tree 示例）**明确标注 "Reka UI Tree"**——即直接基于 reka-ui 的 Tree 原语再加 Tailwind 样式。参考：<https://www.originui-vue.com/tree>
2. **shadcn-vue 官方 registry 里没有 tree 组件。** 网上搜到的 "shadcn tree view" 绝大多数是 **React** 的（`MrLightful/shadcn-tree-view`、reui.io 的 tree 基于 `@headless-tree/react`）——**拿不过来用**。
3. reka-ui 的 Tree 目前是 **Alpha**，官方 release notes 里还在持续加能力（例如近期才给 `TreeItem` 加 `disabled`），说明 API 还会动。

---

## 5. 推荐实现方案

### 结论：**自己写递归组件，不引新依赖、不用 reka-ui Tree。**

理由（按权重排序）：

1. **依赖成本**：用 reka-ui Tree 要新装 `reka-ui` 包。项目当前走的是 `shadcn-vue-cdn` 全局注册 + 按名导入，突然多一个直接依赖，风格不一致；而且 cdn 包内部自带一份 reka-ui，装两份并不划算。
2. **需求匹配度**：我们要的是「文件树 + 单一选中 + 展开折叠」。reka-ui Tree 的价值在 a11y、键盘导航、多选/checkbox 传播、虚拟化——这些我们**用不到或暂不需要**。
3. **成熟度**：reka-ui Tree 是 **Alpha**，API 还在改；自研递归组件 100% 可控，样式完全跟项目 Tailwind 体系走，也不会被上游 break。
4. **手写成本很低**：文件树递归组件大约 80~120 行；展开状态用 `Set<string>`（存 path）即可；选中状态一个 `ref<string>`。真正的技术点只有两个（见下）。

### 建议结构

```
frontend/src/components/file-preview/
  FilePreviewDialog.vue     # Dialog 壳 + 左树右预览布局
  FileTreeNode.vue          # 递归树节点（自引用）
```
复用现有 `frontend/src/components/SplitPane.vue` 做左右分栏（不要在 Dialog 里重写分栏逻辑），预览区复用 `CodeBlock.vue` 的高亮思路。

### 两个真正的坑

1. **递归组件自引用**：`<script setup>` 里组件要自引用，Vue 3 通过「文件名推导组件名」支持——文件名必须是 `FileTreeNode.vue` 且用 `<FileTreeNode>` 标签（或在 `defineOptions({ name: 'FileTreeNode' })` 显式声明，最稳）。
2. **行号与横向滚动**：
   - 行号列必须 `sticky left-0`（或独立容器），横向滚动时保持可见、且**不能被滚走**；
   - 行号列和代码列必须**用完全相同的 `line-height` / 字号**，否则对不齐；
   - 行号列 `select-none`，避免复制时把行号一起复制进去。
3. 树很深时（>50 层）递归嵌套 DOM 有性能风险，但文件树实际深度通常 <20，可忽略。

### 关键片段（自研树的骨架）

```vue
<!-- FileTreeNode.vue -->
<script setup lang="ts">
import { computed } from 'vue'

interface FileNode { path: string; name: string; type: 'file' | 'dir'; children?: FileNode[] }

const props = defineProps<{
  node: FileNode
  depth: number
  expanded: Set<string>
  selected: string | null
}>()
const emit = defineEmits<{
  toggle: [path: string]
  select: [node: FileNode]
}>()

const isOpen = computed(() => props.expanded.has(props.node.path))
</script>

<template>
  <div>
    <button
      class="flex w-full items-center gap-1 rounded px-2 py-1 text-left hover:bg-accent"
      :class="selected === node.path && 'bg-accent font-medium'"
      :style="{ paddingLeft: `${depth * 12 + 8}px` }"
      @click="node.type === 'dir' ? emit('toggle', node.path) : emit('select', node)"
    >
      <ChevronRight v-if="node.type === 'dir'" class="size-3.5 shrink-0 transition-transform" :class="isOpen && 'rotate-90'" />
      <span class="truncate">{{ node.name }}</span>
    </button>

    <FileTreeNode
      v-for="child in isOpen ? node.children : []"
      :key="child.path"
      :node="child"
      :depth="depth + 1"
      :expanded="expanded"
      :selected="selected"
      @toggle="emit('toggle', $event)"
      @select="emit('select', $event)"
    />
  </div>
</template>
```

Dialog 壳直接用项目已注册的 `Dialog` / `DialogContent` / `DialogHeader` / `DialogTitle`（`shadcn-vue-cdn` 已导出，见 §1.4）；宽高用 `DialogContent` 的 class 覆盖成接近全屏（如 `max-w-[90vw] w-[90vw] h-[85vh] p-0`），内部 `flex flex-col min-h-0` + `overflow-hidden` 才能让子级滚动生效。

---

## 6. 版本 / 来源速查

| 库 | context7 库 ID | 本地版本 | 结论 |
| --- | --- | --- | --- |
| reka-ui | `/unovue/reka-ui` | 未安装（cdn 内嵌 ^2.10.1） | 有 Tree，Alpha；本项目暂不可直接用 |
| vueuse | `/websites/vueuse` | `@vueuse/core@^14.4.0` | 无 useTree；useClipboard/useElementSize 可用 |
| shiki | `/shikijs/shiki` | 未安装 | 不推荐引入 |
| highlight.js | `/highlightjs/highlight.js` | `^11.12.0` **已装** | 直接复用 `CodeBlock.vue` 写法 |
| shadcn-vue-cdn | — | `^0.0.4` | 54 个组件，**无 tree**；有 dialog/scroll-area/collapsible |
