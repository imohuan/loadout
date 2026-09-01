# 多模态插件 · 上传层计划（upload.go）

任务概述：为多模态 MCP 插件实现大文件上传层。视频/音频等超过 base64 阈值的大文件，通过火山方舟 Files API 上传拿 `file_id`。从渠道列表按 Base URL 识别方舟平台取 key，上传后轮询文件状态到 `active`。

## 交付物
- `plugins/multimodal-mcp/upload.go`：`uploadAndGetID` 方法 + 渠道识别/取key/上传/轮询辅助函数。
- `plugins/multimodal-mcp/upload_test.go`：mock 火山 /v3/files 端点的单测。

## 实施步骤（每步可独立验证 + commit）
1. **渠道识别 + 取 key**：`repo.ListChannels(ctx)` 读 SQLite 渠道；`net/url` 解析 `ch.BaseURL` host，`.volces.com` 结尾即判定方舟；`st.Decrypt(ch.APIKeyCipher)` 解密 key；收集所有启用的方舟渠道（key, host）供 failover。→ commit
2. **上传**：`POST https://ark.cn-beijing.volces.com/api/v3/files`，multipart `purpose=user_data` + `file`（字节 + 文件名），`Authorization: Bearer <key>`；解析响应 `id`。多 key 依次尝试（failover）。→ commit
3. **轮询 active**：`GET .../files/{id}`，轮询到 `active`；60s 上限，间隔 1s；超时/失败返回明确 error。→ commit
4. **单测**：httptest.Server mock /v3/files 上传 + 检索 + active；覆盖成功、轮询、失败/超时路径。→ commit
5. **验证**：`gofmt -w` + `go build ./plugins/multimodal-mcp/` + `go test ./plugins/multimodal-mcp/`。

## 注意
- 不改 service.go 结构/签名；uploadAndGetID 若已存在占位则覆盖（当前无占位，新建）。
- 不碰 mcp-hub / vision_v2 / model-gateway。
