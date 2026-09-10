# 技能文件树 + 内容预览 Dialog — 资料侦察报告

> 调查对象：`frontend/src/views/SkillsView.vue` 技能列表点击技能 → Dialog 左侧目录树、右侧文件内容预览。
> 调查日期：2026-09-10　调查方式：codegraph MCP（`codegraph_status` / `codegraph_files` / `codegraph_search` / `codegraph_context` / `codegraph_node`）+ 定点读文件取证。

---

## 0. 一句话结论

**后端没有任何「列技能目录 / 读任意技能文件」的接口**，必须新增 2 个只读接口；
前端没有现成的树组件，但 markdown 渲染（`marked`）、代码高亮（`highlight.js`）、
防 XSS（`dompurify`）、左右分栏（`SplitPane.vue`）**都已就绪**，且 `shadcn-vue-cdn` 已全局注册，
`Dialog` / `ScrollArea` / `Tabs` 可直接用，无需新增依赖。

---

## 1. 后端 HTTP 路由表

| 项 | 结论 | 证据 |
|---|---|---|
| 路由表所在文件 | `plugins/admin-api/service.go` | `service.go:95` `func (s *Service) Routes() []plugin.RouteSpec` |
| 注册时机 | 插件 `Apply` 阶段逐条 `ctx.RegisterRoute(spec)` | `plugins/admin-api/plugin.go:102-104` |
| 路径前缀 | **无统一前缀**，每条 Pattern 自带 `/api/...`（Go 1.22+ `net/http` 方法+路径模式，含 `{id}` 通配） | `service.go:98` `Pattern: "POST /api/login"` |
| 鉴权 | 全部走 `Auth: plugin.AuthSession`，handler 用 `s.session(...)` 包装（登录类除外） | `service.go:109` / `service.go:168-180` |
| 路由总数 | 约 120+ 条，覆盖 auth/channels/mcp/keys/skills/presets/processes/unifyai/aggregates/model-status/route-logs/stats/settings | `service.go:96-242` |

### 1.1 技能（skills）相关路由全清单

`plugins/admin-api/service.go:168-180`：

```
GET    /api/skills                      → handleSkillsList
POST   /api/skills                      → handleSkillRegister
POST   /api/skills/install              → handleSkillInstall
POST   /api/skills/import-zip           → handleSkillImportZip
DELETE /api/skills/{name}               → handleSkillDelete
DELETE /api/skills/{name}/source        → handleSkillUnregister
GET    /api/skills/status               → handleSkillsStatus
POST   /api/skills/sync                 → handleSkillSync
POST   /api/skills/check-updates        → handleSkillCheckUpdates
GET    /api/skills/update-status        → handleSkillUpdateStatus
GET    /api/skills/update-stream        → handleSkillUpdateStream   (SSE)
POST   /api/skills/restore              → handleSkillRestore
POST   /api/skills/restore-all          → handleSkillRestoreAll
```

### 1.2 「读文件 / 列目录」类接口排查结果

**结论：没有任何通用「读任意文件 / 列目录 / 读技能文件内容」的接口。**

最接近的是 MCP 日志读取三件套，**只能读 MCP 日志目录、且段名受正则白名单限制，不能复用**：

`service.go:142-143`
```
GET /api/mcp-servers/{name}/log/files  → handleMCPLogFiles   // 列段文件
GET /api/mcp-servers/{name}/log        → handleMCPLogRead    // 读段内容
```
- `service.go:1361` `handleMCPLogFiles`：只调 `s.hub.ListLogFiles(name)`，名字经 `validLogServerName` 校验。
- `service.go:1376` `handleMCPLogRead`：`?file=&offset=&limit=`，段名必须匹配 `mcpLogSegmentRe`（`service.go:1425`，只认 `main.log` / `main-N.log` / `<YYYYMMDD-HHMMSS>.log`）。
- 即：**这是一个专用通道，路径写死在 mcp-hub 内部，无法指向技能目录。**

唯一能拿到「技能目录」信息的是 `handleSkillsList` 返回的 `Skill.path`，但它只是路径字符串，没有目录内容。

---

## 2. 技能数据结构与路径基准

### 2.1 Go 结构体 `plugins/types/types.go:509`

