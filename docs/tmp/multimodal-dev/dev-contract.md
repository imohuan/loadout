# 多模态 MCP 插件 · 开发契约（子代理公共参考）

本契约定义各子代理并行开发时共享的接口与数据结构，避免冲突。所有后端代码在 `plugins/multimodal-mcp/` 包内，包名 `multimodalmcp`，module `loadout/plugins/multimodal-mcp`。

## 一、公共数据结构（定义在 config.go / service.go）

```go
// config.go
// ToolKind 工具类型：图片 / 视频 / 音频
type ToolKind string
const (
    ToolImage ToolKind = "image"
    ToolVideo ToolKind = "video"
    ToolAudio ToolKind = "audio"
)

// ToolConfig 单个工具的配置：启用 + 内置模型名 + 默认参数。
type ToolConfig struct {
    Kind     ToolKind       `json:"kind"`
    Enabled  bool           `json:"enabled"`
    Model    string         `json:"model"`    // 内置模型名（请求体 model 字段，不参与渠道匹配）
    Defaults map[string]any `json:"defaults,omitempty"` // 图片: detail; 视频: fps; 音频: task/language 等
}

// MultimodalConfig 多模态插件整体配置（store JSON 落盘）。
type MultimodalConfig struct {
    Enabled   bool         `json:"enabled"`    // 端点总开关
    Tools     []ToolConfig `json:"tools"`      // 3 个工具配置
    // 音频 task 的 instructions 模板可内置在 prompt.go，不存配置（除非需要用户覆盖）
}
```

```go
// service.go
type Service struct {
    st    *store.Store
    lg    *slog.Logger
    repo  *db.Repository
    gw    modelgateway.SubRequestForwarder // = modelgateway.Service，见 vision_v2 用法
    route contracts.RouteLog
    mu    sync.Mutex
}
func NewService(st *store.Store, repo *db.Repository, lg *slog.Logger) *Service
func (s *Service) SetGateway(gw modelgateway.SubRequestForwarder)
func (s *Service) SetRouteLog(rl contracts.RouteLog)
```

## 二、model-gateway 子请求通道（识别用，勿裸调 HandleProxy）

识别请求通过 `ForwardSubRequest` 走主链路。签名（modelgateway）：

```go
// modelgateway.Service 上：
func (s *Service) ForwardSubRequest(ctx context.Context, pipe *ProxyPipeline, streamWriter func(line []byte) error) (*ProxyPipeline, []byte, error)

type ProxyPipeline struct {
    RequestID string
    Request   *ProxyRequest
    ResponseWriter http.ResponseWriter
    StreamWriter   func(delta string) error
    HTTPRequest    *http.Request
    Metadata       map[string]any
}
type ProxyRequest struct {
    Method string
    Path   string   // 上游路径：chat/completions 或 responses，经 /v1/{path} 透传
    Query  string
    Header http.Header
    Body   []byte   // 完整请求体 JSON
    Model  string
    Stream bool
}
```

关键约定（vision_v2/tool_loop.go 已用，见 plugins/vision_v2/tool_loop.go continuationViaGateway）：
- 构造 `ProxyPipeline{Request:&ProxyRequest{Method:"POST", Path:"chat/completions" 或 "responses", Body:payload, Model:内置模型名, Stream:true/false}, Metadata:map[string]any{}}`。
- `ForwardSubRequest` 会自动打 `__sub_request` / `__sub_request_skip_security`，安检跳过、防递归。
- streamWriter 非 nil = 流式逐行回调；nil = 返回完整 body。多模态 MCP 工具 Handler 是同步返回 `*ToolResult` 的，**用非流式（streamWriter=nil）拿完整 body 再组装 ToolResult**。
- **识别结果 = 上游 body**：chat/completions 取 `choices[0].message.content`；responses 取 `output` 文本。由各识别函数解析。

## 三、图片/视频/音频的请求协议

- **图片/视频**：Path=`"chat/completions"`，payload 为 OpenAI chat 格式。
  - 图片块：`{"type":"image_url","image_url":{"url":"..."}}`（url 或 data URI），`detail` 参数放 image_url 里。
  - 视频块：`{"type":"video_url","video_url":{"url":"...", "fps":2}}`。
  - 文本：`{"type":"text","text":"..."}`。顶层 `{"model":"...","messages":[{"role":"user","content":[块...]}],"stream":false}`。
- **音频**：Path=`"responses"`，payload 为 OpenAI responses 格式。
  - 顶层 `{"model":"...","input":[{"role":"user","content":[{"type":"input_audio","audio_url":"..."},{"type":"input_text","text":"..."}]}],"stream":false,"instructions":"..."}`。
  - `instructions` 是音频 task 的提示词模板（prompt.go 提供，按 task 选）。
