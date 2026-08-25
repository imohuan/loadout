package mcphub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/mcpkit"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/types"
)

// skillCategory 技能工具在索引里使用的固定分类名。
const skillCategory = "skill"

// ToolEntry 索引中的一个工具。
type ToolEntry struct {
	Name        string         `json:"name"`        // 索引里的调用名（可能带冲突前缀）
	Description string         `json:"description"` // 工具描述
	Category    string         `json:"category"`    // 分类
	Source      string         `json:"source"`      // 来源 MCP 名或 "skills"
	InputSchema map[string]any `json:"-"`           // 完整 JSON Schema（get 返回）
	ServerID    string         `json:"-"`           // 上游 server id（invoke 用）
	RawName     string         `json:"-"`           // 未加前缀的原始工具名（invoke 用）
	IsSkill     bool           `json:"-"`           // 是否为技能条目
}

// Index 工具索引。
type Index struct {
	Version    int               `json:"index_version"` // 索引版本号（每次重建 +1）
	Categories []CategorySummary `json:"categories"`    // 分类总览
	Tools      []ToolEntry       `json:"-"`             // 全部工具（含技能，按 name 排序）
}

// CategorySummary 分类总览条目。
type CategorySummary struct {
	Name        string `json:"name"`        // 分类名
	Description string `json:"description"` // 一句话描述
	Count       int    `json:"count"`       // 该分类下工具数量
}

// Service MCP 聚合网关。
type Service struct {
	st *store.Store
	lg *slog.Logger
	// db 共享 SQLite 连接（core/db 提供），用于 mcp_invocations 调用统计。
	db *sql.DB
	// repo 管理后台配置的 SQLite 仓储（mcp_servers/groups/tools_state/skills）。
	repo *db.Repository
	// repoDir 技能完整仓库目录（config.SkillsDir），SKILL.md 从这里读。
	repoDir string
	// mu 保护 upstreams 连接池、索引缓存与 index_version。
	mu sync.Mutex
	// upstreams 上游连接池：server id → 懒连接的上游客户端。
	upstreams map[string]*mcpkit.Upstream
	// logMgr MCP 会话日志管理器（~/.loadout/logs/mcp/…）。
	logMgr *LogManager
	// tools 索引缓存：全部工具（含技能，按 name 排序）。
	tools []ToolEntry
	// version index_version 计数器（每次成功重建 +1）。
	version int
}

// NewService 创建 MCP 聚合网关服务，技能仓库目录取 config.SkillsDir。
func NewService(st *store.Store, lg *slog.Logger, database *sql.DB) *Service {
	var repo *db.Repository
	if database != nil {
		repo, _ = db.NewRepository(database)
	}
	// config.LogsDir 未初始化（如单测环境未调 config.Load）时 root 为空，
	// LogManager.Write 会静默跳过——避免日志写进意外目录。
	var logsRoot string
	if config.LogsDir != "" {
		logsRoot = filepath.Join(config.LogsDir, "mcp")
	}
	return &Service{
		st:        st,
		lg:        lg,
		db:        database,
		repo:      repo,
		repoDir:   config.SkillsDir,
		upstreams: map[string]*mcpkit.Upstream{},
		logMgr:    NewLogManager(logsRoot),
	}
}

// BuildIndex 从上游收集工具并构建索引：遍历 mcp_servers（enabled）ListTools，
// 应用 tools_state 开关/分类，处理同名冲突（前缀 config.ToolConflictPrefix），
// 再加技能（扫描 skills.json，category=技能，source=skills）。index_version 递增。
func (s *Service) BuildIndex(ctx context.Context) (*Index, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	servers, err := s.readServers()
	if err != nil {
		return nil, err
	}
	states, err := s.readToolStates()
	if err != nil {
		return nil, err
	}

	tools := make([]ToolEntry, 0)
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		up := s.getUpstream(srv)
		// 容错：单个上游故障（网络/认证/挂起）不阻断整个索引——记日志跳过该上游。
		// 否则任意一个 enabled server 失败会让 $smart / 分组端点整体不可用且不埋点。
		// 故障上游的工具后续 invoke 会走「工具不可见」分支，同样埋 error 记录。
		ctxList, cancel := context.WithTimeout(ctx, config.McpListToolsTimeout)
		infos, err := up.ListTools(ctxList)
		cancel()
		if err != nil {
			s.warn("mcphub: 列出上游工具失败，跳过该上游", "server", srv.Name, "err", err)
			continue
		}
		for _, info := range infos {
			state := findToolState(states, srv.ID, info.Name)
			if state != nil && !state.Enabled {
				continue // 工具级开关关掉 → 从索引消失
			}
			category := srv.Name // 默认分类 = 来源 MCP 名
			if state != nil && state.Category != "" {
				category = state.Category
			}
			tools = append(tools, ToolEntry{
				Name:        info.Name, // 先放原始名，稍后统一处理冲突前缀
				Description: info.Description,
				Category:    category,
				Source:      srv.Name,
				InputSchema: info.InputSchema,
				ServerID:    srv.ID,
				RawName:     info.Name,
			})
		}
	}

	// 技能条目（category=技能，source=skills）。
	// 注意：冲突前缀不再在 BuildIndex 全局计算，改为 resolveTools 按「视图」内
	// 是否跨来源同名再决定——这样单 MCP / 分组端点里未勾选同名工具时用原始名即可，
	// 避免用户用分组里看到的原始名 get/invoke 却查不到。
	tools = append(tools, s.collectSkills()...)

	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	idx := &Index{
		Version:    s.bumpVersion(),
		Categories: buildCategories(tools, categoryDescs(servers)),
		Tools:      tools,
	}

	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()

	return idx, nil
}