```go
type Skill struct {
    Name        string `json:"name"`                   // 技能名（目录名；SKILL.md frontmatter 的 name 优先）
    Description string `json:"description,omitempty"`
    Source      string `json:"source,omitempty"`       // 如 vercel-labs/agent-skills
    InstalledAt string `json:"installed_at,omitempty"` // RFC3339
    Version     string `json:"version,omitempty"`      // 如 main
    UpdatedAt   string `json:"updated_at,omitempty"`   // 来自 .skill-lock.json
    Path        string `json:"path,omitempty"`         // 技能目录的绝对路径
}
```

### 2.2 前端镜像类型 `frontend/src/lib/types.ts:181`

```ts
export interface Skill {
  name: string
  description?: string
  source?: string
  version?: string
  updated_at?: string
  path?: string // 技能目录的绝对路径（~/.loadout/skills/<name>）
}
```
> 注意：前端类型**没有** `installed_at` 字段（后端有，前端未用）。

### 2.3 路径基准（关键！`path` 到底是哪个目录）

`plugins/skills/service.go:192-204`：
```go
sk.Path = filepath.Join(s.targetDir, sk.Name)   // ← 注意是 targetDir，不是 repoDir
```
而注释写的是「仓库根目录 + 技能目录」（`service.go:202`），**注释与代码不一致**。

两个目录的真实含义（`service.go:63-66`）：
| 字段 | 值 | 含义 |
|---|---|---|
| `repoDir` | `~/.loadout/skills` | **技能库**：所有技能的真实文件所在 |
| `targetDir` | `~/.agents/skills` | **通用目标目录**：agent 实际使用的那份副本（`config.ResolveAgentSkillsDir()`，`core/config/config.go:42`） |

目录推导链：
- `core/config/config.go:249` `HomeDir = "~/.loadout"`
- `core/config/config.go:299-300` `DataDir = <home>/data`，`SkillsDir = <home>/skills`
- `plugins/skills/service.go:71-77` `NewService` 里 `repoDir` 空则回落 `config.SkillsDir`，`targetDir` 空则回落 `config.ResolveAgentSkillsDir()`

`scanSkills` 只扫 `repoDir`（`service.go:188` `skills := scanSkills(s.repoDir)`），
但列表项的 `Path` 指向 `targetDir`。

> ⚠️ **给实现的提醒**：技能列表来自 `repoDir`，而 `path` 指向 `targetDir` 下的
> **同名同构副本**。文件树/预览接口**不要信任前端传来的 `path`**，应当由后端用
> `resolveSkillDir(s.repoDir, name)` 自行解析目录（`service.go:288`），
> 这才是「技能源真正所在的文件夹」，且与 `handleSkillUnregister` 的语义一致。

### 2.4 技能目录解析工具（可直接复用）

`plugins/skills/service.go:288-306`：
```go
// resolveSkillDir 在 dir 里找到名称为 skillName 的技能目录的真实路径：
// 优先按目录名精确匹配，其次匹配 SKILL.md frontmatter 里声明的 name。
func resolveSkillDir(dir, skillName string) string
```
**这个函数正是新接口需要的「名字 → 目录」解析器**，处理了「目录名与 frontmatter name 不一致」的情况
（如目录 `ask-matt copy`、frontmatter name `ask-matt-v2`）。目前是包内未导出函数。

---

## 3. 后端 handler 写法惯例

### 3.1 签名与注册

```go
// plugins/admin-api/service.go:98（注册）
{Method: http.MethodGet, Pattern: "GET /api/skills", Auth: plugin.AuthSession, Handler: s.session(s.handleSkillsList)},

// plugins/admin-api/service.go:2014（handler）
func (s *Service) handleSkillsList(w http.ResponseWriter, r *http.Request) { ... }
```
- 路径参数：`r.PathValue("name")`（见 `service.go:2044`）。
- Query 参数：`r.URL.Query().Get("file")` + `strconv.ParseInt`（见 `service.go:1386-1388`）。
- 请求体：`decodeJSON(w, r, &req)`，失败自动写 400 并 `return`（`service.go:2815`）。

### 3.2 统一响应包装（★ 必须沿用）

`plugins/admin-api/service.go:2787-2821`：
```go
// writeJSON 以 JSON 写出响应。
func writeJSON(w http.ResponseWriter, status int, v any)

// writeError 写出标准错误 JSON（invalid_request_error）。
func writeError(w http.ResponseWriter, status int, message string) {
    writeJSON(w, status, map[string]any{
        "error": map[string]any{
            "message": message,
            "type":    "invalid_request_error",
        },
    })
}

// writeServerError 记录错误日志并返回 500。
func (s *Service) writeServerError(w http.ResponseWriter, err error)   // 500, type: internal_error

// decodeJSON 解析请求体 JSON；失败时写出 400 并返回 false。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool
```

