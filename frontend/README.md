# frontend

Loadout 管理后台前端。基于 **Vue 3 + TypeScript + Vite + Tailwind CSS v4**，UI 组件用 [shadcn-vue-cdn](https://www.npmjs.com/package/shadcn-vue-cdn)，图标用 [@remixicon/vue](https://www.npmjs.com/package/@remixicon/vue)。

## 技术栈

| 用途     | 库                                                      |
| -------- | ------------------------------------------------------- |
| 框架     | Vue 3（`<script setup>` SFC）                           |
| 构建     | Vite + @vitejs/plugin-vue                               |
| 样式     | Tailwind CSS v4（@tailwindcss/vite 插件）               |
| UI 组件  | shadcn-vue-cdn（shadcn-vue 组件库打包版，内置 reka-ui） |
| 图标     | @remixicon/vue（Remix Icon，3100+ 图标）                |
| 类型检查 | vue-tsc + TypeScript                                    |

> 状态管理用 **pinia**、路由用 **vue-router**、工具函数优先 **vueuse**、跨组件全局通知用 **mitt**。这些是团队约定，见下方「开发规范」。

## 环境要求

- Node.js >= 18
- pnpm（项目用 pnpm 管理依赖，根目录有 `pnpm-workspace.yaml`）

## 快速开始

```bash
pnpm install   # 安装依赖（首次）
pnpm dev       # 启动开发服务器（Vite）
pnpm build     # 类型检查 + 生产构建 → dist/
pnpm preview   # 本地预览生产构建
```

`pnpm dev` 后浏览器打开终端提示的地址（默认 http://localhost:5173）。

## 目录结构

```
frontend/
├── index.html            # 入口 HTML
├── vite.config.ts        # Vite 配置（vue + tailwindcss 插件）
├── src/
│   ├── main.ts           # 应用入口：注册 shadcn-vue-cdn 插件
│   ├── style.css         # @import "tailwindcss"
│   ├── App.vue           # 根组件
│   ├── components/       # 业务组件（最小化，只干自己的事）
│   ├── composables/      # 组合式函数 / hooks（最小化，优先 vueuse）
│   ├── stores/           # pinia 状态
│   ├── router/           # vue-router 路由
│   └── utils/            # 工具函数（优先用现成库）
└── public/               # 静态资源
```

## UI 组件：shadcn-vue-cdn

组件已在 `src/main.ts` 全局注册：

```ts
import { createApp } from 'vue'
import 'shadcn-vue-cdn/style.css' // 基础样式（必须）
import { ShadcnVue } from 'shadcn-vue-cdn'

createApp(App).use(ShadcnVue).mount('#app')
```

全局注册后，模板里直接写组件标签即可，无需手动 import：

```vue
<template>
  <Button variant="default">点我</Button>
  <Card>
    <CardHeader>
      <CardTitle>标题</CardTitle>
    </CardHeader>
    <CardContent>内容</CardContent>
  </Card>
</template>
```

也可按需导入（组件名与全局标签一致）：

```ts
import { Button, Card, CardContent, CardHeader, CardTitle } from 'shadcn-vue-cdn'
```

### 组件清单

50+ 个组件族，常用：

- **基础**：Button / Input / Textarea / Label / Checkbox / RadioGroup / Switch / Select / Slider / Toggle / InputOTP
- **反馈**：Alert / Badge / Skeleton / Spinner / Progress / Sonner（toast）
- **浮层**：Dialog / AlertDialog / Sheet / Drawer / Popover / Tooltip / DropdownMenu / ContextMenu / Command / HoverCard
- **数据展示与布局**：Card / Table / Avatar / Tabs / Accordion / Breadcrumb / ScrollArea / Separator / Empty
- **导航**：Pagination / Stepper / Sidebar / NavigationMenu / Menubar
- **表单**：Form / Field（FieldGroup / FieldLabel / FieldDescription）

不确定某个组件名时，看 `node_modules/shadcn-vue-cdn/dist-lib/index.d.ts` 的导出列表。

### 主题（preset）

`app.use(ShadcnVue, options)` 支持两种主题方式：

```ts
// 方式 A：preset 短码（在 https://www.shadcn-vue.com/create 自定义后复制）
createApp(App).use(ShadcnVue, { preset: 'a326wSQq' })

// 方式 B：直接传主题 CSS 字符串（优先级高于 preset）
createApp(App).use(ShadcnVue, { css: ':root { --primary: oklch(0.6 0.2 250); }' })
```

不传则用默认主题。相关工具函数 `presetToCss` / `presetConfigToCode` / `resolveConfig`、以及容器作用域换肤 `usePreset` 均可从 `shadcn-vue-cdn` 导入。

## 图标：@remixicon/vue

图标从 `@remixicon/vue` 导入，命名导出为 `Ri` 前缀 + 图标名（`Line` 描边 / `Fill` 实心两种风格）：
图标使用 tailwindcss size-* 不要使用 属性

```vue
<script setup lang="ts">
import { RiHeartFill, RiCloseLine } from '@remixicon/vue'
</script>

<template>
  <RiHeartFill size="16" color="currentColor" />
  <RiCloseLine size="24" />
</template>
```

- 图标名在 [remixicon.com](https://remixicon.com) 上查，点图标复制 Vue 组件名即可。
- props：`size`（默认 `"24px"`，字符串）、`color`（默认 `currentColor`）、`className`。
- 控制尺寸用 `size`，不要用 Tailwind 的 `size-*`（对组件不生效）。

## 开发规范

1. **有现成库就直接用，不要造轮子。** 需要某个能力先查 vueuse / vue-router / pinia / mitt 是否已覆盖，覆盖了就不要再手写。
2. **最小组件。** 每个组件只干自己的一件事；能拆的通用逻辑下沉到 `composables/`，不要把职责堆进一个 `.vue`。
3. **最小 hooks。** 组合式函数保持小而单一，优先复用 `@vueuse/core`。
4. **状态用 pinia**，不要用全局变量或到处传 props。
5. **路由用 vue-router**，页面级跳转不要手写 history。
6. **跨组件事件通知用 mitt**：建一个全局 emitter，`emit` / `on`，不用 Vue 的 `$emit` 跨层传或用事件总线自造。
7. **样式用语义色**：`bg-background` / `text-foreground` / `bg-primary` 等，不写裸色值（如 `bg-blue-500`）；间距用 `gap-*`，等宽高用 `size-*`。