// ToolView 返回某端点可见的工具列表。endpoint 为 /mcp/{mcp名}、/mcp/{分组名} 或 /mcp/$smart。
func (s *Service) ToolView(endpoint string) ([]ToolEntry, error) {
	return s.resolveTools(endpoint, "")
}

// resolveTools 解析端点 + 可选分组 → 工具列表。
//   - $smart：group 为空返回全部工具（含技能）；group 非空返回该分组勾选工具（分组不存在报错）。
//   - 单 MCP：仅该上游工具；分组：仅该分组勾选工具。
func (s *Service) resolveTools(endpoint, group string) ([]ToolEntry, error) {
	tools, err := s.ensureTools()
	if err != nil {
		return nil, err
	}

	key := strings.TrimPrefix(endpoint, "/mcp/")

	// $smart → 默认全部（含技能），group 非空时只返回该分组。
	if key == "$smart" {
		if group == "" {
			return applyConflictPrefix(tools), nil
		}
		groups, err := s.readGroups()
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.Name == group {
				return applyConflictPrefix(filterGroup(tools, g)), nil
			}
		}
		return nil, fmt.Errorf("mcphub: 分组 %q 不存在", group)
	}

	// 匹配某上游 MCP 的 name → 仅该上游工具。
	servers, err := s.readServers()
	if err != nil {
		return nil, err
	}
	for _, srv := range servers {
		if srv.Name == key {
			var out []ToolEntry
			for _, t := range tools {
				if t.ServerID == srv.ID {
					out = append(out, t)
				}
			}
			return applyConflictPrefix(out), nil
		}
	}

	// 匹配某分组 name → 仅该分组勾选的工具。
	groups, err := s.readGroups()
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.Name == key {
			return applyConflictPrefix(filterGroup(tools, g)), nil
		}
	}

	return nil, fmt.Errorf("mcphub: 未知端点 %q", endpoint)
}

// Endpoints 列出所有端点路径：每个 enabled 上游 → /mcp/{name}，每个分组 → /mcp/{分组名}，加固定 /mcp/$smart。
func (s *Service) Endpoints() ([]string, error) {
	var out []string

	servers, err := s.readServers()
	if err != nil {
		return nil, err
	}
	for _, srv := range servers {
		if srv.Enabled {
			out = append(out, "/mcp/"+srv.Name)
		}
	}

	groups, err := s.readGroups()
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		out = append(out, "/mcp/"+g.Name)
	}

	out = append(out, "/mcp/$smart")
	return out, nil
}

// Status 实现 status 工具：无 category 返回分类总览（或工具较少时扁平返回完整列表），
// 带 category 返回该分类下所有工具（不含 schema）。
func (s *Service) Status(endpoint string, args map[string]any) (string, error) {
	tools, err := s.ToolView(endpoint)
	if err != nil {
		return "", err
	}
	return s.statusWith(tools, args)
}

// statusWith 基于给定工具视图实现 status 逻辑。
func (s *Service) statusWith(tools []ToolEntry, args map[string]any) (string, error) {
	// 带 category：返回指定分类下的工具 {name,description,category,source}（不含 schema）。
	// 支持逗号分隔多个分类；"all" 返回全部。
	if cat := strArg(args, "category"); cat != "" {
		want := map[string]bool{}
		for _, p := range strings.Split(cat, ",") {
			if p = strings.TrimSpace(p); p != "" {
				want[p] = true
			}
		}
		var selected []ToolEntry
		for _, t := range tools {
			if want["all"] || want[t.Category] {
				selected = append(selected, t)
			}
		}
		return marshalStatusTools(selected)
	}

	// 无参数：工具总数 ≤ 阈值时直接返回完整工具列表（扁平），省一轮往返。
	if len(tools) <= config.StatusFlatThreshold {
		return marshalStatusTools(tools)
	}

	// 否则返回分类总览（categories + index_version）。
	// 分类描述来自 mcp_servers.json 的 Description；读取失败仅记日志，返回空描述不阻断总览。
	servers, err := s.readServers()
	if err != nil {
		s.warn("mcphub: 读取上游服务器失败，分类描述将为空", "err", err)
		servers = nil
	}
	overview := struct {
		Categories   []CategorySummary `json:"categories"`
		IndexVersion int               `json:"index_version"`
	}{
		Categories:   buildCategories(tools, categoryDescs(servers)),
		IndexVersion: s.currentVersion(),
	}
	return marshalJSON(overview)
}

// Get 实现 get 工具：按 tools（工具名列表，必填）批量取完整 schema。
// 技能工具返回 SKILL.md 全文（截断 config.SkillBodyMaxChars）。返回 JSON 数组。
func (s *Service) Get(endpoint string, args map[string]any) (string, error) {
	tools, err := s.ToolView(endpoint)
	if err != nil {
		return "", err
	}
	return s.getWith(tools, args)
}

// getWith 基于给定工具视图实现 get 逻辑。
func (s *Service) getWith(tools []ToolEntry, args map[string]any) (string, error) {
	names := strSliceArg(args, "tools")
	if len(names) == 0 {
		return "", errors.New("mcphub: get 需要 tools 参数（工具名列表，以 status 返回的为准）")
	}

	var selected []ToolEntry
	for _, n := range names {
		selected = append(selected, findByNameOrRaw(tools, n)...)
	}
	selected = dedupTools(selected)

	out := make([]any, 0, len(selected))
	for _, t := range selected {
		if t.IsSkill {
			out = append(out, skillGetResp{
				Name:        t.Name,
				Description: t.Description,
				Category:    t.Category,
				Source:      t.Source,
				Body:        s.skillBody(t),
			})
			continue
		}
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{}
		}
		out = append(out, toolGetResp{
			Name:        t.Name,
			Description: t.Description,
			Category:    t.Category,
			Source:      t.Source,
			InputSchema: schema,
		})
	}
	return marshalJSON(out)
}

