# 敏感词过滤插件（sensitive-filter）实施计划

> 状态：待评审
> 目标：新增「敏感词过滤」能力，与视觉（vision）能力并列，命中后对请求体做敏感词替换。

---

## 1. 需求确认（已与用户对齐）

| 决策点 | 结论 |
|---|---|
| 敏感词列表存放位置 | **挂在每条能力路由上**（复用 `capability_routes.json`，与 vision 的 `via_options` 同位置），不新建独立配置文件 |
| 过滤方向 | **仅请求体**（`proxy:before-upstream` 阶段改写 `Request.Body`），不处理响应 |
| 匹配方式 | **纯字符串替换 + 正则替换都支持**（每条规则带 `regex` 开关） |
| route 语义 | **三态对齐 vision**：`proxy`=命中就替换；`native`=原样透传；`error`=命中敏感词直接拒绝请求 |
| JSON 破坏处理 | **整体 stringify → 替换 → 严格校验**：替换后 JSON 非法则拒绝请求（报错），不静默透传 |
| 实现方式 | 整体 body 字符串替换（`string(body)` → 逐条替换 → 校验后写回 `[]byte`） |

**能力标识**：`sensitive_filter`（与 `vision` 同风格的 snake_case）。

---

## 2. 方案总览

完全复刻 `vision` 插件的骨架，把「调视觉模型生成描述」替换成「按规则做字符串替换」：

```
客户端请求 → /v1/{path...}（透明代理 HandleProxy）
  → sniffRequest 提取 model/stream
  → Waterfall(proxy:before-upstream)
      └─ sensitive-filter.HandleProxyBeforeUpstream
           ├─ 查 capability_routes.json 中 capability="sensitive_filter" 的路由
           ├─ 命中判断：MatchModels(model) && MatchChannel(channelID)
           ├─ route=native → 原样透传
           ├─ route=error  → 检测 body 是否含敏感词，命中则拒绝
           └─ route=proxy  → 整体字符串替换 + JSON 严格校验 → 写回 Request.Body
  → 正常转发上游渠道
```

与 vision 的两点关键差异：

1. **不限对话格式**：vision 只处理 `chat/completions`、`messages`、`responses` 三种 path；敏感词过滤是纯文本替换，对**任意 `/v1/{path...}`** 生效（只要 body 是合法 JSON）。
2. **不调用上游模型**：纯本地字符串替换，无缓存、无压缩、无超时、无 failover。

---

## 3. 数据结构变更

### 3.1 `plugins/types/types.go`

新增替换规则结构，并在 `CapabilityRoute` 上挂载：

```go
// SensitiveReplacement 敏感词替换规则：from → to。
// Regex=true 时 from 按正则匹配，to 支持 $1 等捕获组引用（Go regexp 语义）。
type SensitiveReplacement struct {
	From  string `json:"from"`            // 原始内容/敏感词（或正则表达式）
	To    string `json:"to"`              // 替换后的内容
	Regex bool   `json:"regex,omitempty"` // true = from 按正则匹配
}

type CapabilityRoute struct {
	Models       []string               `json:"models"`
	ChannelIDs   []string               `json:"channel_ids,omitempty"`
	Capability   string                 `json:"capability"`
	Route        string                 `json:"route"`
	ViaOptions   []ViaOption            `json:"via_options,omitempty"`   // vision 用
	Replacements []SensitiveReplacement `json:"replacements,omitempty"`  // sensitive_filter 用
}
```

`capability_routes.json` 示例：

```json
[
  {
    "models": ["deepseek-chat"],
    "channel_ids": ["ch-xxx"],
    "capability": "sensitive_filter",
    "route": "proxy",
    "replacements": [
      { "from": "脏话A", "to": "***" },
      { "from": "\\b(\\d{11})\\b", "to": "[手机号]", "regex": true }
    ]
  }
]
```

> 说明：`via_options` 与 `replacements` 按 `capability` 互斥使用，向后兼容旧数据（旧条目无 `replacements` 字段时为空切片）。

---

## 4. 后端实现

### 4.1 新建 `plugins/sensitive-filter/` 包

**`plugin.go`**：照抄 vision，改动点：