**错误响应形状**（前端 `api.ts:26` 正是读 `body.error?.message`）：
```json
{ "error": { "message": "…", "type": "invalid_request_error" } }
```
成功响应**不包 `data`**，直接是数组或对象（如 `writeJSON(w, http.StatusOK, items)`，`service.go:2020`）。

### 3.3 现有技能 handler 参考模板

`service.go:2014-2021`：
```go
// handleSkillsList 返回技能仓库清单。
func (s *Service) handleSkillsList(w http.ResponseWriter, r *http.Request) {
    items, err := s.skill.List()
    if err != nil {
        s.writeServerError(w, err)
        return
    }
    writeJSON(w, http.StatusOK, items)
}
```
模式固定：**取 `r.PathValue` → 校验 → 调 `s.skill.Xxx()` → 错误 `writeServerError` → 成功 `writeJSON`**。
`Service` 上已持有 `skill *skills.Service`（`service.go:52`），新接口直接加方法到 `skills.Service` 再薄包一层即可。

### 3.4 测试基础设施（新接口应配测试）

`plugins/admin-api/admin_api_test.go:33-94`：
- `newTestServer(t)` 用 `t.TempDir()` 组装 store / admin-auth / gateway-keys / skills（`admin_api_test.go:55`）并起 `httptest.Server`。
- `apiReq(t, ts, method, path, body, cookie)` 发请求，`login(t, ts, pw)` 拿会话 Cookie。
- 技能相关现成用例可参考 `admin_api_test.go:804`（install）、`:847`（import-zip）、`:864`（GET /api/skills）。

> ✅ 该测试环境天然支持「往技能库临时目录写文件再读取」，**新增的文件树/文件读取接口写测试零成本**。

---

## 4. 前端 API 封装

### 4.1 请求底座 `frontend/src/lib/api.ts`

```ts
export async function api<T>(path: string, options: RequestInit = {}): Promise<T>   // api.ts:15
export function request<T>(path: string, method: string, body?: unknown)            // api.ts:33
```
- 用 **原生 `fetch`**（非 axios），`credentials: 'same-origin'`，默认 `Content-Type: application/json`（`api.ts:4,16-21`）。
- **无 baseURL**，直接写 `/api/...` 相对路径（dev 由 `vite.config.ts` 代理）。
- 401 时 `emitter.emit('unauthorized')`（`api.ts:23`）；非 2xx 抛 `ApiError`，message 取 `body.error?.message || body.message`（`api.ts:25-28`）。

### 4.2 业务封装 `frontend/src/composables/useManagementApi.ts`

新增接口的写法（`useManagementApi.ts:4-107`）——**在函数内定义局部 const + 加到 return 对象**：

```ts
export function useManagementApi() {
  // ...（api.ts:12）
  const skills = () => api<Skill[]>('/api/skills')                                   // :12
  const deleteSkill = (name: string) =>
    request<void>(`/api/skills/${encodeURIComponent(name)}`, 'DELETE')               // :30-31
  const importSkillZip = (file: File, name: string) => {                             // :24-29
    const body = new FormData()
    body.set('file', file); body.set('name', name)
    return api<void>('/api/skills/import-zip', { method: 'POST', body })
  }
  return { skills, deleteSkill, importSkillZip, /* … */ }                            // :77-107
}
```
> 约定：**路径参数一律 `encodeURIComponent`**；GET 返回数组/对象直接用 `api<T>`；写操作走 `request<T>`。

### 4.3 列表加载 `useListLoader`

`SkillsView.vue:32-37`：
```ts
const { data: skills, loading: skillsLoading, refreshing: skillsRefreshing, refresh: refreshSkills }
  = useListLoader(api.skills)
```
> 文件树/文件内容属于「点开才加载」，**不要用 `useListLoader`**，局部 `ref` + 手写 load 函数更合适。

---

## 5. 前端 Dialog 用法与 UI 组件清单

### 5.1 UI 组件来源：`shadcn-vue-cdn`（全局注册，无需 import）

`frontend/src/main.ts`：
```ts
import 'shadcn-vue-cdn/style.css'
import { ShadcnVue } from 'shadcn-vue-cdn'
createApp(App).use(createPinia()).use(router).use(ShadcnVue).mount('#app')
```
`package.json` 依赖：`"shadcn-vue-cdn": "^0.0.4"`。