// Invoke 实现 invoke 工具：校验工具在当前端点视图可见 → 转发给所属上游 CallTool
// （技能条目直接返回 SKILL.md 全文，不执行）→ 结果超 config.MaxToolResultChars 截断并标注。
func (s *Service) Invoke(ctx context.Context, endpoint string, args map[string]any) (string, error) {
	tools, err := s.ToolView(endpoint)
	if err != nil {
		// 视图解析失败同样埋点（tool 名尽力取 args 里的原始值）。
		s.recordInvocation(time.Now().UTC().Format(time.RFC3339Nano), time.Now(), endpoint, err, strArg(args, "tool"), "", safeJSON(args), "", authKindFrom(ctx))
		return "", err
	}
	return s.invokeWith(ctx, tools, args, endpoint)
}

// invokeWith 基于给定工具视图实现 invoke 逻辑。endpoint 用于推导埋点里的
// aggregate 路由信息。工具解析阶段（未到达 callEntry）的错误在此手动埋点；
// 实际工具执行（callEntry）由 callEntry 统一埋点，避免双记。
func (s *Service) invokeWith(ctx context.Context, tools []ToolEntry, args map[string]any, endpoint string) (out string, err error) {
	name := strArg(args, "tool")
	if name == "" {
		return "", errors.New("mcphub: invoke 需要 tool 参数")
	}
	matched := findByNameOrRaw(tools, name)
	if len(matched) == 0 {
		// 工具不可见：没有 callEntry 调用，这里补记一次失败埋点。
		s.recordInvocation(time.Now().UTC().Format(time.RFC3339Nano), time.Now(), endpoint, fmt.Errorf("mcphub: 工具 %q 在当前端点不可见", name), name, "", safeJSON(args), "", authKindFrom(ctx))
		return "", fmt.Errorf("mcphub: 工具 %q 在当前端点不可见", name)
	}
	if len(matched) > 1 {
		s.recordInvocation(time.Now().UTC().Format(time.RFC3339Nano), time.Now(), endpoint, fmt.Errorf("mcphub: 工具 %q 存在 %d 个同名（跨来源）", name, len(matched)), name, "", safeJSON(args), "", authKindFrom(ctx))
		return "", fmt.Errorf("mcphub: 工具 %q 存在 %d 个同名（跨来源），请用带来源前缀的索引名（如 %q）", name, len(matched), matched[0].Name)
	}
	t := matched[0]

	callArgs := mapArg(args, "arguments")
	if callArgs == nil {
		callArgs = map[string]any{}
	}

	res, err := s.callEntry(ctx, t, callArgs, endpoint)
	if err != nil {
		return "", err
	}

	content := make([]invokeContent, 0, len(res.Content))
	for _, part := range res.Content {
		content = append(content, invokeContent{Type: part.Type, Text: part.Text})
	}
	return marshalJSON(invokeResp{Content: content, IsError: res.IsError})
}

// callEntry 执行一个索引条目：技能直接返回 SKILL.md 全文；其余转发给所属上游 CallTool，
// 结果按 config.MaxToolResultChars 截断。供 invoke 与「直接暴露工具」端点复用。
// 所有工具调用统一在这里异步埋点（recordInvocation 内部起 goroutine，不阻塞业务），
// 保证 $smart invoke、单 MCP / 分组端点直接调用、技能调用全部记录，成功失败都记。
func (s *Service) callEntry(ctx context.Context, t ToolEntry, args map[string]any, endpoint string) (*mcpkit.ToolResult, error) {
	startAt := time.Now().UTC().Format(time.RFC3339Nano)
	startTime := time.Now()
	res, err := s.callEntryInner(ctx, t, args)
	// 成功失败都埋点（内部异步，失败仅日志，绝不影响业务返回）。
	// output 仅在成功时序列化（失败 res 为 nil）；input 与认证快照始终落库。
	outputJSON := ""
	if err == nil && res != nil {
		outputJSON = safeJSON(res.Content)
	}
	s.recordInvocation(startAt, startTime, endpoint, err, t.Name, t.Source, safeJSON(args), outputJSON, authKindFrom(ctx))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// callEntryInner 是 callEntry 的实际执行体（无埋点副作用）。
// 帧日志不再在此记录：完整 JSON-RPC 帧流（initialize/tools/list/tools/call + server push）
// 统一由 mcpkit 的 LoggingTransport 包装提供（三种 transport 一致），避免双重记录。
func (s *Service) callEntryInner(ctx context.Context, t ToolEntry, args map[string]any) (*mcpkit.ToolResult, error) {
	if t.IsSkill {
		return &mcpkit.ToolResult{
			Content: []mcpkit.ContentPart{{Type: "text", Text: s.skillBody(t)}},
		}, nil
	}
	up := s.getUpstreamByID(t.ServerID)
	if up == nil {
		return nil, fmt.Errorf("mcphub: 上游 %q 不存在", t.ServerID)
	}
	res, err := up.CallTool(ctx, t.RawName, args)
	if err != nil {
		return nil, err
	}
	res.Content = truncateContent(res.Content, config.MaxToolResultChars)
	return res, nil
}

// EndpointServer 为单 MCP / 分组端点构建「直接暴露工具」的 mcp.Server：
// tools/list 直接返回该端点可见的聚合工具，调用时转发到所属上游（并统一埋点）。
func (s *Service) EndpointServer(endpoint string) (*mcp.Server, error) {
	tools, err := s.resolveTools(endpoint, "")
	if err != nil {
		return nil, err
	}
	return mcpkit.NewServer(endpoint, s.exposedTools(endpoint, tools)), nil
}

// EndpointServerOrEmpty 返回端点 server；构建失败（端点已不存在等）时返回暴露 0 工具的 server。
// 供上层按「每个新 session 动态构建」使用：重新连接时总能拿到最新配置的工具视图。
func (s *Service) EndpointServerOrEmpty(endpoint string) *mcp.Server {
	srv, err := s.EndpointServer(endpoint)
	if err != nil {
		return mcpkit.NewServer(endpoint, nil)
	}
	return srv
}

// Invalidate 清空工具索引缓存并关闭上游连接，下次访问按最新配置重建。
// 由 admin-api 在修改 MCP / 分组 / 工具开关配置后调用，保证端点视图实时生效。
func (s *Service) Invalidate() {
	// 只使工具索引缓存失效，下次访问按最新配置重建。
	// 注意：这里【只】清工具索引缓存，【不清空/关闭上游进程池】——进程池的增删只由
	// SetServerEnabled / dropUpstream 负责（它们按被修改的 server id 精准启停）。
	// 曾在这里清空并 Close 整个池，导致修改任意一个 MCP 配置时所有 stdio 上游进程被
	// 杀掉并从池中移除，在索引重建（惰性触发）前 ServerStatus 会把这些 server 误报为
	// failed，前端点一个开关就出现其他行状态错乱。故上游连接必须保留，避免误杀。
	s.mu.Lock()
	s.tools = nil
	s.mu.Unlock()
}

// exposedTools 把索引条目转成直接暴露的 MCP 工具（handler 转发到所属上游并埋点）。
// endpoint 是端点路径（如 /mcp/github 或 /mcp/分组名），用于推导埋点的 aggregate 路由信息。
func (s *Service) exposedTools(endpoint string, entries []ToolEntry) []mcpkit.ServerTool {
	out := make([]mcpkit.ServerTool, 0, len(entries))
	for _, t := range entries {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		entry := t // 捕获，避免闭包共享循环变量
		out = append(out, mcpkit.ServerTool{
			Name:        entry.Name,
			Description: entry.Description,
			InputSchema: schema,
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				return s.callEntry(ctx, entry, args, endpoint)
			},
		})
	}
	return out
}

