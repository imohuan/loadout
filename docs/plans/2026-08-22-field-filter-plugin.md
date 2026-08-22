# field-filter 能力插件实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现一个可配置的字段过滤能力插件 `field-filter`，在请求转发上游前 / 非流式响应返回后，按配置剔除或保留 JSON body 字段（及响应头），解决「agent 客户端携带上游不支持的字段（如腾讯 copilot 网关的 `client_metadata`）导致 400」这一类问题。

**Architecture:** 插件订阅 model-gateway 的两个 waterfall hook——`ProxyBeforeUpstream`（请求方向，改 `pipe.Request.Body`）与 `ProxyAfterUpstream`（非流式响应方向，改 `AfterUpstreamPayload.Response.Body/Header`）。配置复用 `types.CapabilityRoute`，新增**一个嵌套字段 `FieldRules`**（与 ViaOptions/Replacements 同模式：嵌套结构体存 JSON 列），models/channel 匹配矩阵复用现有机制，数据源 SQLite 优先、`capability_routes.json` 兜底，与 sensitive-filter 插件同构。流式响应**不做字段级处理**（增量 delta 无法删字段），文档标注限制。

**Tech Stack:** Go 1.x, 现有插件框架（core/plugin, core/store, core/db）, model-gateway hook 机制, SQLite（migration 版本制，最新 v19 → 新增 v20）。

**背景事实（已核实）:**
- 腾讯 copilot 网关（copilot.tencent.com）用 `DisallowUnknownFields` 严格解析请求体 → 请求方向剔除 `client_metadata` 是唯一解法；响应方向与此错误无关。
- 存量定向剔除 `stripCopilotClientMetadata`（proxy.go）**保留作兜底**，本插件验证可靠后再单独移除。
- `CapabilityRoute` 加字段后 admin-api 的 CRUD（`/api/capability-routes`）直接透传结构体自动生效，无需改 admin-api。
- SQLite capability_routes 是**固定列**，加字段必须同步改 SQL + migration（admin_repository.go 的 SELECT/INSERT、import_admin.go 的 INSERT 硬编码列）。
- 迁移幂等测试 `TestMigrateIsIdempotent` 硬编码表计数 19（db_test.go:52），加 v20 后**必须改成 20**。
- 测试建库用 `db.OpenMemory()`（自带 Migrate），写库用 `repo.ReplaceCapabilityRoutes(ctx, routes)`（admin_repository.go:45，**无 Save 方法**）。
- sensitive-filter 测试模式：`store.New(t.TempDir())` + `st.Write(types.FileCapabilityRoutes, routes)` 种子路由 + 直接调 handler（service_test.go:14-57）。Task 5/6 照此模式。
- modelgateway `NewService(st, lg, ctx)` 依赖轻（service.go:40），完整链路集成测试可自建 mock `plugin.Context`（接口 10 个方法，On/Waterfall 实现即可，其余空实现）；field-filter 包**无法**访问 modelgateway 包内私有 `newTestService/mockCtx`。
- proxyForward 非流式分支：`Waterfall(ProxyAfterUpstream, ...)` 在 proxy.go:382，返回值经 `out.(*AfterUpstreamPayload)` 取回后写回客户端（:390-400）——**hook 修改 Response.Body/Header 确实生效**，假设成立。

---

### Task 1: `CapabilityRoute` 增加嵌套 `FieldRules` 配置

**Files:**
- Modify: `plugins/types/types.go:289-298`（CapabilityRoute 结构体）

**Step 1: 写失败测试**（证明嵌套字段可 JSON roundtrip）

Create: `plugins/types/types_test.go`（若不存在；存在则追加）

