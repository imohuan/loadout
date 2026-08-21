# Aggregate Plugin（聚合模型插件）

## 功能

将多个真实模型组合成一个虚拟模型，按优先级自动切换：
- 第一个模型失败 → 自动换第二个
- 第二个也失败 → 换第三个
- 全部失败才报错

## 配置文件

位置：`~/.loadout/data/aggregates.json`

格式：

```json
[
  {
    "name": "auto",
    "targets": [
      {"model": "gpt-4", "channel_id": "ch-openai"},
      {"model": "claude-3-5-sonnet-20241022", "channel_id": "ch-anthropic"},
      {"model": "deepseek-chat", "channel_id": "ch-deepseek"}
    ]
  },
  {
    "name": "fast",
    "targets": [
      {"model": "gpt-3.5-turbo", "channel_id": "ch-openai"},
      {"model": "deepseek-chat", "channel_id": "ch-deepseek"}
    ]
  }
]
```

**说明：**
- `name`：虚拟模型名（用户请求时填这个）
- `targets`：真实模型列表，**数组顺序 = 优先级**（第一个优先）
  - `model`：真实模型名
  - `channel_id`：渠道 ID（必须在 `channels.json` 里存在且启用）

## 使用方式

1. 配置好 `channels.json`（渠道列表）
2. 写 `aggregates.json`（上面的格式）
3. 请求时用虚拟模型名：

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_GATEWAY_KEY" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

系统会：
1. 先用 `ch-openai` 的 `gpt-4` 发请求
2. 如果失败（非 2xx 状态码），换 `ch-anthropic` 的 `claude-3-5-sonnet-20241022`
3. 还失败就换 `ch-deepseek` 的 `deepseek-chat`
4. 全部失败才返回错误

## 查看可用模型

```bash
curl http://localhost:3000/v1/models \
  -H "Authorization: Bearer YOUR_GATEWAY_KEY"
```

会列出所有聚合模型（`auto`, `fast` 等）。

## 与其他插件的组合

aggregate 插件可以与 vision 等其他插件**无缝组合使用**：

```json
{
  "name": "auto",
  "targets": [
    {"model": "gpt-4", "channel_id": "ch-openai"},
    {"model": "claude-3-5-sonnet-20241022", "channel_id": "ch-anthropic"}
  ]
}
```

**工作流程**（用户发送带图片的请求到 `auto` 模型）：

1. **vision 插件先执行**：检测到图片 → 调用视觉模型生成描述 → 改写 `pipe.Messages`（把描述加进去）
2. **aggregate 插件后执行**：拿到改写后的 `pipe.Messages`（已包含图片描述）→ 按优先级轮询目标模型 → 用改写后的消息转发 → 成功返回

插件的执行顺序由 `plugins/registry.go` 的 `All()` 列表顺序决定（vision 在 aggregate 前面）。

## 文件结构

```
plugins/aggregate/
├── plugin.go           # 插件注册和装配
├── service.go          # 核心逻辑（拦截请求、轮询转发）
├── aggregate_test.go   # 单元测试
└── README.md           # 本文档
```