// SmartEndpointServer 为 $smart 端点构建「3 工具入口」的 mcp.Server（status/get/invoke）。
// group 为请求 header 指定的分组名；空 = 全部工具；分组不存在时三个入口返回错误。
func (s *Service) SmartEndpointServer(group string) *mcp.Server {
	statusSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"category": map[string]any{"type": "string", "description": "分类名（进入第二级目录；可传单个分类，多个用逗号分隔，\"all\" 返回全部）。省略则返回第一级分类总览"},
		},
	}
	getSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "按名批量获取的工具名列表（必填，名字以 status 返回的为准）"},
		},
		"required": []any{"tools"},
	}
	invokeSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tool":      map[string]any{"type": "string", "description": "要调用的工具名"},
			"arguments": map[string]any{"type": "object", "description": "工具参数（结构 = 该工具 inputSchema）"},
		},
		"required": []any{"tool"},
	}

	// resolve 按 group 解析 $smart 端点的工具视图。
	resolve := func() ([]ToolEntry, error) {
		return s.resolveTools("/mcp/$smart", group)
	}

	tools := []mcpkit.ServerTool{
		{
			Name:        "status",
			Description: "查看当前可用的 MCP 工具。当用户提到使用 MCP，或者你在当前工具列表中找不到需要的工具时，必须首先调用此工具。本工具严格采用二级分类机制，你必须完成以下两步才能继续后续任务：\n1. 第一步：无参数调用本工具，获取第一级目录（分类总览）。\n2. 第二步：必须携带 category 参数调用本工具，进入第二级目录，获取该分类下的具体工具列表。\n【强制禁止】绝对禁止在仅获取第一级目录（未携带 category 参数进入第二级）的情况下，直接调用 get 或 invoke 工具。只有在第二级目录中确认目标工具后，才能使用 get 加载定义。",
			InputSchema: statusSchema,
			Handler: func(_ context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				view, err := resolve()
				if err != nil {
					return nil, err
				}
				out, err := s.statusWith(view, args)
				if err != nil {
					return nil, err
				}
				return &mcpkit.ToolResult{Content: []mcpkit.ContentPart{{Type: "text", Text: out}}}, nil
			},
		},
		{
			Name:        "get",
			Description: "批量加载工具的完整定义。必须在 status 工具进入第二级目录并确认目标工具存在后，再调用本工具一次性加载本次任务需要的所有工具定义。未加载定义的工具 invoke 时无法正确传参。【强制禁止】禁止跳过 status 的第二级目录查询直接调用本工具。",
			InputSchema: getSchema,
			Handler: func(_ context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				view, err := resolve()
				if err != nil {
					return nil, err
				}
				out, err := s.getWith(view, args)
				if err != nil {
					return nil, err
				}
				return &mcpkit.ToolResult{Content: []mcpkit.ContentPart{{Type: "text", Text: out}}}, nil
			},
		},
		{
			Name:        "invoke",
			Description: "调用一个具体工具。必须先通过 status 进入第二级目录确认工具存在，再用 get 加载其完整定义，最后严格按定义里的参数格式调用。【强制禁止】禁止在未走完 status 二级目录或未 get 的情况下猜测参数直接调用。",
			InputSchema: invokeSchema,
			Handler: func(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
				view, err := resolve()
				if err != nil {
					return nil, err
				}
				out, err := s.invokeWith(ctx, view, args, "/mcp/$smart")
				if err != nil {
					return nil, err
				}
				return &mcpkit.ToolResult{Content: []mcpkit.ContentPart{{Type: "text", Text: out}}}, nil
			},
		},
	}

	return mcpkit.NewServer("/mcp/$smart", tools)
}

// ===== 内部辅助 =====