```go
func TestCapabilityRouteFieldRulesJSON(t *testing.T) {
	in := types.CapabilityRoute{
		Models:     []string{"*"},
		Capability: "field_filter",
		Route:      "proxy",
		FieldRules: &types.FieldRules{
			RequestStrip:  []string{"client_metadata", "a.b.c"},
			RequestKeep:   []string{"model", "messages"},
			ResponseStrip: []string{"choices.0.usage"},
			HeaderStrip:   []string{"X-Internal"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out types.CapabilityRoute
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.FieldRules == nil || len(out.FieldRules.RequestStrip) != 2 || out.FieldRules.RequestStrip[0] != "client_metadata" {
		t.Fatalf("FieldRules roundtrip 失败: %+v", out.FieldRules)
	}
	// 未配置时 FieldRules 应为 nil（omitempty 不输出，反序列化为 nil）
	if err := json.Unmarshal([]byte(`{"capability":"vision"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.FieldRules != nil {
		t.Fatalf("未配置 FieldRules 应为 nil，实际 %+v", out.FieldRules)
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/types/ -run TestCapabilityRouteFieldRulesJSON -v`
Expected: FAIL（编译错误，FieldRules 不存在）

**Step 3: 实现**——`plugins/types/types.go`：

```go
// FieldRules 字段过滤规则（field_filter 能力用；nil = 未配置，原样透传）。
// 字段路径支持顶层 key 与点路径嵌套（如 a.b.c）；Keep 非空时走白名单
// （只保留，忽略同方向 Strip）；均无命中时原字节透传。
type FieldRules struct {
	RequestStrip    []string `json:"request_strip,omitempty"`   // 请求体剔除的字段路径
	RequestKeep     []string `json:"request_keep,omitempty"`    // 请求体白名单：只保留这些字段（顶层）
	ResponseStrip   []string `json:"response_strip,omitempty"`  // 非流式响应体剔除的字段路径
	ResponseKeep    []string `json:"response_keep,omitempty"`   // 非流式响应体白名单（顶层）
	HeaderStrip     []string `json:"header_strip,omitempty"`    // 响应头剔除（大小写不敏感）
}
```

`CapabilityRoute` 结构体末尾追加（与 ViaOptions/Replacements 并列）：

```go
	FieldRules *FieldRules `json:"field_rules,omitempty"` // 字段过滤规则（field_filter 用；nil=未配置）
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/types/ -run TestCapabilityRouteFieldRulesJSON -v`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/types/types.go plugins/types/types_test.go
git commit -m "feat(types): capability route 增加嵌套 FieldRules 字段过滤配置"
```

---

### Task 2: SQLite 迁移 + 仓储读写新列

**Files:**
- Modify: `core/db/migrate.go`（追加 version 20）
- Modify: `core/db/admin_repository.go:21`（SELECT）、`:72`（INSERT）
- Modify: `core/db/import_admin.go:182`（INSERT）
- Modify: `core/db/db_test.go:52`（幂等测试表计数 19 → 20）
- Test: `core/db/admin_repository_test.go`（若存在）或 `core/db/db_test.go` 追加

**Step 1: 写失败测试**——验证 FieldRules 经仓储 roundtrip 持久化：

```go
func TestCapabilityRouteFieldRulesPersist(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	routes := []types.CapabilityRoute{{
		Models:     []string{"gpt-4o"},
		Capability: "field_filter",
		Route:      "proxy",
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}, HeaderStrip: []string{"X-Internal"}},
	}}
	if err := repo.ReplaceCapabilityRoutes(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListCapabilityRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FieldRules == nil ||
		len(got[0].FieldRules.RequestStrip) != 1 || got[0].FieldRules.RequestStrip[0] != "client_metadata" ||
		len(got[0].FieldRules.HeaderStrip) != 1 || got[0].FieldRules.HeaderStrip[0] != "X-Internal" {
		t.Fatalf("FieldRules 持久化失败: %+v", got)
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./core/db/ -run TestCapabilityRouteFieldRulesPersist -v`
Expected: FAIL（FieldRules 为空——SQL 没查新列）

**Step 3: 实现**

`core/db/migrate.go` 版本数组末尾（现最新 version 19 之后，约 442 行起）追加：

```go
}, {
	version: 20,
	name:    "capability-routes-field-rules",
	sql: `
ALTER TABLE capability_routes ADD COLUMN field_rules_json TEXT NOT NULL DEFAULT '{}';
`,
},
```

`core/db/db_test.go:52`：表计数断言 `count != 19` 改为 `count != 20`（该测试枚举 sqlite_master 中的表名数量，加列不加表，但需核对——若 52 行处是**列/迁移版本**断言则相应调整；以实际断言对象为准，原则是加 v20 后该测试必须仍通过）。

`core/db/admin_repository.go`：
- ListCapabilityRoutes（:21）：SELECT 追加 `field_rules_json`，Scan（:30）追加 `var fieldRulesJSON string`，`:33` 处改为读取后手动解析（unmarshalEach 无法给 nil 指针目标分配对象）：
  ```go
  if fieldRulesJSON != "" && fieldRulesJSON != "{}" {
      if err := json.Unmarshal([]byte(fieldRulesJSON), &route.FieldRules); err != nil {
          return nil, fmt.Errorf("db: parse field_rules: %w", err)
      }
  }
  ```
- ReplaceCapabilityRoutes（:72）：INSERT 列与 VALUES 追加 `field_rules_json`，写入前 marshal（**nil 时写 `"{}"` 而非 `"null"`，与 DEFAULT 一致**）：
  ```go
  fieldRulesJSON := "{}"
  if route.FieldRules != nil {
      if b, err := json.Marshal(route.FieldRules); err == nil {
          fieldRulesJSON = string(b)
      }
  }
  ```

`core/db/import_admin.go:182`：INSERT 同步追加 `field_rules_json` 列与值（同样 nil → `"{}"`）。

**Step 4: 跑测试确认通过**

Run: `go test ./core/db/ -v`
Expected: PASS（含新测试与既有迁移/仓储测试，TestMigrateIsIdempotent 不因 v20 失败）

**Step 5: Commit**

```bash
git add core/db/migrate.go core/db/admin_repository.go core/db/import_admin.go core/db/db_test.go
git commit -m "feat(db): capability_routes 增加 field_rules_json 列（migration v20）"
```

---

### Task 3: 字段规则核心逻辑（纯函数，TDD）

**Files:**
- Create: `plugins/field-filter/fields.go`
- Create: `plugins/field-filter/fields_test.go`

**Step 1: 写失败测试**

```go
func TestApplyFieldRules(t *testing.T) {
	// 用 map 结构断言（解析后比较），避免 key 顺序敏感
	must := func(out []byte) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("输出非法 JSON: %v\n%s", err, out)
		}
		return m
	}
	t.Run("strip 顶层 key", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","client_metadata":{"a":1}}`), nil, []string{"client_metadata"})
		m := must(out)
		if _, ok := m["client_metadata"]; ok {
			t.Fatalf("client_metadata 未剔除: %s", out)
		}
		if _, ok := m["model"]; !ok {
			t.Fatalf("model 被误删: %s", out)
		}
	})
	t.Run("strip 嵌套点路径", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"foo":{"bar":1,"baz":2}}`), nil, []string{"foo.bar"})
		m := must(out)
		foo := m["foo"].(map[string]any)
		if _, ok := foo["bar"]; ok {
			t.Fatalf("foo.bar 未剔除: %s", out)
		}
		if _, ok := foo["baz"]; !ok {
			t.Fatalf("foo.baz 被误删: %s", out)
		}
	})
	t.Run("strip 不存在路径原字节返回", func(t *testing.T) {
		body := []byte(`{"model":"x"}`)
		if out := applyFieldRules(body, nil, []string{"nope", "a.b.c"}); !bytes.Equal(out, body) {
			t.Fatalf("无命中应返回原字节: %s", out)
		}
	})
	t.Run("keep 白名单", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","messages":[],"client_metadata":{}}`), []string{"model"}, nil)
		m := must(out)
		if _, ok := m["model"]; !ok {
			t.Fatal("model 被删")
		}
		if _, ok := m["messages"]; ok {
			t.Fatal("messages 应被白名单删掉")
		}
		if _, ok := m["client_metadata"]; ok {
			t.Fatal("client_metadata 应被白名单删掉")
		}
	})
	t.Run("keep 与 strip 同配 keep 优先", func(t *testing.T) {
		out := applyFieldRules([]byte(`{"model":"x","keep_me":1,"strip_me":2}`), []string{"model", "keep_me"}, []string{"strip_me"})
		m := must(out)
		if _, ok := m["keep_me"]; !ok {
			t.Fatal("keep_me 应保留")
		}
		if _, ok := m["strip_me"]; ok {
			t.Fatal("strip_me 应被白名单删掉")
		}
	})
	t.Run("非 JSON 原样", func(t *testing.T) {
		body := []byte(`not json`)
		if out := applyFieldRules(body, nil, []string{"x"}); !bytes.Equal(out, body) {
			t.Fatalf("非 JSON 应原样: %s", out)
		}
	})
	t.Run("空 body 原样", func(t *testing.T) {
		var body []byte
		if out := applyFieldRules(body, nil, []string{"x"}); !bytes.Equal(out, body) {
			t.Fatalf("空 body 应原样: %s", out)
		}
	})
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/field-filter/ -run TestApplyFieldRules -v`
Expected: FAIL（applyFieldRules 未定义）

**Step 3: 实现**——`plugins/field-filter/fields.go`：

```go
package fieldfilter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// applyFieldRules 对 JSON body 应用字段规则：keep 非空走白名单（strip 忽略），
// 否则按 strip 剔除。非 JSON（含顶层数组）/空 body 原样返回；无字段命中
// 返回原字节（不重写）。用 map[string]any + UseNumber 保证数字精度无损。
func applyFieldRules(body []byte, keep, strip []string) []byte {
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return body
	}
	var removed int
	if len(keep) > 0 {
		removed = keepTopLevel(obj, keep)
	} else if len(strip) > 0 {
		removed = stripPaths(obj, strip)
	}
	if removed == 0 {
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// stripPaths 按点路径剔除字段（a.b.c 删 obj["a"]["b"]["c"]），返回删除数量。
// 中间节点不是对象时跳过该路径。
func stripPaths(obj map[string]any, paths []string) int {
	removed := 0
	for _, p := range paths {
		parts := strings.Split(p, ".")
		cur := obj
		for i, part := range parts {
			if i == len(parts)-1 {
				if _, ok := cur[part]; ok {
					delete(cur, part)
					removed++
				}
				break
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				break
			}
			cur = next
		}
	}
	return removed
}

// keepTopLevel 白名单：只保留指定的顶层 key，返回删除数量。
func keepTopLevel(obj map[string]any, keep []string) int {
	allowed := make(map[string]bool, len(keep))
	for _, k := range keep {
		allowed[k] = true
	}
	removed := 0
	for k := range obj {
		if !allowed[k] {
			delete(obj, k)
			removed++
		}
	}
	return removed
}
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/field-filter/ -run TestApplyFieldRules -v`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/field-filter/fields.go plugins/field-filter/fields_test.go
git commit -m "feat(field-filter): 字段剔除/白名单核心逻辑（顶层+点路径）"
```

---

### Task 4: 插件骨架 + 装配

**Files:**
- Create: `plugins/field-filter/plugin.go`
- Modify: `plugins/registry.go:26-42`（All() 追加一行）
- Create: `plugins/field-filter/plugin_test.go`

**Step 1: 写失败测试**

```go
func TestManifest(t *testing.T) {
	p := New()
	m := p.Manifest()
	if m.Name != "field-filter" {
		t.Fatalf("插件名 = %s, 期望 field-filter", m.Name)
	}
	if m.Version == "" {
		t.Fatal("Version 为空")
	}
}
```

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/field-filter/ -v`
Expected: FAIL（包不存在 / New 未定义）

**Step 3: 实现**——`plugins/field-filter/plugin.go`：

```go
// Package fieldfilter 实现 Loadout 的字段过滤能力适配器（plugins/field-filter）。
//
// 订阅 model-gateway 的 proxy:before-upstream（请求方向，改请求体）与
// proxy:after-upstream（非流式响应方向，改响应体/响应头）waterfall 事件，
// 按能力路由表（capability_routes，capability="field_filter"）的 field_rules
// 配置剔除/保留字段。
//
// 路由语义：native 原样透传；proxy 应用字段规则；error 路由 fail-open
// （不拒绝请求，按透传处理——与 sensitive-filter 的安全姿态一致）。
// 流式响应不做字段级处理（增量 delta 无法删字段）。
package fieldfilter

import (
	"database/sql"
	"fmt"
	"log/slog"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
)

type fieldFilterPlugin struct{}

func New() plugin.Plugin { return &fieldFilterPlugin{} }

func (p *fieldFilterPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "field-filter",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"field-filter"},
	}
}

