# 视觉历史图片去重（影子消除）实施计划

> 状态：待执行
> 目标：解决多轮对话中历史图片被每轮请求重复检出、重复识别、重复输出「图片理解」，导致对话里全是图片识别影子、视觉模型被反复调用的三个叠加问题。

---

## 1. 问题与根因

### 1.1 现象

客户端（Cherry Studio / NextChat 类）多轮对话自动携带完整历史，历史里第一轮的图片消息**每轮请求都会原样发来**。视觉插件对历史旧图和最新图一视同仁，于是每轮请求都重走一遍视觉流程：

1. **重复花钱**：md5 缓存 miss 时（TTL 过期、图片 URL 变化、base64 data URI 每次编码不同），同一张图被识别 N 次。
2. **影子直接来源**：`HandleProxyBeforeUpstream` 只要检出图片，流式请求就注入 `> **图片理解** : ` 前缀（`plugins/vision/proxy.go:259-270`）；**缓存命中时也照样输出**（`plugins/vision/service.go:111-113` 的 `_ = streamWriter(text)`）。客户端把这段「图片理解」当普通回复内容存进历史，第 2 轮起对话里全是它的影子。
3. **上下文牵引**：历史图片的完整描述每轮都注入主模型，模型输出被图片内容主导——问别的，它还在答图。

### 1.2 根因

网关是无状态的，无法天然区分「历史旧图」与「本轮新图」。`detectProxyImages`（`proxy.go:68`）遍历**所有** messages，未按消息位置分级，全部走完整视觉流程：

```
HandleProxyBeforeUpstream（vision/proxy.go:200）
  ├─ detectProxyImages 遍历全部 messages（含历史旧图）
  ├─ DecideRoute 查能力路由
  └─ Describe → callVision
       ├─ md5 缓存 miss → 调视觉模型（重复花钱）
       └─ streamWriter 输出「图片理解」前缀 + 描述（缓存命中也输出 → 影子）
```

### 1.3 业界对照

| 路线 | 做法 | 结论 |
|---|---|---|
| 主流（OpenRouter / LiteLLM / one-api） | 原生视觉透传，不生成描述文本 | 无「影子」问题，模型自己管多轮图片 |
| 少数派（本项目的 caption proxy） | 用视觉模型生成描述替换图片 | **业界共识纪律：只处理新图，历史图只做幂等降级**（image→text 缓存已有，位置感知缺失） |

本计划即补齐缺失的「位置感知」纪律，md5 缓存继续保留。

---

## 2. 需求确认（已与用户对齐）

| 决策点 | 结论 |
|---|---|
| 新图判定 | **最后一条 user 消息**中的图片 = 新图；其余 = 历史旧图 |
| 新图处理 | 走完整识别（md5 缓存优先 → miss 调视觉模型 → 写缓存），流式输出「图片理解」 |
| 历史旧图·缓存命中 | 用缓存描述替换（不调视觉模型、不输出流式「图片理解」） |
| 历史旧图·缓存 miss | **不调视觉模型**，替换为轻量占位符 `[图片]` |
| 流式「图片理解」输出 | **仅新图场景**输出一次；纯旧图请求完全不输出 |
| 配置项 | `VisionHistoryMode`：`cache`（默认）/ `placeholder` / `keep`（现状回退） |

效果：每张图最多识别 1 次；第 2 轮起对话流不再出现「图片理解」影子；主模型上下文干净（历史描述只保留一份，不每轮重新注入）。

代价（可接受）：第 2 轮起主模型看不到历史图片细节——第一轮 assistant 回复已含图片内容，通常够用；placeholder 模式可进一步省 token。

---

## 3. 方案总览

按**消息位置**把图片分为两组，分级处理：

```
messages[]
├─ 最后一条 role=user 的消息 → 图片 = 新图组（new）
│    └─ Describe（缓存优先 → 识别 → 写缓存）＋ streamWriter 输出「图片理解」
└─ 其余消息 → 图片 = 旧图组（old，按消息分组）
     ├─ 缓存命中（该组图片 md5 key）→ 缓存描述替换
     └─ 缓存 miss → `[图片]` 占位符（不调视觉模型、不输出流式）
```

`VisionHistoryMode` 控制旧图策略：
- `cache`（默认）：如上，缓存描述替换，miss 占位符
- `placeholder`：旧图一律 `[图片]` 占位符（最省 token，不读缓存）
- `keep`：全部按现状处理（旧图也识别、也输出），兼容回退

新图行为三种模式不变，始终完整识别。

---