- 因为 `.use(ShadcnVue)` 全局注册，**模板里可直接写 `<Dialog>` / `<Button>` / `<ScrollArea>` 等**（见 `SkillsView.vue:1015`，脚本区并未 import Dialog）。
- 只有需要传给 `<script setup>` 作用域的组件才显式 import（如 `ConfirmDialog.vue:2-10` 的 `AlertDialog` 系列）。

### 5.2 本地 `components/ui` 目录（**只有 4 个文件，没有 Dialog**）

```
frontend/src/components/ui/AxImage.vue
frontend/src/components/ui/AxImageViewer.vue
frontend/src/components/ui/AxJsonViewer.vue
frontend/src/components/ui/sonner/Sonner.vue
```

### 5.3 已有 Dialog 实例（照抄即可）

**简单型** — `SkillsView.vue:1015-1072`（「安装技能」弹窗）：
```vue
<Dialog v-model:open="skillDialog">
  <DialogContent class="sm:max-w-xl!">
    <DialogHeader><DialogTitle>安装技能</DialogTitle></DialogHeader>
    <form class="space-y-3" @submit.prevent="installSkill"> … </form>
  </DialogContent>
</Dialog>
```

**大尺寸 + 关闭钩子型** — `SkillsView.vue:1073-1074`（「编辑预设」弹窗，**最贴近本次需求**）：
```vue
<Dialog v-model:open="presetDialog" @update:open="(o: boolean) => !o && closePresetDialog()">
  <DialogContent class="sm:max-w-8/10! lg:max-w-6/10! lg:max-h-8/10 min-w-0 overflow-hidden">
```
> 用 `sm:max-w-8/10!` + `lg:max-h-8/10` + `overflow-hidden` 撑一个大弹窗；
> `@update:open` 在关闭时清空编辑态（本次可在关闭时清空已选文件与预览内容）。

其他用法参考：`components/ConfirmDialog.vue:22-40`（`AlertDialog`）、`views/ModelTestView.vue:1189`、`views/ManagementView.vue:515`。

### 5.4 「树」组件：**没有现成的**

全仓搜索 `ScrollArea|Tree|Collapsible` 在 `SkillsView.vue` 內 **0 命中**；
`components/` 下没有 tree/文件浏览类组件。

但 **`shadcn-vue-cdn` 提供的 `ScrollArea` / `Tabs` / `Collapsible` 可全局直用**，
配合 `@remixicon/vue` 的 `RiArrowRightSLine` / `RiArrowDownSLine`（`SkillsView.vue:6,7` 已 import 过）
自绘一棵展开/折叠树是成本最低的方案。

### 5.5 左右分栏：`frontend/src/components/SplitPane.vue` ★ 直接可用

```vue
<SplitPane :min-left="220" :min-right="320" :initial="0.32">
  <template #left>…目录树…</template>
  <template #right>…文件预览…</template>
</SplitPane>
```
Props：`minLeft`(180) / `minRight`(180) / `initial`(0.5) / `snap`(56) / `heightClass`（`SplitPane.vue:16-36`）。
特性：可拖拽、双向塌缩占满、宽度自适应（`ResizeObserver`）。
> ⚠️ 它需要父容器有明确高度，弹窗里配合 `lg:max-h-8/10` + `h-full` 使用。

---

## 6. 现成的文件树 / 目录浏览 / Markdown 渲染资源

### 6.1 前端依赖（`frontend/package.json`）

| 依赖 | 版本 | 本用途 |
|---|---|---|
| **`marked`** | ^18.0.10 | ✅ Markdown → HTML（SKILL.md 预览） |
| **`highlight.js`** | ^11.12.0 | ✅ 代码高亮（.js/.ts/.go/.py 等预览） |
| **`dompurify`** | ^3.4.14 | ✅ 渲染前清洗 HTML，防 XSS |
| `@vueuse/core` | ^14.4.0 | 可选（防抖等） |
| `@remixicon/vue` | ^4.9.0 | ✅ 图标（`RiFileLine`/`RiFolderLine`/箭头） |
| `vue-sonner` | ^2.0.9 | ✅ toast 错误提示 |
| `echarts` / `pinia` / `vue-router` / `tailwindcss` | — | 与本次无关 |