func (p *fieldFilterPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg, ok := ctx.Get("logger").(*slog.Logger)
	if !ok || lg == nil {
		return fmt.Errorf("field-filter: missing logger service")
	}
	if st == nil {
		return fmt.Errorf("field-filter: missing store service")
	}
	svc := NewService(st, lg)
	if database, ok := ctx.Get("db").(*sql.DB); ok && database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		}
	}
	ctx.Set("field-filter", svc)
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	return nil
}
```

`plugins/registry.go` All() 中 `sensitivefilter.New(),` 之后追加：

```go
	fieldfilter "loadout/plugins/field-filter"
	// ...
	fieldfilter.New(),
```

**Step 4: 跑测试确认通过**

Run: `go build ./plugins/... && go test ./plugins/field-filter/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/field-filter/plugin.go plugins/field-filter/plugin_test.go plugins/registry.go
git commit -m "feat(field-filter): 插件骨架并装配进 registry"
```

---

### Task 5: 请求方向 hook（改请求体）

**Files:**
- Create: `plugins/field-filter/service.go`
- Create: `plugins/field-filter/service_test.go`

**Step 1: 写失败测试**（照 sensitive-filter/service_test.go 模式：`store.New(t.TempDir())` + `st.Write(types.FileCapabilityRoutes, routes)` 种子路由 + 直接调 handler）

```go
package fieldfilter