## 4. 实施细节

### 4.1 config：新增 VisionHistoryMode

文件：`core/config/config.go`（现有 `VisionCacheEnabled`/`VisionCacheTTLHours` 附近）

```go
// VisionHistoryMode 历史消息中旧图的处理策略：
// cache（默认）：缓存描述替换，miss 用占位符；
// placeholder：一律占位符；
// keep：保持现状（全部识别，旧行为）。
VisionHistoryMode = getEnv("LOADOUT_VISION_HISTORY_MODE", "cache")
```

非法值（非 cache/placeholder/keep）回退 `cache`，在 vision 插件读取处做校验，config 层只管透传。

### 4.2 vision：位置感知检出

文件：`plugins/vision/proxy.go`

**改动 1**：`detectProxyImages` 记录每条消息的 role 与格式感知的角色提取。

新增角色提取（三种格式统一）：

```go
// proxyMessageRole 提取消息角色：chat/claude 取 msg["role"]；
// responses 的 input item 可能是 {"type":"message","role":"user",...} 或 {"role":"user",...}。
func proxyMessageRole(msg map[string]any, format visionProxyFormat) string
```

`proxyImageHit` 增加 `role string` 字段；`detectProxyImages` 在命中图片块时写入该消息的 role。

**改动 2**：处理前计算 `lastUserIdx = 最后一个 role=="user" 的消息索引`（遍历 messages 逆序找）。若 messages 最后不是 user（异常/工具结果结尾），则无新图，全部按旧图处理。

### 4.3 vision：分级替换与流式控制

文件：`plugins/vision/proxy.go`（`HandleProxyBeforeUpstream`）＋ `plugins/vision/service.go`

**service 层新增历史图解析（只读缓存，绝不调视觉模型）**：

```go
// historyPlaceholder 历史旧图缓存 miss 时的占位文本。
const historyPlaceholder = "[图片]"

// resolveHistoryImages 解析一组历史图片：缓存命中返回描述，miss 返回占位符。
// 只读缓存，不触发视觉模型调用；key 与该组图片在 Describe 中的一致。
func (s *Service) resolveHistoryImages(images []string, viaModel string) string {
    if config.VisionHistoryMode == "placeholder" {
        return historyPlaceholder
    }
    key := md5Hex(strings.Join(images, "\n") + "|" + viaModel + "|" + visionCacheVersion)
    if config.VisionCacheEnabled {
        if text, ok := s.readCache(key); ok {
            return text
        }
    }
    return historyPlaceholder
}
```

> 缓存 key 与 `Describe` 完全一致（`service.go:108`），保证「第一轮新图写入的缓存，第二轮旧图能命中」。

**proxy 层分级流程**（替换现有 `HandleProxyBeforeUpstream` 中 `rewriteProxyImages` 之后的逻辑）：

```go
newHits, oldGroups := splitImageHits(hits, messages, format, lastUserIdx)
// oldGroups: 按消息分组的旧图组 []imageGroup{{images, hits, text:""}}

// 旧图组先解析（纯内存/磁盘，不联网）
for i := range oldGroups {
    oldGroups[i].text = s.resolveHistoryImages(oldGroups[i].images, viaModel)
}

// 新图走完整 Describe；只有存在新图才启用 streamWriter（输出「图片理解」）
var streamWriterOut func(string) error
if len(newHits) > 0 && pipe.StreamWriter != nil {
    streamWriterOut = 带 visionStreamPrefix 前缀的封装（现 proxy.go:257-270 逻辑）
}

// 多图合并：新图全部合并为一次 Describe（多张图一个 key，现行为）
text, err := s.Describe(ctx, newImages, viaModel, opt.ChannelID, streamWriterOut)

// 逐组改写：新图组替换为识别文本；旧图组替换为各自 text（描述或占位符）
rewriteProxyImagesByGroup(messages, format, newGroup, text)
for _, g := range oldGroups {
    rewriteProxyImagesByGroup(messages, format, g, g.text)
}
```

`rewriteProxyImagesByGroup` 复用现有 `rewriteProxyImages` 的单组语义（第一张图替换为文本、其余删除，`proxy.go:158-184`），改为接收一组 hits 而不是全量 hits。

**关键行为变化**：
- 纯旧图请求（用户第 2 轮只发文字）：不调视觉模型、不输出「图片理解」——影子与重复花钱同时消除。
- 新图缓存命中：仍输出一次描述（客户端第一轮没看过），保留现状 `service.go:111-113` 行为。
- `keep` 模式：`lastUserIdx` 不生效，全部视为新图走 `Describe`，与现状完全一致（回归兼容）。