// readServers 读 MCP 服务器清单（SQLite 优先，fallback mcp_servers.json）。
func (s *Service) readServers() ([]types.MCPServer, error) {
	if s.repo != nil {
		servers, err := s.repo.ListMCPServers(context.Background())
		if err == nil {
			return servers, nil
		}
		s.warn("mcphub: 从 SQLite 读 MCP 服务器失败，回退 JSON", "err", err)
	}
	var servers []types.MCPServer
	if err := s.st.Read(types.FileMCPServers, &servers); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return servers, nil
}

// readToolStates 读工具开关（SQLite 优先，fallback tools_state.json）。
func (s *Service) readToolStates() ([]types.ToolState, error) {
	if s.repo != nil {
		states, err := s.repo.ListToolStates(context.Background())
		if err == nil {
			return states, nil
		}
		s.warn("mcphub: 从 SQLite 读工具开关失败，回退 JSON", "err", err)
	}
	var states []types.ToolState
	if err := s.st.Read(types.FileToolsState, &states); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return states, nil
}

// readGroups 读分组（SQLite 优先，fallback groups.json）。
func (s *Service) readGroups() ([]types.Group, error) {
	if s.repo != nil {
		groups, err := s.repo.ListGroups(context.Background())
		if err == nil {
			return groups, nil
		}
		s.warn("mcphub: 从 SQLite 读分组失败，回退 JSON", "err", err)
	}
	var groups []types.Group
	if err := s.st.Read(types.FileGroups, &groups); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return groups, nil
}

// getUpstream 按配置取（或创建）上游连接，写入连接池（并发安全）。
// 注意：本方法全程持 s.mu；LogHook 回调（logMgr.Write）不得反向调用 hub 方法（死锁）。
func (s *Service) getUpstream(srv types.MCPServer) *mcpkit.Upstream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.upstreams[srv.ID]; ok {
		return u
	}
	u := mcpkit.NewUpstream(mcpkit.UpstreamConfig{
		Name:      srv.Name,
		Transport: srv.Transport,
		Command:   srv.Command,
		Args:      srv.Args,
		Env:       srv.Env,
		URL:       srv.URL,
		Headers:   srv.Headers,
		LogHook: func(kind string, fields ...any) {
			// connect = 新的连接会话开始：先清空该 server 旧日志（含全部滚动段），
			// 从空的 main.log 重新记录——"重启/重连后日志自动清空"。
			// 复用连接的后续调用（tools/call 等）不发 connect，不会误清历史。
			if kind == "connect" {
				s.logMgr.RemoveServerLogs(srv.Name)
			}
			// Ensure 幂等（首次建目录/文件，后续直接返回）：Write 不负责建日志，
			// 事件回调里先确保 ServerLog 存在再写。
			s.logMgr.Ensure(srv.ID, srv.Name)
			s.logMgr.Write(srv.ID, kind, s.enrichHookFields(srv, kind, fields...)...)
		},
	})
	s.upstreams[srv.ID] = u
	return u
}

// enrichHookFields 给 connect 事件补全连接信息字段（transport/cmd/args/env/url/headers，
// 敏感值掩码）；其余事件原样透传。mcpkit 只发基础事件，server 全量配置在此拼接。
func (s *Service) enrichHookFields(srv types.MCPServer, kind string, fields ...any) []any {
	if kind != "connect" {
		return fields
	}
	out := []any{"transport", srv.Transport}
	switch srv.Transport {
	case "stdio":
		out = append(out, "cmd", srv.Command, "args", srv.Args)
		if len(srv.Env) > 0 {
			out = append(out, "env", maskMap(srv.Env))
		}
	case "http", "sse":
		out = append(out, "url", srv.URL)
		if len(srv.Headers) > 0 {
			out = append(out, "headers", maskMap(srv.Headers))
		}
	}
	return append(out, fields...)
}

// getUpstreamByID 从连接池取指定 server id 的上游（不存在返回 nil）。
func (s *Service) getUpstreamByID(id string) *mcpkit.Upstream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upstreams[id]
}

// dropUpstream 从连接池移除指定 server id 的上游（调用方需已确保 Close）。
func (s *Service) dropUpstream(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.upstreams, id)
}

// findServer 按 id 读服务器配置。
func (s *Service) findServer(id string) (*types.MCPServer, error) {
	servers, err := s.readServers()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if servers[i].ID == id {
			return &servers[i], nil
		}
	}
	return nil, fmt.Errorf("mcphub: MCP 服务器不存在: %s", id)
}

// ServerRunState 上游进程的运行状态（前端状态列展示）。
type ServerRunState string

const (
	// StateRunning 已启动且存活（stdio 子进程常驻；HTTP/SSE 为外部服务，enabled 即视为运行中）。
	StateRunning ServerRunState = "running"
	// StateStopped 未启动（enabled=false 或从未拉起）。
	StateStopped ServerRunState = "stopped"
	// StateFailed 启动失败或进程已崩溃退出（enabled=true 但进程没活着）。
	StateFailed ServerRunState = "failed"
)

// SetServerEnabled 按开关启停上游进程（进程生命周期与 enabled 开关绑定）：
//   - enabled=true：拉起 stdio 子进程并常驻后台（HTTP/SSE 无进程可拉，仅入池）；
//     启动失败返回错误，由调用方决定是否展示「失败」状态。
//   - enabled=false：杀掉进程并从连接池移除。
func (s *Service) SetServerEnabled(ctx context.Context, id string, enabled bool) error {
	srv, err := s.findServer(id)
	if err != nil {
		return err
	}
	if !enabled {
		if up := s.getUpstreamByID(id); up != nil {
			_ = up.Close()
			s.dropUpstream(id)
		}
		return nil
	}
	return s.getUpstream(*srv).Connect(ctx)
}

