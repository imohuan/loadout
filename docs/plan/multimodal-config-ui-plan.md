# 多模态配置页前端计划

## 任务概述
在设置界面新建「多模态」tab，配置多模态 MCP 端点的总开关、3 个工具（图片/视频/音频）的启用 + 内置模型名 + 默认参数。对接后端 `GET/PUT /api/multimodal/config`。

## 改动文件
| 文件 | 改动 |
|---|---|
| `frontend/src/views/MultimodalView.vue` | 新建：配置页（总开关 + 3 工具卡片 + 保存/加载） |
| `frontend/src/views/ManagementView.vue` | 加 `<TabsTrigger value="multimodal">` + `<TabsContent value="multimodal">` |

## 实施步骤
1. 新建 `MultimodalView.vue`：定义 `MultimodalConfig`/`ToolConfig` interface；用 `api`/`request` 调 `GET/PUT /api/multimodal/config`；总开关 Switch；3 工具卡片（启用开关 + 模型名 Input + 各 kind 默认参数：image detail select、video fps number、audio task/language/source_lang/target_lang）；保存按钮走 `useAsyncTask`。
2. 在 `ManagementView.vue` 加 tab（引用并嵌入 `<MultimodalView />`）。
3. 构建验证：`cd frontend && pnpm build`。

## 测试方案
- `pnpm build` 通过。
- 手动验证：GET 回填、修改保存后 PUT 落盘。

## 风险
- 后端 API 字段以 `plugins/multimodal-mcp/config.go` 的 json tag 为准（已核对）。