```go
const capabilityName = "sensitive_filter"  // 能力路由表中的固定名称

func (p *sensitiveFilterPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "sensitive-filter",
		Version: "0.1.0",
		Inject:  []string{"store", "logger"},
		Provide: []string{"sensitive-filter"},
	}
}

func (p *sensitiveFilterPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	svc := NewService(st, lg)
	ctx.Set("sensitive-filter", svc)
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	return nil
}
```

> 只依赖 `store` + `logger`，**不依赖 db**（敏感词过滤不查渠道、不解密 key，比 vision 更轻）。

**`service.go`**：核心逻辑。

```go
type Service struct {
	st *store.Store
	lg *slog.Logger
}

// DecideRoute 查 capability="sensitive_filter" 的路由（与 vision.DecideRoute 同构）。
func (s *Service) DecideRoute(model, channelID string) (*types.CapabilityRoute, error)

// HandleProxyBeforeUpstream 订阅 proxy:before-upstream。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	// 仅处理合法 JSON body；非 JSON 原样透传（与 vision 行为一致，避免误伤二进制/表单）。
	if !json.Valid(pipe.Request.Body) {
		return payload, nil
	}

	model := pipe.Request.Model
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	route, err := s.DecideRoute(model, channelID)
	if err != nil {
		return nil, sensitiveError(err.Error())
	}
	if route == nil || route.Route == types.RouteNative {
		return payload, nil
	}

	text := string(pipe.Request.Body)

	// error：命中任一敏感词即拒绝（不替换）。
	if route.Route == types.RouteError {
		if s.containsAny(text, route.Replacements) {
			return nil, sensitiveError(fmt.Sprintf("请求命中敏感词过滤规则，模型 %q 已拒绝", model))
		}
		return payload, nil
	}

	// proxy：按数组顺序逐条替换。
	replaced, err := s.replaceAll(text, route.Replacements)
	if err != nil {
		return nil, sensitiveError(err.Error())
	}

	// 严格校验：替换后必须仍是合法 JSON，否则拒绝（用户确认的行为）。
	if !json.Valid([]byte(replaced)) {
		return nil, sensitiveError("敏感词替换后请求体 JSON 非法，已拒绝该请求")
	}

	pipe.Request.Body = []byte(replaced)
	return pipe, nil
}
```

关键子函数：

```go
// containsAny 判断 text 是否命中任一规则（error 模式用）。
func (s *Service) containsAny(text string, rules []types.SensitiveReplacement) bool {
	for _, r := range rules {
		if r.Regex {
			if re, err := regexp.Compile(r.From); err == nil && re.MatchString(text) {
				return true
			}
		} else if strings.Contains(text, r.From) {
			return true
		}
	}
	return false
}

// replaceAll 按数组顺序执行替换；正则规则用 regexp.ReplaceAllString（支持 $1 捕获组）。
// 正则编译失败视为配置错误，返回 error 让请求拒绝（而非静默跳过）。
func (s *Service) replaceAll(text string, rules []types.SensitiveReplacement) (string, error) {
	out := text
	for _, r := range rules {
		if r.Regex {
			re, err := regexp.Compile(r.From)
			if err != nil {
				return "", fmt.Errorf("敏感词正则规则非法 %q: %w", r.From, err)
			}
			out = re.ReplaceAllString(out, r.To)
		} else {
			out = strings.ReplaceAll(out, r.From, r.To)
		}
	}
	return out, nil
}

// sensitiveError 构造统一的敏感词过滤错误（OpenAI error.type = sensitive_filter_error）。
func sensitiveError(msg string) *modelgateway.GatewayError {
	return &modelgateway.GatewayError{Type: "sensitive_filter_error", Msg: msg}
}
```

> `GatewayError` 由 `model-gateway` 的 `writeGatewayError` 统一转成 `{"error":{...}}` 响应，与 vision 的 `vision_capability_error` 机制一致，无需额外写响应逻辑。

### 4.2 注册插件 `plugins/registry.go`