import (
	"encoding/json"
	"log/slog"
	"testing"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default()), st
}

func seedRoute(t *testing.T, st *store.Store, route types.CapabilityRoute) {
	t.Helper()
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{route}); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}
}

func proxyPipe(t *testing.T, body any) *modelgateway.ProxyPipeline {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return &modelgateway.ProxyPipeline{
		Request:  &modelgateway.ProxyRequest{Body: raw, Model: "gpt-4o"},
		Metadata: map[string]any{},
	}
}

func TestHandleProxyBeforeUpstreamStrip(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}},
	})
	pipe := proxyPipe(t, map[string]any{
		"model": "gpt-4o", "messages": []any{},
		"client_metadata": map[string]any{"app": "codex"},
	})
	out, err := svc.HandleProxyBeforeUpstream(pipe)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.ProxyPipeline)
	var body map[string]any
	if err := json.Unmarshal(got.Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["client_metadata"]; ok {
		t.Fatalf("client_metadata 未剔除: %s", got.Request.Body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatal("messages 被误删")
	}
}
```

用例补充：未命中路由 → 原样；route=native → 原样；FieldRules=nil（老配置）→ 原样不 panic；keep 白名单模式；非 JSON → 原样。

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/field-filter/ -run TestHandleProxyBeforeUpstream -v`
Expected: FAIL（service.go 不存在）

**Step 3: 实现**——`plugins/field-filter/service.go`：

```go
package fieldfilter

import (
	"context"
	"errors"
	"log/slog"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// capabilityName 能力路由表中字段过滤能力的固定名称。
const capabilityName = "field_filter"

// Service 字段过滤适配器：查能力路由，按配置对请求体/非流式响应体应用字段规则。
type Service struct {
	st   *store.Store
	lg   *slog.Logger
	repo *db.Repository
}

func NewService(st *store.Store, lg *slog.Logger) *Service {
	return &Service{st: st, lg: lg}
}

func (s *Service) SetRepository(repo *db.Repository) { s.repo = repo }

// decideRoute 查能力路由：model + 请求渠道上下文的 field_filter 路由。
// 未命中返回 nil；native/error 返回非 nil route（由调用方按 Route != proxy 排除，
// 均视为原样透传）。读表/解析失败 fail-open：记录日志并返回 nil，不拒绝请求。
// 逻辑照 sensitive-filter 的 DecideRouteScope（含聚合模型渠道上下文）。
func (s *Service) decideRoute(pipe *modelgateway.ProxyPipeline) (*types.CapabilityRoute, error) {
	if pipe == nil || pipe.Request == nil {
		return nil, nil
	}
	scope := types.ChannelScopeFromMetadata(pipe.Metadata, s.requestChannelBaseURL)
	if s.repo != nil {
		routes, err := s.repo.ListCapabilityRoutes(context.Background())
		if err == nil {
			for i := range routes {
				if routes[i].Capability == capabilityName &&
					types.MatchModels(routes[i].Models, pipe.Request.Model) &&
					types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
					return &routes[i], nil
				}
			}
			return nil, nil
		}
		s.lg.Error("field-filter: 从 SQLite 读能力路由表失败，回退 JSON", "err", err)
	}
	var routes []types.CapabilityRoute
	if err := s.st.Read(types.FileCapabilityRoutes, &routes); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		s.lg.Error("field-filter: 读取能力路由表失败，按透传处理", "err", err)
		return nil, nil
	}
	for i := range routes {
		if routes[i].Capability == capabilityName &&
			types.MatchModels(routes[i].Models, pipe.Request.Model) &&
			types.MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
			return &routes[i], nil
		}
	}
	return nil, nil
}

// requestChannelBaseURL 取请求渠道的 base_url（用于渠道级匹配），无渠道/无 repo 返回空串。
func (s *Service) requestChannelBaseURL(channelID string) string {
	if channelID == "" || s.repo == nil {
		return ""
	}
	channels, err := s.repo.ListChannels(context.Background())
	if err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.ID == channelID {
			return ch.BaseURL
		}
	}
	return ""
}

// HandleProxyBeforeUpstream 请求方向 hook：转发上游前按配置剔除/保留请求体字段。
// 仅处理合法 JSON body；未命中路由/native/error/无 FieldRules → 原样透传，绝不拒绝请求。
func (s *Service) HandleProxyBeforeUpstream(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	route, err := s.decideRoute(pipe)
	if err != nil || route == nil || route.Route != types.RouteProxy || route.FieldRules == nil {
		return payload, nil
	}
	r := route.FieldRules
	if len(r.RequestKeep) == 0 && len(r.RequestStrip) == 0 {
		return payload, nil
	}
	before := pipe.Request.Body
	pipe.Request.Body = applyFieldRules(before, r.RequestKeep, r.RequestStrip)
	if string(pipe.Request.Body) != string(before) {
		s.lg.Info("field-filter: 请求体字段过滤", "model", pipe.Request.Model,
			"keep", r.RequestKeep, "strip", r.RequestStrip)
	}
	return pipe, nil
}
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/field-filter/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/field-filter/service.go plugins/field-filter/service_test.go
git commit -m "feat(field-filter): 请求方向字段过滤 hook"
```

---

### Task 6: 非流式响应方向 hook（改响应体 + 响应头）

**Files:**
- Modify: `plugins/field-filter/service.go`
- Modify: `plugins/field-filter/service_test.go`

**Step 1: 写失败测试**

```go
func TestHandleProxyAfterUpstreamStrip(t *testing.T) {
	svc, st := newTestService(t)
	seedRoute(t, st, types.CapabilityRoute{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{
			ResponseStrip: []string{"usage"},
			HeaderStrip:   []string{"x-internal"}, // 小写验证大小写不敏感
		},
	})
	pipe := proxyPipe(t, map[string]any{"model": "gpt-4o"})
	after := &modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"X-Internal": {"secret"}, "Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"total_tokens":10}}`),
		},
	}
	out, err := svc.HandleProxyAfterUpstream(after)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*modelgateway.AfterUpstreamPayload)
	if got.Response.Header.Get("X-Internal") != "" {
		t.Fatalf("X-Internal 未剔除: %+v", got.Response.Header)
	}
	if got.Response.Header.Get("Content-Type") == "" {
		t.Fatal("Content-Type 被误删")
	}
	var body map[string]any
	if err := json.Unmarshal(got.Response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["usage"]; ok {
		t.Fatalf("usage 未剔除: %s", got.Response.Body)
	}
	if _, ok := body["choices"]; !ok {
		t.Fatal("choices 被误删")
	}
}
```

用例补充：response_keep 白名单；FieldRules=nil → 原样；未命中路由 → 原样；空 body → 跳过 body 处理（header 仍处理）。

**Step 2: 跑测试确认失败**

Run: `go test ./plugins/field-filter/ -run TestHandleProxyAfterUpstream -v`
Expected: FAIL（方法未定义）

**Step 3: 实现**——`plugins/field-filter/service.go` 追加（import 补 `"net/http"` 不需要，Header.Del 直接用）：

```go
// HandleProxyAfterUpstream 非流式响应方向 hook：返回前按配置剔除/保留响应体
// 字段与响应头。未命中路由/native/error/无 FieldRules → 原样透传，绝不拒绝请求。
func (s *Service) HandleProxyAfterUpstream(payload any) (any, error) {
	after, ok := payload.(*modelgateway.AfterUpstreamPayload)
	if !ok || after == nil || after.Pipe == nil || after.Response == nil {
		return payload, nil
	}
	route, err := s.decideRoute(after.Pipe)
	if err != nil || route == nil || route.Route != types.RouteProxy || route.FieldRules == nil {
		return payload, nil
	}
	r := route.FieldRules
	if len(r.HeaderStrip) > 0 && after.Response.Header != nil {
		for _, h := range r.HeaderStrip {
			after.Response.Header.Del(h) // http.Header.Del 大小写不敏感
		}
	}
	if len(after.Response.Body) > 0 && (len(r.ResponseKeep) > 0 || len(r.ResponseStrip) > 0) {
		before := after.Response.Body
		after.Response.Body = applyFieldRules(before, r.ResponseKeep, r.ResponseStrip)
		if string(after.Response.Body) != string(before) {
			s.lg.Info("field-filter: 响应体字段过滤", "model", after.Pipe.Request.Model,
				"keep", r.ResponseKeep, "strip", r.ResponseStrip)
		}
	}
	return after, nil
}
```

**Step 4: 跑测试确认通过**

Run: `go test ./plugins/field-filter/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add plugins/field-filter/service.go plugins/field-filter/service_test.go
git commit -m "feat(field-filter): 非流式响应方向字段/响应头过滤 hook"
```

---

### Task 7: 端到端集成测试（mock ctx + 完整 HandleProxy 链路）

**Files:**
- Create: `plugins/field-filter/integration_test.go`

**说明：** field-filter 包无法访问 modelgateway 包内私有 `newTestService/mockCtx`（model_gateway_test.go:22），且 field-filter import modelgateway 会构成循环依赖（不能把测试放 modelgateway 包）。因此**自建最小 mock `plugin.Context`**（只实现 On/Waterfall，其余方法空实现），走 `modelgateway.NewService(st, lg, ctx)` + `HandleProxy` 完整 HTTP 链路。

**Step 1: 写测试**

```go
package fieldfilter

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// mockCtx 最小 plugin.Context：支持 On/Waterfall，其余方法空实现。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