- **file_id 传入**：图片/视频 chat 用 `{"type":"image_url"/"video_url","...":{"url":fileID}}` 不支持——**file_id 用于 responses 协议的 `{"type":"input_video","file_id":"..."}` / `{"type":"input_audio","file_id":"..."}`**。首版大文件走 URL/base64（见下方决策），file_id 场景需在识别层判断。

## 四、上传取 key（upload.go）

- **渠道读取**：用 `repo.ListChannels(ctx)`（SQLite，与 model-gateway 主链路同源），**不用 store.Read(FileChannels)**（JSON 双轨风险）。
- **识别方舟**：按 `ch.BaseURL` 解析 host，判断是否方舟（`ark.cn-beijing.volces.com` 等）。**不要只用 NormalizeBaseURL**（仅去尾斜杠），需自行解析 URL host。
- **取 key**：`store.Decrypt(ch.APIKeyCipher)`（st 由插件注入）。
- **上传**：`POST https://ark.cn-beijing.volces.com/api/v3/files`，multipart：`purpose=user_data` + `file=@path`，`Authorization: Bearer <key>`。
- **轮询**：轮询文件状态到 `active` 拿 file_id；设超时上限；失败/超时降级（报错或退回 base64/URL）。

## 五、MCP server 与工具（server.go / tools.go）

```go
// 构建 mcp server。
srv := mcpkit.NewServer("multimodal", []mcpkit.ServerTool{{
    Name: "understand_image", Description: "...", InputSchema: {...},
    Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {...},
}, ...})
```

### MCP server 挂 HTTP 传输（关键机制，已查证）
- MCP 库：`github.com/modelcontextprotocol/go-sdk/mcp`。
- servercore 里已有 `/mcp/` 前缀的统一分发器（core/servercore/server.go:125）：
  `mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { ... }, nil)` 挂 `mux.Handle("/mcp/", keys.MCPKeyMiddleware(mcpHandler))`。它按 URL path 从 mcp-hub 拿 server。
- **多模态自建端点方案**：插件用 `ctx.RegisterRoute` 注册精确路由 `POST /mcp/multimodal`（`Auth: plugin.AuthMCPHeader`），Handler 自己构建：
  `srv := mcpkit.NewServer("multimodal", tools)` 然后 `handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return srv }, nil)`。
  **Go http.ServeMux 精确路径 `/mcp/multimodal` 优先于前缀 `/mcp/`，不会冲突**。这使多模态独立于 mcp-hub。
- 若精确路由与 `/mcp/` 前缀冲突（需验证），备选：改造 servercore 的 getServer 分发函数，对 `/mcp/multimodal` 返回多模态 server。

### mcpkit 关键类型（core/mcpkit/mcpkit.go）
- `NewServer(name string, tools []ServerTool) *mcp.Server`（mcpkit.go:453）
- `ServerTool{Name, Description string; InputSchema map[string]any; Handler func(ctx context.Context, args map[string]any) (*ToolResult, error)}`（mcpkit.go:440）
- `ToolResult{Content []ContentPart; IsError bool}`（mcpkit.go:49）
- 库导入：`"github.com/modelcontextprotocol/go-sdk/mcp"`

## 六、配置路由（config.go）

- `GET /api/multimodal/config`（AuthSession）→ 返回 `MultimodalConfig`。
- `PUT /api/multimodal/config`（AuthSession）→ 保存。
- 读写经 `store`（JSON 文件，如 `multimodal_config.json`）。

## 七、前端（MultimodalView.vue + ManagementView.vue）

- `ManagementView.vue` 的 `<Tabs>` 加一个 `<TabsTrigger value="multimodal">多模态</TabsTrigger>` + `<TabsContent value="multimodal">`，嵌入 `<MultimodalView />`。
- `MultimodalView.vue`：端点总开关、3 个工具（图片/视频/音频）启用 + 内置模型名输入 + 默认参数（图片 detail、视频 fps、音频 task/language），保存按钮调 `PUT /api/multimodal/config`，加载调 `GET`。
- 复用现有 Card/Table/Button/Input 组件与 `useAsyncTask`、toast。

## 八、禁止事项

- **不**裸调 `HandleProxy`；**不**用 `resolveTestTarget`；**不**新建密钥体系。
- 所有改动收敛在 `plugins/multimodal-mcp/` 与前端多模态 tab，不碰 mcp-hub / vision_v2 / model-gateway 现有逻辑。
- 每个子代理只碰自己负责的文件，避免交叉覆盖。