> ⚠️ **`marked` / `highlight.js` / `dompurify` 三者目前全仓 0 处 import**（`grep` 无命中），
> 属「装了没用」的闲置依赖 —— 本次正好是它们的第一批使用者，**无需新增任何依赖**。

### 6.2 后端
没有任何文件树生成工具；日志读取用的 `mcp-hub/server_logs.go` 是 mcp-hub 私有实现，不可复用。

---

## 7. Go 后端「安全读文件 / 防路径穿越」可复用件

**结论：没有通用的 `safeJoin` 工具函数，但有 3 处成熟的手写范式，新接口直接照抄即可。**

| 位置 | 代码 | 说明 |
|---|---|---|
| `plugins/admin-api/service.go:1427-1433` | `validLogServerName`：`filepath.Base(name) == name` 且非 `""`/`"."`/`".."` | **最适合照抄的「单段目录名」校验** |
| `plugins/skills/service.go:816-830` | `validSkillName`：非空、非 `.`/`..`、不含 `/\` | 技能名校验（**包内未导出**，需导出或在 admin-api 侧重写） |
| `plugins/skills/service.go:288-306` | `resolveSkillDir`：名字 → 技能目录真实路径 | 目录解析（**包内未导出**） |
| `plugins/skills/service.go:1193-1211` | Zip Slip 双重防御：`filepath.IsAbs` + `clean == ".."` + `strings.HasPrefix(target, dst+sep)` | **文件路径穿越的标准写法，直接照抄** |
| `plugins/admin-api/transfer.go:1173-1174` / `:1246-1247` | 同上思路的另一处实现（相对路径清洗） | 可参考 |

标准范式（照抄 `service.go:1206-1211`）：
```go
target := filepath.Join(root, rel)
if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
    return fmt.Errorf("路径越界: %s", rel)
}
```
> 三层防御建议：① 技能名走 `validSkillName` 语义；② 相对路径 `filepath.Clean` + 拒 `..`/绝对路径；
> ③ 最终 `HasPrefix(target, skillRoot+sep)` 兜底。

---

## 8. 最合适的新增接口设计（如果没有现成接口 → 建在哪、怎么建）

### 8.1 结论：**没有现成接口，需新增 2 个**

| 方法 | 路径 | 作用 |
|---|---|---|
| `GET` | `/api/skills/{name}/tree` | 返回该技能目录的树（目录 + 文件，含大小） |
| `GET` | `/api/skills/{name}/file?path=<相对路径>` | 返回单个文件内容（文本） |

> 路径风格与现有 `GET /api/mcp-servers/{name}/log/files` + `GET /api/mcp-servers/{name}/log`
> 完全一致（列表 + 读取两段式），**沿用同一套惯例最自然**。

### 8.2 建在哪个文件

**分两层，与现有架构一致：**

1. **业务层**（磁盘操作、路径校验、防穿越）→ `plugins/skills/service.go`
   - 新增：`func (s *Service) Tree(name string) ([]TreeNode, error)`（或返回嵌套结构）
   - 新增：`func (s *Service) ReadFile(name, rel string) (FileContent, error)`
   - 内部复用 `resolveSkillDir(s.repoDir, name)`（`service.go:288`）+ 照抄 Zip Slip 防御范式
   - `types.Skill` 同文件风格的新类型定义放 `plugins/types/types.go`（或就近放 skills 包）

2. **HTTP 层**（薄包一层）→ `plugins/admin-api/service.go`
   - 在 `Routes()`（`service.go:168-180` 技能段）追加两行 `RouteSpec`
   - 在技能 handler 区（`service.go:2011` 起、"===== 技能与预设 =====" 段）追加两个 handler，
     照 `handleSkillsList`（`service.go:2014`）的模板写
   - 错误一律用 `writeError` / `s.writeServerError`（`service.go:2794` / `:2804`）

**handler 骨架（贴合现有写法）：**
```go
// GET /api/skills/{name}/tree —— 返回技能目录树。
func (s *Service) handleSkillTree(w http.ResponseWriter, r *http.Request) {
    tree, err := s.skill.Tree(r.PathValue("name"))
    if err != nil {
        writeError(w, http.StatusBadRequest, err.Error())  // 非法技能名 / 目录不存在
        return
    }
    writeJSON(w, http.StatusOK, tree)
}
```

**响应形状建议**（成功响应不包 `data`，与 `handleMCPLogRead` 的扁平 map 一致，`service.go:1418-1421`）：
```jsonc
// /tree
{ "name": "git-tools", "root": "…绝对路径…",
  "entries": [ { "path": "SKILL.md", "name": "SKILL.md", "dir": false, "size": 1234 },
               { "path": "scripts", "name": "scripts", "dir": true, "size": 0 } ] }

