// Package translate 实现翻译能力插件：把英文的 MCP 工具描述、skill 描述等文本，
// 通过大模型 API 批量翻译成目标语言，翻译结果持久化到 SQLite 作为缓存。
//
// 核心设计：
//   - 翻译走 model-gateway 的 ForwardSubRequest（不自建 http.Client），自动获得
//     request-log / 额度 / failover；不设 channel_candidates，让网关按目标模型自动路由。
//   - 缓存用「内容 hash」当 key：改一个空格只会让那一小块失效重翻，其余命中。
//   - 翻译粒度按「空行分大段 → 段内按句切小块」，同批小块合并进一次大模型请求省调用。
//   - 对外批量接口用 SSE 流式推 {done,total,item} 进度，前端进度条跟这个走。
//   - 表字段带 source_type / source_id / type，为后续「技能 hover 总结」扩展预留。
package translate

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	mcphub "loadout/plugins/mcp-hub"
	gatewaykeys "loadout/plugins/gateway-keys"
)

// ServiceName 是本插件在服务容器里的注册名。
const ServiceName = "translate"

// TranslationType 翻译内容类型：translate=翻译，summary=总结（扩展预留）。
type TranslationType string

const (
	TypeTranslate TranslationType = "translate"
	TypeSummary   TranslationType = "summary"
)

// SourceType 文本来源类型。
type SourceType string

const (
	SourceMCP    SourceType = "mcp"
	SourceSkill  SourceType = "skill"
	SourceCustom SourceType = "custom"
)

// Translation 一条翻译记录（translations 表）。
type Translation struct {
	ID             int64           `json:"id"`
	Hash           string          `json:"hash"`
	SourceText     string          `json:"source_text"`
	TranslatedText string          `json:"translated_text"`
	SourceType     SourceType      `json:"source_type"`
	SourceID       string          `json:"source_id"`
	Key            string          `json:"key"`
	TargetLang     string          `json:"target_lang"`
	Model          string          `json:"model"`
	Type           TranslationType `json:"type"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// TranslateRequest 单条/多条翻译请求。
type TranslateRequest struct {
	// SourceText 待翻译文本（单条）。
	SourceText string `json:"source_text"`
	// Texts 多条文本（批量，用于合并进一次大模型请求）。
	Texts []string `json:"texts"`
	// TargetLang 目标语言，如 "zh-CN" / "zh" / "ja"。
	TargetLang string `json:"target_lang"`
	// Model 目标模型（provider 支持模型的 id，含虚拟模型）。
	Model string `json:"model"`
	// Prompt 自定义翻译提示词；空则用默认。
	Prompt string `json:"prompt"`
	// SourceType / SourceID / Key 来源定位（写入缓存表）。
	SourceType SourceType      `json:"source_type"`
	SourceID   string          `json:"source_id"`
	Key        string          `json:"key"`
	Type       TranslationType `json:"type"`
}

// TranslateResponse 单条/多条翻译响应。
type TranslateResponse struct {
	// Texts 译文列表，与请求 Texts 一一对应。
	Texts []string `json:"texts"`
	// Text 单条译文（SourceText 非空时）。
	Text string `json:"text"`
}

// SourceItem 一个可翻译来源（设置页来源清单）。
type SourceItem struct {
	SourceType  SourceType      `json:"source_type"`
	SourceID    string          `json:"source_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	// InputSchema MCP 工具的完整 JSON Schema（参数配置）；skill 为空。
	InputSchema map[string]any `json:"input_schema,omitempty"`
	// Params 从 InputSchema 提取的可翻译参数项（name/title/description），用于参数翻译展示。
	Params []ParamItem `json:"params,omitempty"`
	Translated bool `json:"translated"` // 是否已有译文（缓存命中）
}

// ParamItem 一个可翻译的参数项。
type ParamItem struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ProgressEvent SSE 批量翻译进度事件。
type ProgressEvent struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Index   int    `json:"index"`
	Text    string `json:"text,omitempty"` // 该条译文（可选）
	Error   string `json:"error,omitempty"`
	Finished bool  `json:"finished"`
}

// Plugin 是 translate 插件的实现。
type pluginImpl struct{}

// New 创建 translate 插件实例。
func New() plugin.Plugin { return &pluginImpl{} }

// Manifest 声明依赖与提供：依赖 store/db/logger/model-gateway/mcp-hub/gateway-keys。
func (p *pluginImpl) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "translate",
		Version: "0.1.0",
		Inject:  []string{"store", "db", "logger", "model-gateway", "mcp-hub", "gateway-keys"},
		Provide: []string{ServiceName},
	}
}

// Apply 装配插件：取依赖服务，建表，构造 Service，注册路由。
func (p *pluginImpl) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	database, ok := ctx.Get("db").(*sql.DB)
	if !ok || database == nil {
		return fmt.Errorf("translate: missing db service")
	}
	gw, ok := ctx.Get("model-gateway").(*modelgateway.Service)
	if !ok || gw == nil {
		return fmt.Errorf("translate: missing model-gateway service")
	}
	hub, ok := ctx.Get("mcp-hub").(*mcphub.Service)
	if !ok || hub == nil {
		return fmt.Errorf("translate: missing mcp-hub service")
	}
	keys, ok := ctx.Get("gateway-keys").(*gatewaykeys.Manager)
	if !ok || keys == nil {
		return fmt.Errorf("translate: missing gateway-keys service")
	}

	if err := migrate(database); err != nil {
		return fmt.Errorf("translate: migrate: %w", err)
	}
	repo, err := db.NewRepository(database)
	if err != nil {
		return fmt.Errorf("translate: 初始化仓储失败: %w", err)
	}
	svc := NewService(st, repo, database, lg, gw, hub, keys)
	ctx.Set(ServiceName, svc)

	ctx.RegisterRoute(plugin.RouteSpec{Method: "POST", Pattern: "/api/translate", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleTranslate)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: "POST", Pattern: "/api/translate/lookup", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleLookup)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: "POST", Pattern: "/api/translate/batch", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleBatch)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: "GET", Pattern: "/api/translate/batch/status", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleBatchStatus)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: "POST", Pattern: "/api/translate/batch/cancel", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleBatchCancel)})
	ctx.RegisterRoute(plugin.RouteSpec{Method: "GET", Pattern: "/api/translate/sources", Auth: plugin.AuthSession, Handler: http.HandlerFunc(svc.handleSources)})
	ctx.RegisterCheck("translate-config", func() []plugin.Issue { return nil })
	return nil
}

// migrate 幂等建表（参照 mcp-hub stats.go 的插件内迁移，不进核心 migrate.go）。
func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS translations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		hash TEXT NOT NULL,
		source_text TEXT NOT NULL,
		translated_text TEXT NOT NULL,
		source_type TEXT NOT NULL DEFAULT 'custom',
		source_id TEXT NOT NULL DEFAULT '',
		key TEXT NOT NULL DEFAULT '',
		target_lang TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'translate',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`)
	if err != nil {
		return err
	}
	// hash 需唯一，才能让 save() 的 ON CONFLICT(hash) 生效；
	// 旧库可能建过同名普通索引，先删再建唯一索引。
	_, _ = db.Exec(`DROP INDEX IF EXISTS idx_translations_hash`)
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_translations_hash ON translations(hash)`)
	return err
}