// StartEnabled 启动时自动拉起所有 enabled 的上游（stdio 常驻进程）。
// 并行 Connect（总耗时 = 最慢的一个，而非求和），单个失败只记日志不阻断
// 整体启动——失败状态由前端经 ServerStatus 展示。成功/失败/汇总都会输出日志。
func (s *Service) StartEnabled(ctx context.Context) {
	servers, err := s.readServers()
	if err != nil {
		s.warn("mcphub: 启动恢复：读服务器清单失败", "err", err)
		return
	}

	var enabled []types.MCPServer
	for _, srv := range servers {
		if srv.Enabled {
			enabled = append(enabled, srv)
		}
	}
	if len(enabled) == 0 {
		s.info("mcphub: 启动恢复：无启用的 MCP 服务器，跳过")
		return
	}
	s.info("mcphub: 启动恢复：开始拉起", "count", len(enabled))

	var wg sync.WaitGroup
	var failed int
	var mu sync.Mutex
	for _, srv := range enabled {
		wg.Add(1)
		go func(srv types.MCPServer) {
			defer wg.Done()
			if err := s.getUpstream(srv).Connect(ctx); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				s.warn("mcphub: 启动恢复拉起失败", "server", srv.Name, "err", err)
			} else {
				s.info("mcphub: 启动恢复拉起成功", "server", srv.Name)
			}
		}(srv)
	}
	wg.Wait()

	s.info("mcphub: 启动恢复完成", "total", len(enabled), "ok", len(enabled)-failed, "failed", failed)
}

// ServerStatus 返回指定 server 的进程运行状态。
//   - enabled=false → stopped
//   - HTTP/SSE（外部服务，无本地进程）→ enabled 即 running
//   - stdio：池中无 Upstream 或进程未存活 → failed（启动失败 / 已崩溃，不自动重启）
//   - stdio：进程存活 → running
func (s *Service) ServerStatus(id string) (ServerRunState, error) {
	srv, err := s.findServer(id)
	if err != nil {
		return StateStopped, nil
	}
	if !srv.Enabled {
		return StateStopped, nil
	}
	if srv.Transport != "stdio" {
		return StateRunning, nil
	}
	up := s.getUpstreamByID(id)
	if up == nil || !up.Alive() {
		return StateFailed, nil
	}
	return StateRunning, nil
}

// ServerError 返回指定 server 最近一次建连失败的错误原因（无记录返回空串）。
func (s *Service) ServerError(id string) string {
	up := s.getUpstreamByID(id)
	if up == nil {
		return ""
	}
	return up.LastError()
}

// Close 实现 io.Closer：退出时杀掉所有已拉起的上游子进程，关闭全部日志句柄。
// core/plugin 的 Assembly.Unload 会检测 io.Closer 并自动调用。
func (s *Service) Close() error {
	s.mu.Lock()
	ups := make([]*mcpkit.Upstream, 0, len(s.upstreams))
	for _, up := range s.upstreams {
		ups = append(ups, up)
	}
	s.mu.Unlock()
	var firstErr error
	for _, up := range ups {
		if err := up.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.logMgr != nil {
		s.logMgr.CloseAll()
	}
	return firstErr
}

// bumpVersion 递增 index_version 并返回新值。
func (s *Service) bumpVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	return s.version
}

