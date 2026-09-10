# 调查：「同步时获取数据」后数据预览里 OpenCodex 模型为 0，刷新元数据才有效

日期：2026-09-10
结论性质：链路已查清 + 修复已落地并验证

## 一、现象

UnifyAI 页面里，不点「更新元数据」直接进页面（走 `--list all` 同步取数）时，
「数据预览 → OpenCodex 模型」显示 0 个；点了「更新元数据」之后才正常。

## 二、实测（本机真实 CLI 输出）

`npx unifyai@latest --list all --json`：

```json
"metadata": { "modelCount": 431, "cachedAt": "2026-09-09T09:03:03Z" },
"models": {
  "rawCount": 0,
  "degraded": true,
  "degradedReason": "OpenCodex 代理服务不可用（请先启动，或检查 localhost:${port}）",
  "models": [],
  "count": 0,
  "orMatchedCount": 0,
  "orTotal": 431
}
```

要点：

1. 代理不可达时 `models: []` —— 这是 opencodex 代理没起来时的**正常降级**。
2. 但同一时刻元数据缓存是**有的**（431 条，9-09 刷新过）。
3. `orTotal: 431` 说明 CLI 自己拿缓存做过名字匹配、缓存可用；`orMatchedCount: 0`
   是因为压根没有模型可匹配（列表为空），不是匹配失败。
4. 「更新元数据」之后能显示，是因为那一刻代理起来了 / 重新探测到了模型。

## 三、发现的两个真 Bug

### Bug 1（主因）：点过「更新元数据」之后，同步取数就拿不到模型了

代理不可达时，CLI 会**回退到本地 openrouter-models.json 生成模型列表**
（否则不会白算 `orTotal`）。它按模型名回填字段，用的键名从压缩后的元数据条目里读：

```js
m.context ?? m.context_window ?? m.context_length   // CLI 的读法
```

而 CLI 自己写缓存时写的是**压缩命名**：

```js
{ id, name, context, output, vision, reasoning }     // 后端 ModelSource 就是这么解析的
```

两边对不上 → 匹配到的模型 `context` 为 0 → CLI **把匹配失败的模型全部丢弃**
→ `models: []`、`count: 0`。

这就解释了为什么「刷新元数据之后反而好了」以及用户看到的「只有刷新元数据才有效」：
缓存从 openrouter.ai 原生格式（`context_length`，能被 CLI 认出来）被 CLI 刷新成压缩格式后，
同步取数这条路就废了。

### Bug 2（次要）：前端用 CLI 的 `count` 字段做展示

「数据预览」的模型数直接用 `opencodexModels.count`，而 CLI 只往
`models.models` 里塞数据、没设 `count` → 即使列表有值也会显示「0 个模型」。

## 四、修复

### 后端 `plugins/unifyai/service.go`

`OpenCodexModels` 在返回前调 `enrichFromOpenRouter`：代理没给上下文窗口时，
用**后端自己解析**本地元数据缓存（字段名由 `ModelSource` 保证与 CLI 写的一致），
按「完整 id」与「裸模型名」两级匹配补回 `contextWindow`。
让「同步取数」与「刷新元数据」两条路得到同样的结果。

浏览器直接打开 openrouter（`https://openrouter.ai/api/v1/models`）永远给的是原生字段名
（`context_length` / `architecture.input_modalities` / `supported_parameters`），
所以从浏览器侧刷新出来的缓存必然能被 CLI 的匹配逻辑认出来——那条路是稳的。

### 前端

- `normalizeOpenCodexModels`（`frontend/src/lib/unifyai.ts`）：`count` 缺失时用
  `models.length` 兜底，`fetchOpenCodexModels` 与 `UnifyaiPanel` 初始化都走它。
- 新增诊断接口 `GET /api/unifyai/opencodex-models-live`：原样透传 CLI 输出
  （压缩命名），用于对比 CLI 写的字段名与 UI 期望的字段名。

### 诊断样例（实测数据）

```
metadata_enrich_real_test.go : 缓存条目 431（裸名索引 348）
deepseek-v4-flash → context 1048576
glm-5.2           → context 1048576
```

真实渠道库（`Loadout/<模型名>`）覆盖率：命中 4/7，未命中
`claude-haiku-4-5-20251001`（OpenRouter 里是 `-20251001` 快照命名，裸名不带日期，匹配不到）、
`deepseek-auto` / `deepseek-v4-flash-ga-260731`（厂商内部名，OpenRouter 里没有）。
这三条属于元数据里就没有，界面会保留空白上下文而不是误填——比错填安全。

## 五、遗留 / 未做

- **没改 CLI（opencodex / unifyai 包）**：写缓存与读缓存字段名不一致是 CLI 自身的
  bug（写 `context`、读 `context_length`），属于上游仓库，本次只在前端桥接层兜住。
  要根治得让 CLI 用 openrouter.ai 的原生字段名写缓存。
- **`--list all` 路径的 `count` 字段同样缺失**：前端已兜底，但 CLI 侧没补。