### 4.4 边界情况

| 场景 | 行为 |
|---|---|
| messages 最后一条不是 user | 无新图，全部按旧图处理 |
| 同一消息多张旧图 | 该组一个缓存 key，命中合并为一段描述，miss 一个占位符（不拆成 N 个） |
| data URI 每次编码不同 | 旧图缓存 miss → 占位符，**不再重复识别**（本计划核心收益） |
| 缓存 TTL 过期 | 旧图 miss → 占位符，不重新识别；新图正常识别并重写缓存 |
| `VisionMaxImages` | 只约束新图（旧图不进 `callVision`，天然不受限） |
| 三种格式（chat/claude/responses） | 角色提取走 `proxyMessageRole` 分支；替换块类型沿用 `textBlockType` |

---

## 5. 改动文件清单

| 文件 | 改动 |
|---|---|
| `core/config/config.go` | 新增 `VisionHistoryMode`（默认 `cache`，环境变量 `LOADOUT_VISION_HISTORY_MODE`） |
| `plugins/vision/proxy.go` | `proxyMessageRole` 新增；`proxyImageHit` 加 role；`HandleProxyBeforeUpstream` 分级流程；`rewriteProxyImagesByGroup`；流式前缀仅新图 |
| `plugins/vision/service.go` | 新增 `resolveHistoryImages` + `historyPlaceholder`；`Describe` 不变（缓存命中流式输出保留） |
| `plugins/vision/proxy_test.go` | 新用例（见 §6） |
| `docs/IMPLEMENTATION.md` | 补一段「历史图片分级处理」说明（可选，随实现提交） |

`vision_compress_integration_test.go` 等现有测试不动，`keep` 模式保证兼容。

---

## 6. 测试计划

| 层 | 用例 |
|---|---|
| proxy | `TestRewriteHistoryOnly`：messages 只有历史图（最后一条 user 无图）→ Describe 调用计数 0（mock），旧图替换为缓存描述或 `[图片]` |
| proxy | `TestRewriteLatestOnly`：只有新图 → 正常识别（计数 1） |
| proxy | `TestRewriteMixed`：历史图 + 新图 → 新图识别 1 次，历史图用缓存/占位符 |
| proxy | `TestStreamPrefixOnlyForNew`：纯旧图流式请求不输出 `visionStreamPrefix`；新图请求输出且仅一次 |
| proxy | `TestKeepModeUnchanged`：`keep` 模式行为与现状一致（旧图也识别） |
| service | `TestResolveHistoryCacheHit`：缓存命中返回描述，不写缓存 |
| service | `TestResolveHistoryCacheMiss`：返回 `[图片]`，不调视觉模型 |
| config | `VisionHistoryMode` 默认 `cache`；非法值回退 |
| 回归 | 现有 `go test ./...` 全绿（`keep` 兼容 + 既有用例不动） |

---

## 7. 任务分解（执行顺序）

1. **计划文档**（本文件）
2. **config**：`VisionHistoryMode` → commit
3. **service**：`resolveHistoryImages`（TDD：先写失败测试 → 实现 → 跑测试）→ commit
4. **proxy**：位置感知检出 + 分级替换 + 流式控制（TDD，核心）→ commit
5. **全量验证**：`go test ./...`、`go vet ./...`、`go build ./...`
6. **文档**：`docs/IMPLEMENTATION.md` 补历史图片分级处理说明

---

## 8. 风险与备注

- **判定依赖「最后一条 user 消息」**：客户端若连续多条 user 消息（中间无 assistant），取最后一条，行为仍正确；极端场景下新图判定可能误伤，但 OpenAI 兼容协议无更可靠的会话信号，可接受。
- **旧图缓存 miss 后主模型看不到图**：第一轮回复已含图片内容，通常够用；对「追问图片细节」强需求的用户可切 `placeholder`/`cache` 观察，若仍不够再评估「历史图 miss 时也识别」的开关（本期不做，YAGNI）。
- **与视觉日志落库计划（`docs/VISION-ROUTE-LOG-PLAN.md`）独立**：该计划记 `step_no=-1` 的视觉 attempt；本计划改的是识别次数与流式输出，两者不冲突。`keep` 模式下视觉日志仍能记录每轮识别，属预期。
- **耦合点**：`resolveHistoryImages` 的缓存 key 必须与 `Describe` 完全一致（§4.3），已用常量级联（`md5Hex` + `visionCacheVersion`），代码注释需明确「勿单独改格式」。