func newMockCtx() *mockCtx { return &mockCtx{handlers: map[string][]plugin.Handler{}} }

func (m *mockCtx) Get(name string) any                            { return nil }
func (m *mockCtx) Set(name string, svc any) plugin.Disposer       { return func() {} }
func (m *mockCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}
func (m *mockCtx) Emit(event string, payload any) {
	for _, h := range m.handlers[event] {
		_, _ = h(payload)
	}
}
func (m *mockCtx) Waterfall(event string, payload any) (any, error) {
	cur := payload
	for _, h := range m.handlers[event] {
		next, err := h(cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
func (m *mockCtx) Effect(fn func()) plugin.Disposer              { return func() {} }
func (m *mockCtx) Logger() *slog.Logger                          { return slog.Default() }
func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }

// TestFieldFilterE2E 完整链路：echo 上游 + 渠道 + field_filter 路由，
// 验证请求方向剔除与响应方向剔除都经 HandleProxy 生效。
func TestFieldFilterE2E(t *testing.T) {
	// 1) 测试 store + 渠道 + 能力路由
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var gotBody string
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":7},"X-Server-Extra":1}`))
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{
			RequestStrip:  []string{"client_metadata"},
			ResponseStrip: []string{"usage"},
			HeaderStrip:   []string{"X-Server-Extra"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// 2) mock ctx 注册 field-filter hook + modelgateway 服务
	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	gw := modelgateway.NewService(st, slog.Default(), ctx)

	// 3) 请求带 client_metadata → 上游收到的 body 无该字段；响应被剔除 usage/header
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"client_metadata":{"app":"codex"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	// 请求方向：echo 收到的 body 无 client_metadata
	if strings.Contains(gotBody, "client_metadata") {
		t.Fatalf("上游收到未剔除的 client_metadata: %s", gotBody)
	}
	// 响应方向：usage 剔除、X-Server-Extra header 剔除
	if strings.Contains(rr.Body.String(), "usage") {
		t.Fatalf("响应未剔除 usage: %s", rr.Body.String())
	}
	if rr.Header().Get("X-Server-Extra") != "" {
		t.Fatalf("响应头 X-Server-Extra 未剔除")
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("响应内容缺失: %s", rr.Body.String())
	}
}
```

**Step 2: 跑测试确认通过**

Run: `go test ./plugins/field-filter/ -run TestFieldFilterE2E -v`
Expected: PASS

**Step 3: Commit**

```bash
git add plugins/field-filter/integration_test.go
git commit -m "test(field-filter): 端到端集成测试（mock ctx + HandleProxy 完整链路）"
```

---

### Task 8: 文档 + 腾讯配置示例

**Files:**
- Create: `plugins/field-filter/README.md`
- Modify: `docs/` 下能力路由配置相关文档（如既有 capability 说明，按实际文件补充一节）

**Step 1: 写文档**

`plugins/field-filter/README.md` 核心内容：

```markdown
# field-filter 字段过滤能力

按能力路由表（capability_routes）配置，对请求体 / 非流式响应体的 JSON 字段做剔除或白名单保留，也可剔除指定响应头。

## 腾讯 copilot 网关示例

Codex 等 agent 客户端会在请求体携带 `client_metadata`，腾讯 copilot 网关（copilot.tencent.com）
以 DisallowUnknownFields 严格解析请求体，导致 400 `json: unknown field "client_metadata"`。

capability_routes.json 配置：

```json
[{
  "capability": "field_filter",
  "models": ["*"],
  "channel_base_urls": ["https://copilot.tencent.com"],
  "route": "proxy",
  "field_rules": {
    "request_strip": ["client_metadata"]
  }
}]
```

## 字段规则语义（field_rules）

- 字段路径：顶层 key 或点路径嵌套（`a.b.c`）；Keep 白名单仅支持顶层 key。
- `request_keep` / `response_keep` 非空时走白名单（只保留），忽略同方向 strip。
- 无字段命中时原字节透传；非 JSON body 不处理。
- `header_strip` 作用于非流式响应头（大小写不敏感）。
- 流式响应（SSE）不做字段级处理（增量 delta 无法删字段），请勿对流式模型配置响应过滤。
```

**Step 2: 提交**

```bash
git add plugins/field-filter/README.md docs/
git commit -m "docs(field-filter): 插件说明 + 腾讯 copilot 网关配置示例"
```

---

### Task 9: 全量验证 + 收尾

**Files:** 无（验证为主）

**Step 1: 全量编译与测试**

Run: `go build ./... && go test ./...`
Expected: 全部 PASS，无回归（特别注意 core/db 的 TestMigrateIsIdempotent 已随 v20 更新）

**Step 2: 手动验证腾讯场景**（重新编译部署后）：

```bash
curl -X POST http://localhost:PORT/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <渠道key>" \
  -d '{"model":"<copilot模型>","messages":[{"role":"user","content":"hi"}],"client_metadata":{"app":"codex"}}'
```

Expected: 不再返回 `json: unknown field "client_metadata"`（请求方向已剔除）；其他渠道带该字段仍正常透传。

**Step 3: 收尾检查**——遗留事项记录到 issue 或本计划末尾：
- [ ] field-filter 稳定运行后，移除 model-gateway 写死的 `stripCopilotClientMetadata` 与 `TestStripCopilotClientMetadata`（保留 `stripAltAuth`，两者职责不同）。
- [ ] 流式响应字段过滤：如需支持，另起任务（ProxyStreamChunk 逐块，仅整块删/非增量块解析，增量 delta 不可删字段）。

---

## 验证清单（最终验收）

- [ ] `go build ./...` 通过
- [ ] `go test ./...` 全绿（含新增 field-filter / types / db 测试；TestMigrateIsIdempotent 随 v20 更新）
- [ ] 腾讯 copilot 渠道请求带 `client_metadata` 不再 400
- [ ] 其他渠道/未配置模型全程原样透传（透明代理语义不破坏）
- [ ] 管理后台能力路由 CRUD 可读写 field_filter 配置（field_rules_json 列持久化生效）
