# 多模态插件新增「文档理解」工具（understand_document）计划

## 任务概述
在 `plugins/multimodal-mcp` 新增第 4 个 MCP 工具 `understand_document`（文档/PDF 理解），
严格参考现有 `understand_image` / `understand_video` / `understand_audio` 的实现方式，
走火山方舟 Responses API 的 `input_file` 能力（参考 `docs/archive/火山 文档理解.md`）。

## 能力（来自火山文档理解文档）
- 支持模型：走视觉理解能力的模型，如 `doubao-seed-2-1-pro-260628`（Responses API）。
- 文档输入三种方式：
  1. **Files API 上传（推荐）**：本地大文件 → `POST /v3/files` 拿 `file_id` → Responses 里 `{"type":"input_file","file_id":...}`。≤512MB。
  2. **Base64 传入**：本地小文件（<50MB，请求体<64MB）→ `{"type":"input_file","file_data":"data:application/pdf;base64,...","filename":"x.pdf"}`。
  3. **文件 URL 传入**：公网 URL → `{"type":"input_file","file_url":"..."}`。≤50MB。
- 输出：模型返回文档分段理解文本（段落/标题/内容等）。

## 改动文件
| 文件 | 改动 |
|---|---|
| `plugins/multimodal-mcp/tools.go` | 新增 `understand_document` 工具定义 + schema |
| `plugins/multimodal-mcp/document.go` | 新增 `understandDocument` 实现（资源解析 + Responses input_file 构造 + 识别）|
| `plugins/multimodal-mcp/call.go` | 新增 `inputFileBlockResponses` 构造 input_file 块；`mimeByExt` 加 `.pdf`；加 `documentSizeLimit` |
| `plugins/multimodal-mcp/config.go` | `ToolKind` 加 `ToolDocument`；`DefaultConfig` 加第 4 个工具 |
| `plugins/multimodal-mcp/prompt.go` | 新增 `defaultDocumentPrompt` |
| 测试 | `document_test.go` 新增：URL / base64 小文件 / file_id 大文件 / schema |

## 实施步骤
1. `config.go`：ToolKind 加 `ToolDocument`，DefaultConfig 加 `{Kind: ToolDocument, Enabled: true, Defaults:{...}}`。
2. `call.go`：新增 `inputFileBlockResponses`（file_id / file_data+filename / file_url 三态）；`mimeByExt` 加 `.pdf` → `application/pdf`；加 `documentSizeLimit = 50MB`（base64 上限，Files API 走 file_id）。
3. `document.go`：`understandDocument` — 校验端点启用 → 取模型 → resolveResource（本地文件 base64 或 file_id，URL 原样）→ 构造 `input_file` 块 + text 块 → `callResponses` → `runRecognition` 写 route-log。
4. `tools.go`：注册 `understand_document` 工具 + schema（document/prompt，required document）。
5. `prompt.go`：`defaultDocumentPrompt`。
6. 测试 + 前端无改动（工具自动经 `$smart` 聚合 + `/mcp/multimodal` 暴露）。
7. `go build ./...` + `go test ./plugins/multimodal-mcp/...` 验证，commit。

## 完成状态
✅ 已实现并提交（见 `plugins/multimodal-mcp/` 新增 document.go + tools.go/config.go/call.go/prompt.go 改动）。
✅ 新增 `document_test.go`（input_file 三态 / URL / 本地 base64 / 缺模型 4 个用例）。
✅ `go build ./...` + `go test ./plugins/multimodal-mcp/... ./plugins/mcp-hub/... ./plugins/admin-api/...` 全通过。
✅ 前端无改动（工具自动经 `$smart` 聚合 + `/mcp/multimodal` 暴露）。
⚠️ 待重启后端生效，并在设置页给「文档」工具配一个支持文档理解的模型（默认 doubao-seed-2-1-pro-260628）。

## 关键实现点
- 本地文件：`resolveResource(ctx, ref, "application/pdf", documentSizeLimit)`，与图片一致（返回 data URI 或 file_id）。注意 resolveResource 大文件走 `uploadAndGetID`（Files API）→ 已实现，返回 file_id。
- 大文件拿到 file_id → Responses `{"type":"input_file","file_id":...}`（Responses 支持 file_id，与图片/视频的 chat 协议不同）。
- 小文件 → data URI → 剥成纯 base64 拼 `file_data` + `filename`。
- URL → `file_url`。
- 走 `callResponses`（Responses API，路径 `responses`），解析 `output[].content[].output_text`。
- route-log：`runRecognition(ctx, "understand_document", "文档理解", model, meta, ...)`。

## 风险与注意
- 需要配一个支持文档理解的模型（默认 doubao-seed-2-1-pro-260628），否则模型不支持 `input_file`。
- documentSizeLimit 50MB 是 base64 上限；超限走 Files API（512MB）。
- resolveResource 对本地文件已按内容探测 mime（PDF magic `%PDF`），但 `.pdf` 扩展名可靠，加进 mimeByExt 即可。