```go
import sensitivefilter "loadout/plugins/sensitive-filter"

func All() []plugin.Plugin {
	return []plugin.Plugin{
		// ...
		vision.New(),
		sensitivefilter.New(),   // 新增：紧随 vision，装配顺序靠后（视觉改写后再做敏感词替换）
		mcphub.New(),
		adminapi.New(),
	}
}
```

> 装配顺序影响 waterfall 订阅者执行顺序。`sensitive-filter` 放 vision 之后，视觉描述文本（如含敏感词）也会被过滤。

---

## 5. 前端实现

### 5.1 `frontend/src/lib/types.ts`

```ts
export interface SensitiveReplacement {
  from: string
  to: string
  regex?: boolean
}

export interface CapabilityRoute {
  models: string[]
  channel_ids?: string[]
  capability: string
  route: 'native' | 'proxy' | 'error' | string
  via_options?: ViaOption[]
  replacements?: SensitiveReplacement[]  // 新增
}
```

### 5.2 新建 `frontend/src/components/capability-routes/SensitiveWordList.vue`

参考 `ModelChannelList.vue` 的布局，但**去掉模型下拉**，每行改为：

```
[原始内容 input] → [替换后内容 input] [正则 switch] [上移] [下移] [删除]
底部：+ 添加规则
```

- `defineModel<SensitiveReplacement[]>({ required: true })`
- `from`/`to` 用 `<Input>` 直接绑定；`regex` 用 `<Switch>`（或 checkbox）
- 上移/下移/删除/添加逻辑照抄 `ModelChannelList.vue`
- 空 `from` 的行在保存时过滤掉（`editor` 的 `submit` 处理）

### 5.3 修改 `frontend/src/components/capability-routes/CapabilityRouteEditor.vue`

1. **能力下拉**加一项：

   ```ts
   const capabilityOptions = [
     { value: 'vision', label: 'vision（视觉）' },
     { value: 'sensitive_filter', label: 'sensitive_filter（敏感词过滤）' },
   ]
   ```

2. **路由方式下拉**按能力动态：
   - `sensitive_filter` 时提供三态：`proxy`（附加代理/替换）、`native`（原生透传）、`error`（命中拒绝）
   - `vision` 保持现状（`proxy`、`native`），避免破坏现有行为

3. **route=proxy 时的下方内容**按能力切换：
   - `capability === 'vision'` → 显示现有「视觉候选」`ModelChannelList`
   - `capability === 'sensitive_filter'` → 显示「敏感词过滤列表」`SensitiveWordList`

4. **form 状态**增加 `replacements: SensitiveReplacement[]`，`watch(route)` 与 `submit()` 同步读写：

   ```ts
   // watch 时
   replacements: route?.replacements?.length
     ? route.replacements.map(r => ({ from: r.from || '', to: r.to || '', regex: !!r.regex }))
     : [{ from: '', to: '', regex: false }]

   // submit 时（route=proxy 且 capability=sensitive_filter）
   const replacements = form.replacements
     .map(r => ({ from: r.from.trim(), to: r.to, regex: !!r.regex }))
     .filter(r => r.from)
   ```

5. **校验**：`sensitive_filter` + `proxy` 时要求 `replacements` 非空（类似 vision 的 via_model 校验）。

6. **文案**：`routeHint` 增加 sensitive_filter 三态说明；`DialogDescription` 改为按能力区分。

### 5.4 修改 `frontend/src/components/capability-routes/CapabilityRouteTable.vue`

- 「视觉候选」列改为**动态列**：`capability === 'vision'` 显示 `via_options`，`capability === 'sensitive_filter'` 显示替换规则摘要（如 `脏话A → ***`，多行 clamp）。
- 其余列（目标模型/渠道/能力/路由方式）不变，`routeLabel`/`routeVariant` 已支持 `error`。

---

## 6. 文件改动清单

