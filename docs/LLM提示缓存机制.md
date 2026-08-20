# LLM 提示缓存机制详解

> 结论先行：**市面上不存在统一的"缓存字段"**。所有 LLM 提供商的提示缓存（prompt caching）底层原理一致——**token 前缀精确匹配**，但"是否需要客户端显式传字段"分为三种做法。

---

## 1. 通用原理

LLM 本身无状态，每次调用必须把完整上下文重放进请求。服务端对请求做如下处理：

```
请求（system + 全部历史轮次）
        │
        ▼
token 化 + 前缀哈希 ──→ 缓存键 = 前缀 token 序列本身
        │
        ▼
     查缓存索引
      /      \
    命中      未命中
     │          │
     ▼          ▼
 复用 KV 状态   全量计算
 只算新增部分   并写入缓存
```

关键点：

- **缓存键不是客户端传的 ID，而是请求内容本身的前缀哈希**。服务端把请求前 N 个 token 做哈希，去查缓存索引。
- 第一次请求：服务端自动把该前缀的 KV 状态写入缓存，客户端**无需任何操作**。
- 第二次请求：**前缀逐 token 一致**则命中，不一致则 miss。
- 命中的部分不再重复计算，只按新增 token 计费；缓存是省钱 + 降延迟的手段，不是状态绑定手段。

---

## 2. 主流厂商机制对比

| 机制 / 厂商 | 判断缓存的"字段" | 创建 / 命中的观测点 |
|---|---|---|
| **自动前缀缓存**<br>OpenAI · DeepSeek · 通义 · Kimi | 不需要传任何字段。缓存键 = 前缀 token 序列本身，服务端自动哈希匹配。历史必须原封不动。 | `usage.prompt_tokens_details.cached_tokens`<br>`prompt_cache_hit_tokens` |
| **cache_control 断点**<br>Anthropic Claude | 请求内给 system 或大文档块加 `cache_control: {"type": "ephemeral"}`，断点之后的块进入缓存。后续请求不传 ID，照样前缀匹配。 | `cache_creation_input_tokens`（写入）<br>`cache_read_input_tokens`（命中） |
| **缓存句柄**<br>Google Gemini | 显式传 `cachedContent: "cachedContents/xxx"`，指向预先创建的缓存对象。 | 独立 CachedContent API，可配置 TTL |

---

## 3. 各家细节

### 3.1 OpenAI / DeepSeek / 通义 / Kimi —— 全自动，什么都不传

- 服务端自动按前缀匹配，缓存键就是 token 化的前缀本身。
- 响应中的 usage 字段（`cached_tokens`、`prompt_cache_hit_tokens`）只是**报告**，不是开关——不能主动关，也不能手动指定。
- 前提条件：
  - 前缀 ≥ 最小长度（OpenAI 约 1024 token）。
  - TTL 内：OpenAI 约 5 分钟滑动窗口；DeepSeek 为盘上缓存，可撑数小时。

### 3.2 Anthropic Claude —— 用 cache_control 标记断点

- 请求里给某条消息（通常是 system prompt 或大文档）加 `cache_control: {"type": "ephemeral"}`，告诉服务端"从这个断点开始要缓存"。
- 后续请求**不传任何 ID**，依旧是前缀匹配。
- 通过响应中的 `cache_creation_input_tokens`（第一次写入）和 `cache_read_input_tokens`（命中读取）观察效果。

### 3.3 Google Gemini —— 显式句柄，最接近"传字段"的模型

- 先调用独立的 CachedContent API 创建缓存对象，返回一个名字（如 `cachedContents/xxx`）。
- 之后每次请求在 `cachedContent` 字段传入该句柄指向缓存。
- 这是唯一一种"第二次请求真的显式传字段"的实现。

---

## 4. 对 Agent 平台的启示

- **"绑定上一次结果"的真相是重放，不是绑定**：Agent（无论哪个 CLI 平台）每次把完整对话拼进请求，因为 LLM 无状态，上下文必须每次重发。
- **命中缓存只取决于一件事：前缀是否逐 token 原封不动**。
- 会打穿缓存的常见操作：
  - 修改 system prompt 措辞；
  - 升级模型版本（tokenizer 变化导致前缀哈希全变）；
  - 压缩 / 重写历史消息；
  - 调整消息顺序或格式（如插入 system-reminder 类内容）。
- 想要最大化命中率，唯一能做的就是**保持前缀稳定**：system prompt 固定、历史按原样追加、不重写。

---

## 5. 一句话总结

- 自动前缀匹配（OpenAI 系）：不传字段，前缀即缓存键。
- 断点标记（Claude）：请求内加 `cache_control`，命中也靠前缀。
- 显式句柄（Gemini）：先建缓存，再传 `cachedContent` 字段。
- 对 Agent 而言：**缓存是省钱手段，不是状态绑定手段**；状态绑定靠的是把历史写进 prompt 本身。