// currentVersion 返回当前 index_version。
func (s *Service) currentVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// ensureTools 返回索引缓存；尚未构建时惰性构建一次。
func (s *Service) ensureTools() ([]ToolEntry, error) {
	s.mu.Lock()
	tools := s.tools
	s.mu.Unlock()
	if tools != nil {
		return tools, nil
	}
	if _, err := s.BuildIndex(context.Background()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tools, nil
}

// collectSkills 扫描技能清单（SQLite 优先，fallback skills.json），为每个技能生成一个技能条目（含 frontmatter 描述）。
func (s *Service) collectSkills() []ToolEntry {
	var skills []types.Skill
	if s.repo != nil {
		if repoSkills, err := s.repo.ListSkills(context.Background()); err == nil {
			skills = repoSkills
		} else {
			s.warn("mcphub: 从 SQLite 读技能清单失败，回退 JSON", "err", err)
		}
	}
	if skills == nil {
		if err := s.st.Read(types.FileSkills, &skills); err != nil {
			if !errors.Is(err, store.ErrNotExist) {
				s.warn("mcphub: 读取技能清单失败", "err", err)
			}
			return nil
		}
	}

	out := make([]ToolEntry, 0, len(skills))
	for _, sk := range skills {
		body := readSkillBody(filepath.Join(s.repoDir, sk.Name, "SKILL.md"))
		desc := parseSkillDescription(body)
		if desc == "" {
			desc = sk.Name
		}
		out = append(out, ToolEntry{
			Name:        sk.Name,
			Description: desc,
			Category:    skillCategory,
			Source:      "skills",
			RawName:     sk.Name,
			IsSkill:     true,
		})
	}
	return out
}

// skillBody 读取技能的 SKILL.md 全文并按 SkillBodyMaxChars 截断；缺失时返回提示而非空串。
func (s *Service) skillBody(t ToolEntry) string {
	path := filepath.Join(s.repoDir, t.RawName, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("（技能 %s 的 SKILL.md 缺失或不可读：%s。请用「安装」而非「登记」方式添加技能）", t.RawName, path)
	}
	return truncateRunes(string(data), config.SkillBodyMaxChars)
}

// warn 在日志器可用时记录一条警告日志（nil 日志器安全）。
func (s *Service) warn(msg string, args ...any) {
	if s.lg != nil {
		s.lg.Warn(msg, args...)
	}
}

// info 在日志器可用时记录一条信息日志（nil 日志器安全）。
func (s *Service) info(msg string, args ...any) {
	if s.lg != nil {
		s.lg.Info(msg, args...)
	}
}

// ===== 纯函数辅助 =====

// applyConflictPrefix 对「当前视图内」跨来源同名的工具加「来源_」前缀
// （config.ToolConflictPrefix 开启时），并返回按 name 排序的新切片（不污染输入）。
// 前缀按视图计算：单 MCP / 分组端点里未勾选同名工具时用原始名即可。
func applyConflictPrefix(tools []ToolEntry) []ToolEntry {
	out := make([]ToolEntry, len(tools))
	copy(out, tools)

	if config.ToolConflictPrefix {
		// 统计每个原始名出现的来源集合。
		seen := map[string]map[string]bool{}
		for _, t := range out {
			if seen[t.RawName] == nil {
				seen[t.RawName] = map[string]bool{}
			}
			seen[t.RawName][t.Source] = true
		}
		for i := range out {
			if len(seen[out[i].RawName]) > 1 {
				out[i].Name = out[i].Source + "_" + out[i].RawName
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildCategories 按分类统计工具数量，按分类名排序，返回分类总览。
// categoryDescs 从服务器列表构建分类描述映射：分类名 → 描述。
// 默认分类 = 来源 MCP 名，取服务器配置的 Description；技能分类给固定默认描述。
// 自定义分类（tools_state）没有描述来源，保持空字符串。
func categoryDescs(servers []types.MCPServer) map[string]string {
	descs := map[string]string{skillCategory: "已安装的技能工具"}
	for _, srv := range servers {
		if desc := strings.TrimSpace(srv.Description); desc != "" {
			descs[srv.Name] = desc
		}
	}
	return descs
}

func buildCategories(tools []ToolEntry, descs map[string]string) []CategorySummary {
	counts := map[string]int{}
	for _, t := range tools {
		counts[t.Category]++
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]CategorySummary, 0, len(names))
	for _, n := range names {
		out = append(out, CategorySummary{Name: n, Description: descs[n], Count: counts[n]})
	}
	return out
}

// filterGroup 返回分组勾选工具对应的索引条目（按 server_id + 原始工具名匹配）。
func filterGroup(tools []ToolEntry, g types.Group) []ToolEntry {
	var out []ToolEntry
	for _, gt := range g.Tools {
		for _, t := range tools {
			if t.ServerID == gt.ServerID && t.RawName == gt.ToolName {
				out = append(out, t)
			}
		}
	}
	return out
}

// findByNameOrRaw 按名称查找工具，返回所有匹配项。匹配优先级：
//  1. 索引名精确匹配（Name，可能是「分类_工具名」形式的冲突前缀名）；
//  2. 「分类_工具名」组合匹配（Category+"_"+RawName，显式限定分类）；
//  3. 原始名匹配（RawName，跨来源同名可能返回多个）。
func findByNameOrRaw(tools []ToolEntry, name string) []ToolEntry {
	for _, t := range tools {
		if t.Name == name {
			return []ToolEntry{t}
		}
	}
	var out []ToolEntry
	for _, t := range tools {
		if t.Category+"_"+t.RawName == name {
			out = append(out, t)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, t := range tools {
		if t.RawName == name {
			out = append(out, t)
		}
	}
	return out
}

// dedupTools 对工具列表去重：RawName+Description+InputSchema 完全相同的只保留第一个。
// 用于原始名命中多个跨来源同名工具时，避免完全相同的重复暴露（如同一上游被重复配置）。
func dedupTools(tools []ToolEntry) []ToolEntry {
	seen := map[string]bool{}
	out := make([]ToolEntry, 0, len(tools))
	for _, t := range tools {
		key := t.RawName + "\x00" + t.Description + "\x00" + marshalSchemaKey(t.InputSchema)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// marshalSchemaKey 把 inputSchema 序列化为稳定的字符串键（map 按 key 排序，结果确定）。
func marshalSchemaKey(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return fmt.Sprintf("%v", schema)
	}
	return string(b)
}

// findToolState 在 tools_state 里查找 (server_id, tool_name) 的记录；不存在返回 nil。
func findToolState(states []types.ToolState, serverID, toolName string) *types.ToolState {
	for i := range states {
		if states[i].ServerID == serverID && states[i].ToolName == toolName {
			return &states[i]
		}
	}
	return nil
}

// truncateContent 对结果内容做字符截断：超出 max 时截断并在末尾标注「结果已截断」。
func truncateContent(parts []mcpkit.ContentPart, max int) []mcpkit.ContentPart {
	const mark = "…（结果已截断）"
	out := make([]mcpkit.ContentPart, 0, len(parts))
	used := 0
	for _, p := range parts {
		runes := []rune(p.Text)
		if used+len(runes) > max {
			room := max - used
			if room < 0 {
				room = 0
			}
			out = append(out, mcpkit.ContentPart{Type: p.Type, Text: string(runes[:room]) + mark})
			return out
		}
		out = append(out, p)
		used += len(runes)
	}
	return out
}

// readSkillBody 读取指定路径文件内容；不存在返回空字符串。
func readSkillBody(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// parseSkillDescription 解析 SKILL.md 的 YAML frontmatter，返回 description 字段；无则返回空。
func parseSkillDescription(body string) string {
	const sep = "---"
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != sep {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == sep {
			break
		}
		if strings.HasPrefix(line, "description:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// truncateRunes 按字符数截断字符串；未超限返回原串。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// ===== 参数解析与序列化 =====

// strArg 从参数 map 读取字符串；缺失或类型不符返回空串。
func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// strSliceArg 从参数 map 读取字符串数组（支持 []string、[]any 与单个 string，兼容误传）。
func strSliceArg(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	switch arr := args[key].(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if v, ok := e.(string); ok {
				out = append(out, v)
			}
		}
		return out
	case string:
		if arr == "" {
			return nil
		}
		return []string{arr}
	}
	return nil
}

// mapArg 从参数 map 读取嵌套对象；缺失或类型不符返回 nil。
func mapArg(args map[string]any, key string) map[string]any {
	if args == nil {
		return nil
	}
	if m, ok := args[key].(map[string]any); ok {
		return m
	}
	return nil
}

// marshalJSON 序列化并返回 JSON 字符串。
func marshalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("mcphub: 序列化结果失败: %w", err)
	}
	return string(data), nil
}

// safeJSON 序列化失败时返回空串，供埋点使用（埋点绝不阻塞业务）。
func safeJSON(v any) string {
	s, err := marshalJSON(v)
	if err != nil {
		return ""
	}
	return s
}

// authKindFrom 读 request ctx 中的认证方式快照；未注入返回 "public"。
func authKindFrom(ctx context.Context) string {
	return string(plugin.AuthKindFrom(ctx))
}

// statusTool 是 status 扁平/分类查询返回的工具条目（不含 schema，仅名称+描述）。
type statusTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// marshalStatusTools 把工具条目转成 status 的 JSON 数组（字段顺序固定）。
func marshalStatusTools(tools []ToolEntry) (string, error) {
	out := make([]statusTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, statusTool{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return marshalJSON(out)
}

// toolGetResp 是 get 返回的工具条目（含完整 inputSchema）。
type toolGetResp struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Source      string         `json:"source"`
	InputSchema map[string]any `json:"inputSchema"`
}

// skillGetResp 是 get 返回的技能条目（含 SKILL.md 全文 body）。
type skillGetResp struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	Body        string `json:"body"`
}

// invokeContent 是 invoke 返回的单个内容片段。
type invokeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// invokeResp 是 invoke 返回的结果（content + isError）。
type invokeResp struct {
	Content []invokeContent `json:"content"`
	IsError bool            `json:"isError"`
}

// ---------------------------------------------------------------------------
// 会话日志 API（admin-api 端点转发到这里）
// ---------------------------------------------------------------------------

// ListLogs 返回全部有日志文件的 server（含段列表与 transport）。供前端下拉。
func (s *Service) ListLogs() []LogServerInfo {
	out := []LogServerInfo{}
	if s.logMgr == nil {
		return out
	}
	names := s.logMgr.ListServers()
	servers, _ := s.readServers()
	byName := make(map[string]string, len(servers))
	for _, srv := range servers {
		byName[srv.Name] = srv.Transport
	}
	for _, name := range names {
		out = append(out, LogServerInfo{
			Name:      name,
			Transport: byName[name],
			Files:     s.logMgr.ListFiles(name),
		})
	}
	return out
}

// ListLogFiles 返回指定 server 的段文件列表（「加载更早」回溯用）。
func (s *Service) ListLogFiles(name string) []LogFileInfo {
	if s.logMgr == nil {
		return nil
	}
	return s.logMgr.ListFiles(name)
}

// ReadLog 增量读指定段文件：返回 (数据, 段总字节, eof, err)。
// name/segment 的合法性由 LogManager.Read 校验（单段名 + 段名正则）。
func (s *Service) ReadLog(name, segment string, offset, limit int64) ([]byte, int64, bool, error) {
	if s.logMgr == nil {
		return nil, 0, false, nil
	}
	return s.logMgr.Read(name, segment, offset, limit)
}

// RemoveServerLogs 按 server 名删除其整个日志目录（server 删除联动）。
func (s *Service) RemoveServerLogs(name string) {
	if s.logMgr == nil {
		return
	}
	s.logMgr.RemoveServerLogs(name)
}

// InvokeTool 按 server id + 工具名直接调用上游（「测试工具」复用生产路径）：
// 走连接池（复用常驻连接）、ToolView 路由、callEntry 埋点与日志——与网关调用完全一致。
// server 未启用 / 工具不可见时返回明确错误。
// 注意：所有未到达 callEntry 的失败分支必须手动埋点，否则失败尝试不落 mcp_invocations。
func (s *Service) InvokeTool(ctx context.Context, serverID, toolName string, args map[string]any) (*mcpkit.ToolResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	start := time.Now()
	srv, err := s.findServer(serverID)
	if err != nil {
		s.recordInvocation(now, start, "/mcp/"+serverID, err, toolName, "", safeJSON(args), "", authKindFrom(ctx))
		return nil, err
	}
	if !srv.Enabled {
		err := fmt.Errorf("mcphub: MCP 服务器 %q 未启用，无法测试调用", srv.Name)
		s.recordInvocation(now, start, "/mcp/"+srv.Name, err, toolName, srv.Name, safeJSON(args), "", authKindFrom(ctx))
		return nil, err
	}
	endpoint := "/mcp/" + srv.Name
	tools, err := s.ToolView(endpoint)
	if err != nil {
		s.recordInvocation(now, start, endpoint, err, toolName, srv.Name, safeJSON(args), "", authKindFrom(ctx))
		return nil, err
	}
	var entry *ToolEntry
	for i := range tools {
		if tools[i].RawName == toolName || tools[i].Name == toolName {
			entry = &tools[i]
			break
		}
	}
	if entry == nil {
		err := fmt.Errorf("mcphub: 工具 %q 在服务器 %q 不可见", toolName, srv.Name)
		s.recordInvocation(now, start, endpoint, err, toolName, srv.Name, safeJSON(args), "", authKindFrom(ctx))
		return nil, err
	}
	return s.callEntry(ctx, *entry, args, endpoint)
}

// TestTools 从索引返回指定 server 的全部工具（含描述与完整 schema），不建连。
// 「测试工具」的工具列表/Schema 从此索引拿，与生产一致且零额外连接。
func (s *Service) TestTools(serverID string) []mcpkit.ToolInfo {
	tools, err := s.ensureTools()
	if err != nil {
		return nil
	}
	out := []mcpkit.ToolInfo{}
	for _, t := range tools {
		if t.ServerID == serverID {
			out = append(out, mcpkit.ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return out
}