| 文件 | 操作 | 说明 |
|---|---|---|
| `plugins/types/types.go` | 修改 | 新增 `SensitiveReplacement`；`CapabilityRoute` 加 `replacements` 字段 |
| `plugins/sensitive-filter/plugin.go` | 新建 | 插件清单 + Apply（订阅 `proxy:before-upstream`） |
| `plugins/sensitive-filter/service.go` | 新建 | Service、DecideRoute、HandleProxyBeforeUpstream、替换逻辑 |
| `plugins/sensitive-filter/service_test.go` | 新建 | 单测（见 §7） |
| `plugins/registry.go` | 修改 | 注册 `sensitivefilter.New()` |
| `frontend/src/lib/types.ts` | 修改 | 新增 `SensitiveReplacement`、`CapabilityRoute.replacements` |
| `frontend/src/components/capability-routes/SensitiveWordList.vue` | 新建 | 敏感词列表编辑组件 |
| `frontend/src/components/capability-routes/CapabilityRouteEditor.vue` | 修改 | 能力下拉 + 三态路由 + 敏感词列表 |
| `frontend/src/components/capability-routes/CapabilityRouteTable.vue` | 修改 | 「视觉候选」列按能力动态展示 |
| `docs/DESIGN.md` | 修改（可选） | §5.5 补充 sensitive_filter 说明 |
| `docs/API.md` | 无需改动 | `/api/capability-routes` 接口结构不变，仅 JSON 多一个字段 |

> 不需要动：`model-gateway`、`admin-api`（capability-routes CRUD 已是通用结构透传）、`config.go`（敏感词过滤无需新增环境变量）、路由/sidebar（能力路由页复用）。

---

## 7. 测试计划

### 后端单测（`service_test.go`）

1. **命中 proxy 替换**：写 `capability_routes.json` 一条 `sensitive_filter`/`proxy` 路由，构造 `ProxyPipeline` body 含敏感词，断言 body 被替换且仍是合法 JSON。
2. **正则替换**：`regex: true` 规则（如 `\d{11}` → `[手机号]`），断言捕获组/替换生效。
3. **native 透传**：route=native，body 不变。
4. **error 命中拒绝**：body 含敏感词，返回 `GatewayError{Type:"sensitive_filter_error"}`。
5. **error 未命中透传**：body 不含敏感词，原样返回。
6. **渠道约束**：`channel_ids` 绑定时，`__current_channel` 不匹配则不生效（复用 `MatchChannel` 语义）。
7. **JSON 破坏拒绝**：替换后 JSON 非法（如 from 命中 body 中某值、to 含未转义引号导致结构破坏），返回错误。
8. **非 JSON body 透传**：body 非法 JSON 时原样透传。
9. **正则非法报错**：`from` 是非法正则，返回错误。

### 前端

- 手工验证：能力下拉选 `sensitive_filter` → 路由方式出现三态 → proxy 时出现敏感词列表 → 增删/上移下移/正则开关 → 保存后表格正确展示替换摘要。

---

## 8. 验收标准

- [ ] 管理后台「能力路由」页可添加 `sensitive_filter` 能力路由，配置目标渠道/目标模型/三态路由/敏感词列表
- [ ] `proxy` 路由命中后，请求体敏感词被替换，转发上游的是替换后的合法 JSON
- [ ] `native` 路由原样透传；`error` 路由命中敏感词时返回 `sensitive_filter_error`
- [ ] 纯字符串与正则替换均生效，正则捕获组 `$1` 可用
- [ ] 替换破坏 JSON 时请求被拒绝（不静默透传）
- [ ] 非 JSON body、未命中路由的请求完全不受影响
- [ ] vision 能力行为不受本次改动影响

---

## 9. 风险与边界

1. **整体字符串替换的误伤**：敏感词若与 JSON 的 key 或结构符号（如 `"`、`,`）重合，会破坏 JSON → 严格校验会拒绝请求。这是用户确认的方案，但需在 UI 文案里提示「替换词请避免与 JSON 结构字符冲突」。
2. **性能**：`replaceAll` 是 O(规则数 × 文本长度)，正则编译每次请求现编。若规则数多可后续加编译缓存（首版不做，保持简单）。
3. **流式请求**：`proxy` 阶段改写的是请求体，流式只影响上游响应，不影响本功能（仅请求体过滤）。
4. **正则 `$` 语义**：`to` 中的 `$1`、`$name` 会被 `ReplaceAllString` 当捕获组展开；若要字面 `$` 需写 `$$`。UI 加一条 hint 说明。