// /file
{ "path": "SKILL.md", "size": 1234, "truncated": false, "content": "…" }
```
> 建议**扁平 entries + 前端自建树**（后端简单、前端排序/折叠灵活）；
> `content` 设上限（如 512 KiB）并返回 `truncated` 标记，防超大文件撑爆响应。

### 8.3 需要同步改的测试
- `plugins/skills/skills_test.go`（技能包单测：树生成、穿越拦截）
- `plugins/admin-api/admin_api_test.go`（HTTP 层：用 `newTestServer` + 往临时 repoDir 写文件）

---

## 9. 前端可复用清单（一览）

| 资源 | 位置 | 用途 |
|---|---|---|
| `Dialog` / `DialogContent` / `DialogHeader` / `DialogTitle` / `DialogFooter` | `shadcn-vue-cdn`（全局） | 弹窗外壳；大尺寸写法抄 `SkillsView.vue:1074` |
| `ScrollArea` | `shadcn-vue-cdn`（全局） | 左侧树的滚动容器 / 右侧内容滚动 |
| `Button` / `Badge` / `Tooltip` / `Input` / `Label` | `shadcn-vue-cdn`（全局） | 交互控件（`SkillsView.vue` 已在用） |
| **`SplitPane.vue`** | `frontend/src/components/SplitPane.vue` | **左右分栏 + 可拖拽，直接可用** |
| `LoadingBlock.vue` / `EmptyState.vue` | `frontend/src/components/` | 加载中 / 空状态 |
| `useListLoader` | `frontend/src/composables/useListLoader.ts` | 技能列表加载（弹窗内数据另写） |
| `useAsyncTask` | `frontend/src/composables/useAsyncTask.ts` | 按钮 loading（`isPending(key)`） |
| `toast` | `vue-sonner`（`SkillsView.vue:18`） | 错误提示 |
| `@remixicon/vue` 图标 | 已 import `RiArrowRightSLine` / `RiArrowDownSLine` / `RiLoader4Line` / `RiClipboardLine` 等（`SkillsView.vue:3-17`） | 树展开箭头、文件/文件夹图标 |
| `TranslateText.vue` | `frontend/src/components/` | （可选）描述翻译 |
| **`marked` + `highlight.js` + `dompurify`** | `package.json` 依赖（**全仓未使用，本次首用**） | Markdown 渲染 + 代码高亮 + XSS 清洗 |

**无现成**：树组件、文件预览组件、文件类型图标映射 → 需自建（建议放进
`frontend/src/components/skill-preview/` 或直接在 `SkillsView.vue` 内实现，视体积而定）。

---

## 10. 实现这个功能前后端各需要动哪些文件

前端要动 3～4 个文件：`frontend/src/views/SkillsView.vue`（加一个「预览」入口、切换 `previewDialog`、把当前技能传进新组件）、
`frontend/src/composables/useManagementApi.ts`（加 `skillTree(name)` 和 `skillFile(name, path)` 两个方法）、
`frontend/src/lib/types.ts`（加 `SkillTreeNode` / `SkillFileContent` 类型），
再新建一个弹窗组件（建议 `frontend/src/components/SkillPreviewDialog.vue`，内部用 `SplitPane` 分栏、左树右内容，
按扩展名走 `marked+dompurify` 渲染 md、走 `highlight.js` 渲染代码、其余按纯文本 `<pre>`）；
后端要动 3 个文件：`plugins/skills/service.go`（加 `Tree` 与 `ReadFile` 两个方法，复用 `resolveSkillDir` 并照抄 Zip Slip 防御）、
`plugins/admin-api/service.go`（在 `Routes()` 技能段追加 `GET /api/skills/{name}/tree` 和 `GET /api/skills/{name}/file`，并加两个 handler，
沿用 `writeJSON`/`writeError`/`s.writeServerError`），
以及 `plugins/types/types.go`（放树节点/文件内容结构体）；测试补 `plugins/skills/skills_test.go` 与 `plugins/admin-api/admin_api_test.go`。
**不需要新增任何前端依赖**，也不需要改路由/数据库/迁移。
