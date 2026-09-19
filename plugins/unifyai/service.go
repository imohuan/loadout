// Package unifyai 实现 UnifyAI 配置同步 CLI 的桥接服务：
// 把 Loadout 管理后台的「UnifyAI 配置同步」页面连接到 unifyai CLI
// （统一通过 `npx unifyai@latest -y` 运行，npx 自动安装，无需本地脚本），
// 提供平台能力列表（--list platforms --json）与指令执行（含实时日志流）。
package unifyai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"loadout/core/config"
	"loadout/core/deps"
	"loadout/core/procreg"
)

// OpenRouterMeta 对应 openrouter-models.json 中单个模型的元数据条目
// （unifyai --list metadata 从 https://openrouter.ai/api/v1/models 拉取后缓存）。
type OpenRouterMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Context   int64  `json:"context"`
	Output    int64  `json:"output"`
	Vision    bool   `json:"vision"`
	Reasoning bool   `json:"reasoning"`
}

// ModelSourceStatus 对应 UI「模型来源」卡片：OpenRouter 连接信息 + 元数据缓存状态。
type ModelSourceStatus struct {
	Kind           string `json:"kind"` // openrouter | none
	BaseURL        string `json:"baseUrl"`
	APIKeyMasked   string `json:"apiKeyMasked"` // 未配置为空串
	ModelCount     int    `json:"modelCount"`
	VisionCount    int    `json:"visionCount"`
	ReasoningCount int    `json:"reasoningCount"`
	CachedAt       string `json:"cachedAt"` // 缓存文件修改时间（RFC3339），无缓存为空
	Degraded       string `json:"degraded,omitempty"`
}

// openrouterBaseURL 是 unifyai --list metadata 拉取模型数据的公开端点。
const openrouterBaseURL = "https://openrouter.ai/api/v1"

// metadataCachePath 返回 OpenRouter 元数据缓存文件路径（~/.unifyai/cache/openrouter-models.json），
// 与 unifyai 源码 metadata-fetcher.mjs 的 CACHE_FILE 保持一致。
// 做成 var 便于测试覆盖到临时目录，与 mcpConfigPath / syncConfigPath 同模式。
var metadataCachePath = func() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".unifyai", "cache", "openrouter-models.json")
	}
	return "openrouter-models.json"
}

// MetadataCachePath / SetMetadataCachePath 暴露缓存路径的读取与替换，
// 供 admin-api 等外部包在测试里隔离真实用户目录（包内测试直接改 var）。
func MetadataCachePath() string { return metadataCachePath() }

func SetMetadataCachePath(fn func() string) { metadataCachePath = fn }

// ModelSource 读取 OpenRouter 元数据缓存（不经 CLI，快、离线可用）。
// 缓存缺失/损坏时返回 Kind=none（不报错），UI 据此提示先执行刷新。
func (s *Service) ModelSource() ModelSourceStatus {
	res := ModelSourceStatus{Kind: "openrouter", BaseURL: openrouterBaseURL}
	// API Key：OpenRouter /models 是公开端点，仅当配置了环境变量时回显掩码。
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		res.APIKeyMasked = maskSecret(key)
	}
	data, err := os.ReadFile(metadataCachePath())
	if err != nil {
		res.Kind = "none"
		res.Degraded = "元数据缓存不存在，请先点击「更新元数据」"
		return res
	}
	var metas []OpenRouterMeta
	if err := json.Unmarshal(data, &metas); err != nil {
		res.Kind = "none"
		res.Degraded = "元数据缓存解析失败，请重新「更新元数据」"
		return res
	}
	res.ModelCount = len(metas)
	for _, m := range metas {
		if m.Vision {
			res.VisionCount++
		}
		if m.Reasoning {
			res.ReasoningCount++
		}
	}
	if fi, err := os.Stat(metadataCachePath()); err == nil {
		res.CachedAt = fi.ModTime().Format(time.RFC3339)
	}
	return res
}

// maskSecret 把密钥掩码成 sk-xxxx****xxxx 形式（前后各留 4 位）。
func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// CatalogModel 「加载配置」下拉里的单个 OpenRouter 模型条目。
// 字段与聚合模型「模型配置」一一对应，前端选中后直接回填输入框。
type CatalogModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Context   int64  `json:"context"`
	Output    int64  `json:"output"`
	Vision    bool   `json:"vision"`
	Reasoning bool   `json:"reasoning"`
}

// CatalogModels 读 OpenRouter 元数据缓存（~/.unifyai/cache/openrouter-models.json），
// 返回可用于回填虚拟模型「模型配置」的模型清单。
// 缓存缺失/损坏时返回空列表（不报错），UI 据此提示先「更新元数据」。
// 空 id 的脏条目直接跳过，避免下拉出现无法识别的空行。
func (s *Service) CatalogModels() []CatalogModel {
	data, err := os.ReadFile(metadataCachePath())
	if err != nil {
		return nil
	}
	var metas []OpenRouterMeta
	if err := json.Unmarshal(data, &metas); err != nil {
		return nil
	}
	out := make([]CatalogModel, 0, len(metas))
	for _, m := range metas {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out = append(out, CatalogModel{
			ID:        m.ID,
			Name:      m.Name,
			Context:   m.Context,
			Output:    m.Output,
			Vision:    m.Vision,
			Reasoning: m.Reasoning,
		})
	}
	return out
}

// OpenCodexModel 对应 unifyai --list models --json 输出的单个模型。
type OpenCodexModel struct {
	Provider         string `json:"provider"`
	ModelID          string `json:"modelId"`
	DisplayName      string `json:"displayName"`
	ContextWindow    int64  `json:"contextWindow"`
	MaxOutputTokens  int64  `json:"maxOutputTokens"`
	SupportsVision   bool   `json:"supportsVision"`
	SupportsThinking bool   `json:"supportsThinking"`
}

// OpenCodexModelsResult 对应 unifyai --list models --json 的完整输出。
type OpenCodexModelsResult struct {
	Source               string           `json:"source"`
	ProxyURL             string           `json:"proxyUrl"`
	Port                 int              `json:"port"`
	HasAPIKey            bool             `json:"hasApiKey"`
	APIKeyPreview        string           `json:"apiKeyPreview"`
	ProviderCount        int              `json:"providerCount"`
	EnabledProviderCount int              `json:"enabledProviderCount"`
	RawCount             int              `json:"rawCount"`
	Degraded             bool             `json:"degraded"`
	DegradedReason       string           `json:"degradedReason"`
	ORMatchedCount       int              `json:"orMatchedCount"`
	ORTotal              int              `json:"orTotal"`
	Models               []OpenCodexModel `json:"models"`
	Count                int              `json:"count"`
	Error                string           `json:"error,omitempty"`
}

// OpenCodexModels 获取 OpenCodex 代理的模型列表（--list models --json）。
// enableVision=true 时追加 --enable-vision（强制所有模型标记为支持视觉）。
// CLI 不可用/代理不可达时返回 Degraded=true + 原因（不报错），保证页面可用。
func (s *Service) OpenCodexModels(enableVision bool) OpenCodexModelsResult {
	res, err := s.queryModels("models", enableVision)
	if err != nil {
		return OpenCodexModelsResult{Degraded: true, DegradedReason: err.Error()}
	}
	// 代理不可达（没有模型列表）时补一次 OpenRouter 名称匹配，见 enrichFromOpenRouter。
	enrichFromOpenRouter(&res)
	return res
}

// queryModels 执行 `--list <what> --json` 并解析出 models 对象。
// what = "models"（只查模型）或 "all"（平台 + 模型 + MCP + 元数据一起查）。
//
// enableVision 是**每次调用显式传入**的开关，绝不从 sync.json 读：
// sync.json 里那份可能停在某次同步留下的旧值（例如 enableVision:true），
// 而 CLI 的代理探测是「--enable-vision 才生效」的——旧值一旦被沿用，
// 代理关着时模型列表就恒为空，页面显示 0 个模型，且和 UI 开关状态对不上。
//
// 另外：CLI 请求 OpenCodex 代理只等 3 秒（config-loader.mjs 里的硬编码超时），
// 而代理「冷启动」——第一次被调用、或换个进程/连接来问——实测要 10 秒以上，
// 于是第一次查询必然超时、报「代理服务不可用」、模型列表为空，
// 紧接着的第二次查询因为代理已热就秒回。这正是「刚进页面是 0，点一下刷新就有了」的由来。
// 这里在判定失败且「一个模型都没拿到」时退避重试一次，把冷启动那一下吃掉。
func (s *Service) queryModels(what string, enableVision bool) (OpenCodexModelsResult, error) {
	// 先把代理问热，避免 CLI 内部 3 秒超时把冷启动掐断（见 warmOpenCodexProxy）。
	s.warmOpenCodexProxy()
	var last OpenCodexModelsResult
	for attempt := 0; attempt < queryModelsAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(queryModelsRetryDelay)
		}
		res, retryable, err := s.queryModelsOnce(what, enableVision)
		if err != nil {
			return OpenCodexModelsResult{}, err
		}
		last = res
		// 代理不可用且没拿到任何模型 → 大概率是冷启动超时，退避后再试一次。
		if !retryable {
			return res, nil
		}
		s.lg.Info("unifyai: 代理疑似冷启动超时，重试查询", "what", what, "attempt", attempt+1, "reason", res.DegradedReason)
	}
	return last, nil
}

// 查询重试策略：代理冷启动比 CLI 的 3 秒超时长，多给几次退避重试（热了之后秒回）。
// 做成 var 便于测试缩短等待。
var (
	queryModelsAttempts   = 3
	queryModelsRetryDelay = 500 * time.Millisecond
)

// warmProxyTimeout 是「预热代理」单次请求的等待上限。
// 比 CLI 内部的 3 秒超时宽裕得多，确保冷启动能真正跑完。
var warmProxyTimeout = 25 * time.Second

// warmOpenCodexProxy 先自己请求一次 OpenCodex 代理，把它的冷启动吃掉。
//
// 为什么需要：unifyai CLI 请求代理只等 3 秒（config-loader.mjs 硬编码），而代理
// 「冷启动」——第一次被问、或换个进程来问——实测要 10 秒以上。于是 CLI 那 3 秒必然
// 被掐断，报「代理服务不可用」、模型列表为空，页面「数据预览 → OpenCodex 模型」
// 就显示 0 个；紧接着再点一次代理已经热了，才正常显示。这就是用户看到的
// 「必须点刷新元数据才有数据」。
//
// 与其依赖 CLI 放宽超时（那份代码在 npm 包里，本仓库管不着），不如在调用 CLI 之前
// 由后端自己先把代理问热：预热请求不设 3 秒限制，冷启动多久就等多久；热了之后
// CLI 的 3 秒绰绰有余。CLI 不可用/代理不可达时静默跳过，不影响原有降级行为。
//
// proxyURL 为代理地址（与 CLI 用的同一个）；失败只记日志，不返回错误。
func (s *Service) warmOpenCodexProxy() {
	cfg, err := s.SyncConfig()
	if err != nil {
		return
	}
	// 从源配置里读代理地址与 API Key（与 CLI 的 tryFetchFromProxy 同源）。
	base := ""
	if src, ok := cfg["source"].(string); ok && src != "" {
		if data, err := os.ReadFile(expandHome(src)); err == nil {
			var oc struct {
				ProxyURL string `json:"proxyUrl"`
				Port     int    `json:"port"`
			}
			if json.Unmarshal(data, &oc) == nil {
				base = oc.ProxyURL
				if base == "" && oc.Port > 0 {
					base = fmt.Sprintf("http://localhost:%d/v1/models", oc.Port)
				}
			}
		}
	}
	if base == "" {
		base = "http://localhost:10100/v1/models"
	}

	ctx, cancel := context.WithTimeout(context.Background(), warmProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.lg.Debug("unifyai: 预热 OpenCodex 代理失败（忽略）", "url", base, "err", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	s.lg.Info("unifyai: 已预热 OpenCodex 代理", "url", base, "status", resp.StatusCode, "elapsed_ms", time.Since(start).Milliseconds())
}

// queryModelsOnce 执行一次查询；返回值 retryable 表示「代理疑似冷启动、值得重试」。
func (s *Service) queryModelsOnce(what string, enableVision bool) (res OpenCodexModelsResult, retryable bool, err error) {
	args := []string{"--list", what, "--json"}
	if enableVision {
		args = append(args, "--enable-vision")
	}
	if source := s.sourceFromSync(); source != "" {
		args = append(args, "--source", source)
	}
	lines, err := runCollect(args)
	if err != nil {
		s.lg.Warn("unifyai: --list "+what+" 执行失败", "err", err)
		return OpenCodexModelsResult{}, false, err
	}
	var wrapped struct {
		Models OpenCodexModelsResult `json:"models"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &wrapped); err != nil {
		s.lg.Warn("unifyai: 解析 --list "+what+" JSON 失败", "err", err)
		return OpenCodexModelsResult{}, false, fmt.Errorf("解析 --list %s 输出失败", what)
	}
	res = wrapped.Models
	if res.Error != "" {
		res.Degraded = true
		res.DegradedReason = res.Error
	}
	// 退化成「代理不可用」且一个模型都没拿到 = 冷启动超时的典型特征。
	retryable = res.Degraded && len(res.Models) == 0
	return res, retryable, nil
}

// enrichFromOpenRouter 在 OpenCodex 代理拿不到模型时，用本地 OpenRouter 元数据缓存
// 给模型补上「同类模型的上下文窗口」。
//
// 为什么需要：opencodex 对自家网关（Loadout）渠道只会拿到模型 id，拿不到上下文窗口，
// 于是 CLI 回退到 OpenRouter 按名字匹配。那个匹配依赖缓存里的字段是 openrouter.ai 的
// 原始命名（context_length / architecture.input_modalities / supported_parameters），
// 而刷新元数据的旧实现写的是压缩命名（context / vision / reasoning），
// 结果 CLI 只匹配上 id、其余全空 → UI 显示 0 个。这里用后端自己解析缓存（字段名由
// ModelSource 保证正确），把上下文字段补回去，让「同步后」与「刷新元数据后」的结果一致。
//
// enableVision=true 时同 CLI 语义，把所有模型的上下文窗口补满并标记支持视觉。
func enrichFromOpenRouter(res *OpenCodexModelsResult) {
	if len(res.Models) == 0 {
		return
	}
	missing := false
	for _, m := range res.Models {
		if m.ContextWindow <= 0 {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	byID, bySlug := openRouterContextIndex()
	if len(byID) == 0 {
		return
	}
	for i := range res.Models {
		if res.Models[i].ContextWindow > 0 {
			continue
		}
		if ctx, ok := lookupOpenRouterContext(byID, bySlug, res.Models[i].ModelID); ok {
			res.Models[i].ContextWindow = ctx
		}
	}
}

// openRouterContextIndex 解析 OpenRouter 元数据缓存，返回按完整 id 与「裸模型名」两种
// 键建立的名字 → 上下文窗口索引（后者对应 CLI 的 fallback 匹配方式）。
// 缓存缺失/损坏返回空表，调用方跳过补全。
func openRouterContextIndex() (byID, bySlug map[string]int64) {
	byID = map[string]int64{}
	bySlug = map[string]int64{}
	data, err := os.ReadFile(metadataCachePath())
	if err != nil {
		return byID, bySlug
	}
	var metas []OpenRouterMeta
	if err := json.Unmarshal(data, &metas); err != nil {
		return byID, bySlug
	}
	for _, m := range metas {
		if m.ID == "" || m.Context <= 0 {
			continue
		}
		id := strings.ToLower(m.ID)
		if _, ok := byID[id]; !ok {
			byID[id] = m.Context
		}
		slug := openRouterSlug(id)
		if _, ok := bySlug[slug]; !ok {
			bySlug[slug] = m.Context
		}
	}
	return byID, bySlug
}

// openRouterSlug 取 OpenRouter 模型 id 的最后一段并去掉日期/版本后缀：
// "anthropic/claude-sonnet-5-20260101" → "claude-sonnet-5"。
func openRouterSlug(id string) string {
	slug := id
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	// 去掉 :free / :nitro 之类的变体后缀。
	if i := strings.Index(slug, ":"); i > 0 {
		slug = slug[:i]
	}
	// 去掉尾部的 -YYYYMMDD（OpenRouter 的日期快照后缀）。
	if len(slug) > 9 && slug[len(slug)-9] == '-' {
		digits := true
		for _, r := range slug[len(slug)-8:] {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if digits {
			slug = slug[:len(slug)-9]
		}
	}
	return slug
}

// lookupOpenRouterContext 按完整 id 优先、裸名兜底查上下文窗口。
func lookupOpenRouterContext(byID, bySlug map[string]int64, modelID string) (int64, bool) {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return 0, false
	}
	if ctx, ok := byID[id]; ok {
		return ctx, true
	}
	slug := openRouterSlug(id)
	if ctx, ok := bySlug[slug]; ok {
		// 裸名匹配：只有 id 本身没有 provider 前缀（或前缀被剥掉后同名）时才认，
		// 避免 "openai/gpt-5" 之类的裸名误配到别家同名模型。
		if strings.Contains(id, "/") {
			return 0, false
		}
		return ctx, true
	}
	// 带前缀的模型名：用 provider 前缀 + 裸名再试一次完整 id 命中。
	if i := strings.LastIndex(id, "/"); i >= 0 {
		if ctx, ok := byID[id[i+1:]]; ok {
			return ctx, true
		}
	}
	return 0, false
}

// OpenCodexModelsLive 重新探测一次 OpenCodex 代理，原样返回 `--list models --json`
// 的完整输出（**不做任何补全**），用于排查「UI 显示 0 个模型」类问题：可以直接看到
// CLI 自己写进去的字段名（压缩命名 context / vision / reasoning）与 UI 期望的字段名
// （contextWindow / supportsVision / supportsThinking）之间的对照。
// 慢（每次都要连代理），仅供诊断。
func (s *Service) OpenCodexModelsLive(enableVision bool) json.RawMessage {
	args := []string{"--list", "models", "--json"}
	if enableVision {
		args = append(args, "--enable-vision")
	}
	if source := s.sourceFromSync(); source != "" {
		args = append(args, "--source", source)
	}
	lines, err := runCollect(args)
	if err != nil {
		s.lg.Warn("unifyai: --list models 执行失败", "err", err)
		return mustJSON(map[string]any{"error": err.Error()})
	}
	raw := strings.TrimSpace(strings.Join(lines, "\n"))
	if raw == "" || !json.Valid([]byte(raw)) {
		return mustJSON(map[string]any{"error": "CLI 输出不是合法 JSON", "raw": raw})
	}
	return json.RawMessage(raw)
}

// mustJSON 把值编码成 JSON（编码失败返回一个说明用的对象字面量）。
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"error":"encode failed"}`)
	}
	return b
}

// Platform 对应 `unifyai --list platforms --json` 输出的单个平台（附录 B.1）。
type Platform struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SupportsModels bool   `json:"supportsModels"`
	ModelStatus    string `json:"modelStatus"`
	SupportsMcp    bool   `json:"supportsMcp"`
	McpStatus      string `json:"mcpStatus"`
	ConfigPath     string `json:"configPath"`
	ConfigFormat   string `json:"configFormat"`
}

// ListPlatformsResult 对应 --list platforms --json 的完整输出。
type ListPlatformsResult struct {
	Platforms []Platform `json:"platforms"`
}

// McpMatrixServer 对应 --list mcp --json 中单个服务器的条目（name/enabled/config 原始配置）。
type McpMatrixServer struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// McpSourceState 对应 --list mcp --json 的 source 字段（源 mcp.json）。
type McpSourceState struct {
	Path    string            `json:"path"`
	Servers []McpMatrixServer `json:"servers"`
}

// McpPlatformState 对应 --list mcp --json 中单个平台的状态（可读性 + 服务器开关列表）。
type McpPlatformState struct {
	Platform   string            `json:"platform"`
	Name       string            `json:"name"`
	ConfigPath string            `json:"configPath"`
	Readable   bool              `json:"readable"`
	Servers    []McpMatrixServer `json:"servers"`
}

// McpMatrixResult 对应 --list mcp --json 的完整输出（源 + 各平台），供前端渲染同步矩阵。
type McpMatrixResult struct {
	Source    *McpSourceState    `json:"source"`
	Platforms []McpPlatformState `json:"platforms"`
}

// Service 是 UnifyAI CLI 桥接服务。
type Service struct {
	lg     *slog.Logger
	runner *RunRunner
	mu     sync.Mutex
	// pendingArgs 保存「启动任务前」由 handler 设置的 CLI 参数，
	// runner 启动任务时取出（takePendingArgs 一次性消费）。
	pendingArgs []string
	// pendingID 保存前端传入的任务进程 ID（空=自动生成），与 pendingArgs 一并透传。
	pendingID string
}

// NewService 创建服务。
func NewService(lg *slog.Logger) *Service {
	svc := &Service{lg: lg}
	svc.runner = newRunRunner(svc)
	return svc
}

// SetArgs 设置下一次任务的 CLI 参数（启动前调用）。
func (s *Service) SetArgs(args []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingArgs = append([]string(nil), args...)
}

// SetID 设置下一次任务的进程 ID（前端 task id，启动前调用）。
func (s *Service) SetID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingID = id
}

// takePendingID 取出并清空待执行进程 ID。
func (s *Service) takePendingID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.pendingID
	s.pendingID = ""
	return id
}

// takePendingArgs 取出并清空待执行参数。
func (s *Service) takePendingArgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	args := s.pendingArgs
	s.pendingArgs = nil
	return args
}

// Subscribe 订阅任务日志流（SSE 用）；无任务在跑时自动启动一个。
func (s *Service) Subscribe() (<-chan RunEvent, error) {
	return s.runner.Subscribe()
}

// PlatformInfo 解析 `unifyai --list platforms --json`，返回平台能力列表。
// CLI 不可用时回落到内置默认平台（与 UI 静态数据一致），保证页面可用。
func (s *Service) PlatformInfo() (ListPlatformsResult, error) {
	lines, err := runCollect([]string{"--list", "platforms", "--json"})
	if err != nil {
		s.lg.Warn("unifyai: --list platforms 失败，回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	var res ListPlatformsResult
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &res); err != nil {
		s.lg.Warn("unifyai: 解析平台列表 JSON 失败，回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	if len(res.Platforms) == 0 {
		return defaultPlatforms(), nil
	}
	return res, nil
}

// AllConfigResult 对应 `unifyai --list all --json` 的完整输出（前端一次获取全部配置）。
// Models / Metadata 结构复杂且随 CLI 演进，用 RawMessage 透传，前端直接消费。
type AllConfigResult struct {
	Platforms []Platform      `json:"platforms"`
	Models    json.RawMessage `json:"models"`
	Mcp       McpMatrixResult `json:"mcp"`
	Metadata  json.RawMessage `json:"metadata"`
}

// ListAll 解析 `unifyai --list all --json`，返回平台 + 模型 + MCP 矩阵 + 元数据缓存状态，
// 前端初始化一次拉全（替代分别调 platforms / opencodex-models / mcp-matrix）。
// enableVision 由页面传入（不再从 sync.json 读旧值，见 queryModels 注释）。
// 与 queryModels 同样对「代理冷启动超时」退避重试，见该函数注释。
// CLI 不可用时返回空结构（前端回落内置默认），不报错。
func (s *Service) ListAll(enableVision bool) (AllConfigResult, error) {
	// 先把代理问热，避免 CLI 内部 3 秒超时把冷启动掐断（见 warmOpenCodexProxy）。
	s.warmOpenCodexProxy()
	var last AllConfigResult
	for attempt := 0; attempt < queryModelsAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(queryModelsRetryDelay)
		}
		res, retryable := s.listAllOnce(enableVision)
		last = res
		if !retryable {
			return res, nil
		}
		s.lg.Info("unifyai: --list all 疑似代理冷启动超时，重试", "attempt", attempt+1)
	}
	return last, nil
}

// listAllOnce 执行一次 `--list all --json`；retryable 表示「代理疑似冷启动、值得重试」。
func (s *Service) listAllOnce(enableVision bool) (res AllConfigResult, retryable bool) {
	args := []string{"--list", "all", "--json"}
	if enableVision {
		args = append(args, "--enable-vision")
	}
	if source := s.sourceFromSync(); source != "" {
		args = append(args, "--source", source)
	}
	lines, err := runCollect(args)
	if err != nil {
		s.lg.Warn("unifyai: --list all 执行失败", "err", err)
		return AllConfigResult{}, false
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &res); err != nil {
		s.lg.Warn("unifyai: 解析 --list all JSON 失败", "err", err)
		return AllConfigResult{}, false
	}
	// 代理不可达且一个模型都没拿到 → 只会是冷启动超时，重试可救。
	var models OpenCodexModelsResult
	if len(res.Models) > 0 {
		if err := json.Unmarshal(res.Models, &models); err == nil {
			retryable = models.Degraded && len(models.Models) == 0
			// 代理不可达时用本地 OpenRouter 缓存补模型上下文，与 OpenCodexModels 同款兜底。
			enrichFromOpenRouter(&models)
			if raw, err := json.Marshal(models); err == nil {
				res.Models = raw
			}
		}
	}
	return res, retryable
}

// syncConfigPath 返回同步配置文件路径（前端把当前 UI 状态落盘后以 --config 引用）。
// 做成 var 便于测试覆盖到临时目录，与 mcpConfigPath 同模式。
var syncConfigPath = func() string {
	return filepath.Join(osConfigHome(), "sync.json")
}

// osConfigHome 返回 unifyai 配置目录（~/.unifyai），与 CLI 的 resolveSourceMcp 一致。
func osConfigHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".unifyai"
	}
	return filepath.Join(home, ".unifyai")
}

// SaveSyncConfig 把同步配置 JSON 写入 ~/.unifyai/sync.json（前端保存当前 UI 状态用）。
func (s *Service) SaveSyncConfig(cfg []byte) (string, error) {
	p := syncConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, cfg, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// SyncConfigPath 返回同步配置文件路径（前端命令预览展示用）。
func (s *Service) SyncConfigPath() string {
	return syncConfigPath()
}

// SyncConfig 读取 ~/.unifyai/sync.json 的完整内容。文件不存在或解析失败时
// 返回空 map（不报错），方便调用方安全地取字段（如 source）。
func (s *Service) SyncConfig() (map[string]any, error) {
	p := syncConfigPath()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		s.lg.Warn("unifyai: 解析 sync.json 失败", "err", err)
		return map[string]any{}, nil
	}
	return cfg, nil
}

// UpdateSource 只更新 sync.json 里的 source 字段（模型源配置路径），其余字段原样保留。
// 文件不存在时以空配置起步新建。返回写入后的文件路径。
func (s *Service) UpdateSource(source string) (string, error) {
	cfg, err := s.SyncConfig()
	if err != nil {
		return "", err
	}
	cfg["source"] = source
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return s.SaveSyncConfig(raw)
}

// sourceFromSync 读取 sync.json 里持久化的 source（模型源配置路径），
// 并展开开头的 ~ 为绝对路径。返回空串表示未配置（调用方不拼 --source）。
// 注意：unifyai CLI 的 --list 查询路径不会自行展开 ~（只有同步 runFullSync 会），
// 因此必须在这里转成绝对路径，否则 CLI 会把字面 ~ 当作路径导致「配置文件不存在」。
func (s *Service) sourceFromSync() string {
	cfg, err := s.SyncConfig()
	if err != nil {
		return ""
	}
	src, ok := cfg["source"].(string)
	if !ok || src == "" {
		return ""
	}
	return expandHome(src)
}

// expandHome 把开头的 ~ 展开为当前用户主目录；其余原样返回。
func expandHome(src string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return src
	}
	switch {
	case src == "~":
		return home
	case strings.HasPrefix(src, "~/") || strings.HasPrefix(src, "~\\"):
		return filepath.Join(home, src[2:])
	default:
		return src
	}
}

// ListMcpMatrix 解析 `unifyai --list mcp --json`，返回源 mcp.json + 各平台 MCP 开关状态，
// 供前端「MCP 同步矩阵」渲染（行=去重服务器，列=平台，勾选=该平台开启）。
// CLI 不可用 / 执行失败时返回空结果（前端回落内置默认），不报错。
func (s *Service) ListMcpMatrix() (McpMatrixResult, error) {
	lines, err := runCollect([]string{"--list", "mcp", "--json"})
	if err != nil {
		s.lg.Warn("unifyai: --list mcp 执行失败", "err", err)
		return McpMatrixResult{}, nil
	}
	// 新版 CLI 输出 {mcp: {source, platforms}}，多包一层 mcp。
	var wrapped struct {
		Mcp McpMatrixResult `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &wrapped); err != nil {
		s.lg.Warn("unifyai: 解析 --list mcp JSON 失败", "err", err)
		return McpMatrixResult{}, nil
	}
	return wrapped.Mcp, nil
}

// Run 执行一次 unifyai 指令（args 为 CLI 参数，如 ["--all", "--dry-run"]），
// 实时把 stdout/stderr 逐行回传给 onLog。返回进程退出错误（nil=成功）。
// id 可传前端 task id（空则自动生成），用于按 id 关联/查询该进程。
func (s *Service) Run(id string, args []string, onLog func(string)) error {
	if onLog == nil {
		onLog = func(string) {}
	}
	cmd, base, err := resolveCmd()
	if err != nil {
		return err
	}
	onLog(fmt.Sprintf("执行: %s %s", displayCmd(cmd, base), strings.Join(args, " ")))
	full := append(append([]string{}, base...), args...)
	h, err := procreg.Run(procreg.Options{
		ID:    id,
		Name:  "UnifyAI 同步",
		Kind:  "unifyai",
		Cmd:   cmd,
		Args:  full,
		OnLog: onLog,
	})
	if err != nil {
		return fmt.Errorf("unifyai: %w", err)
	}
	if err := h.Wait(); err != nil {
		return fmt.Errorf("unifyai: %w", err)
	}
	return nil
}

// runCollect 用 procreg 统一执行一条 unifyai 查询命令并收集全部输出行。
// 走 procreg 让命令出现在全局进程面板（ProcessFooter 可见、可终止），kind 统一为 "unifyai"。
// 命令入口统一经 resolveCmd()（含 LOADOUT_UNIFYAI_CMD / 全局指令 / npx 优先级），保证不绕过配置。
// 通用实现提取到 procreg.RunCollect，并通过 EnvWithPathPrefix 补全命令目录到 PATH（确保 npx 能找到同目录 node）。
func runCollect(args []string) ([]string, error) {
	cmd, base, err := resolveCmd()
	if err != nil {
		return nil, err
	}
	full := append(append([]string{}, base...), args...)
	return procreg.RunCollect("UnifyAI 查询", "unifyai", cmd, full, procreg.EnvWithPathPrefix(filepath.Dir(cmd)))
}

// resolveCmd 返回 unifyai 执行入口（cmd + 固定前缀参数）。
// 优先级：
//  1. LOADOUT_UNIFYAI_CMD（config.UnifyaiCmd）配置的命令行，按 shell 风格分词
//     （双引号可包含空格的路径），例如 `node "D:/Code/Git/unifyai/src/cli.mjs"`；
//  2. 默认统一走 `npx -y unifyai@latest`。
//
// npx 会自动拉取/复用 unifyai 包，无需本地仓库或全局安装，只要机器有 Node.js 环境。
// 注意：`-y`（跳过 npx 的 "Ok to proceed?" 安装确认）必须放在包名【前面】，
// 放在包名后会作为 unifyai 的参数传入，导致 `error: unknown option '-y'`。
func resolveCmd() (string, []string, error) {
	// 依赖开关：库已全局安装 且 开关打开 → 用全局指令，不用 npx。
	if deps.UseGlobal && deps.GlobalAvailable("unifyai") {
		return "unifyai", nil, nil
	}
	if cfg := config.UnifyaiCmd; cfg != "" {
		parts := splitCommandLine(cfg)
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("LOADOUT_UNIFYAI_CMD 无法解析: %q", cfg)
		}
		return parts[0], parts[1:], nil
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		// PATH 中找不到 npx（后台服务/systemd 启动时环境不完整），
		// 按常见安装位置兜底，覆盖 Windows 与 Linux。
		for _, p := range npxCandidates() {
			if fileExists(p) {
				return p, []string{"-y", "unifyai@latest"}, nil
			}
		}
		return "", nil, fmt.Errorf("未找到 npx：请先安装 Node.js（unifyai 通过 npx 自动运行，无需额外安装）")
	}
	return npx, []string{"-y", "unifyai@latest"}, nil
}

// splitCommandLine 按空白分词，支持双引号包裹的路径/含空格参数（如 "C:/Program Files/..."）。
func splitCommandLine(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// npxCandidates 按常见安装位置枚举 npx 完整路径（含 Windows 与 Linux）。
// 包级变量，测试可替换。
var npxCandidates = func() []string {
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(os.Getenv("APPDATA"), "npm", "npx.cmd"),
			"C:/Program Files/nodejs/npx.cmd",
		}
	}
	// Linux: nvm、fnm 版本目录全部枚举，另加 volta/asdf/系统位置。
	var cands []string
	if home, err := os.UserHomeDir(); err == nil {
		// nvm: ~/.nvm/versions/node/<ver>/bin/npx。
		versionsDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(versionsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					cands = append(cands, filepath.Join(versionsDir, e.Name(), "bin", "npx"))
				}
			}
		}
		cands = append(cands,
			filepath.Join(home, ".nvm", "current", "bin", "npx"),
			filepath.Join(home, ".local", "bin", "npx"),
			filepath.Join(home, ".volta", "bin", "npx"),
			filepath.Join(home, ".asdf", "shims", "npx"),
		)
		// fnm: ~/.local/share/fnm/<ver>/installation/bin/npx。
		fnmDir := filepath.Join(home, ".local", "share", "fnm")
		if entries, err := os.ReadDir(fnmDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					cands = append(cands, filepath.Join(fnmDir, e.Name(), "installation", "bin", "npx"))
				}
			}
		}
	}
	return append(cands,
		"/usr/local/bin/npx",
		"/usr/bin/npx",
		"/opt/node/bin/npx",
		"/usr/local/node/bin/npx",
	)
}

// fileExists 判断路径存在且是普通文件。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// displayCmd 生成人类可读的命令展示。
func displayCmd(cmd string, base []string) string {
	return "npx -y unifyai@latest"
}

// defaultPlatforms 内置默认平台（CLI 不可用时的 UI 兜底，与附录 B.1 一致）。
func defaultPlatforms() ListPlatformsResult {
	return ListPlatformsResult{Platforms: []Platform{
		{ID: "opencode", Name: "OpenCode", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.config/opencode/opencode.json", ConfigFormat: "jsonc"},
		{ID: "codex", Name: "Codex", SupportsModels: false, ModelStatus: "not_supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.codex/config.toml", ConfigFormat: "toml"},
		{ID: "claudecode", Name: "Claude Code", SupportsModels: false, ModelStatus: "not_supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.claude.json", ConfigFormat: "json"},
		{ID: "reasonix", Name: "Reasonix", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "not_implemented", ConfigPath: "~/AppData/Roaming/reasonix/config.toml", ConfigFormat: "toml"},
		{ID: "penguin", Name: "PenguinHarness", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.penguin/data/default_project/.project_config.toml", ConfigFormat: "toml"},
		{ID: "workbuddy", Name: "WorkBuddy", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.workbuddy/models.json", ConfigFormat: "json"},
	}}
}

// ============ MCP 服务器列表（直接读写 mcp.json，不经 CLI）============

// McpServer 单个 MCP 服务器（mcp.json 条目）。
// 写回时只写 disabled 字段：unifyai 同步时 loadMcpConfig 以
// `if (!config.disabled)` 过滤服务器（enabled 仅 normalizeMcp 兜底，不再双写）。
type McpServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"` // local | remote
	Enabled bool              `json:"enabled"`
	Command []string          `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// mcpRawEntry mcp.json 中 mcpServers 的单条原始结构（兼容 string/数组 command）。
type mcpRawEntry struct {
	Type     string            `json:"type"`
	Enabled  *bool             `json:"enabled"`
	Disabled *bool             `json:"disabled"`
	Command  json.RawMessage   `json:"command"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Env      map[string]string `json:"env"`
}

// McpServers 读取 mcp.json（优先级 cwd/mcp.json > ~/.unifyai/mcp.json），
// 返回 UI 使用的服务器列表（含 disabled，由 UI 决定参与同步）。
// 文件不存在返回空列表（不报错）。
func (s *Service) McpServers() ([]McpServer, error) {
	path := mcpConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []McpServer{}, nil
		}
		return nil, fmt.Errorf("unifyai: 读取 mcp.json 失败: %w", err)
	}
	var raw struct {
		McpServers map[string]mcpRawEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unifyai: 解析 mcp.json 失败: %w", err)
	}
	servers := make([]McpServer, 0, len(raw.McpServers))
	for name, entry := range raw.McpServers {
		enabled := true
		if entry.Enabled != nil && !*entry.Enabled {
			enabled = false
		}
		if entry.Disabled != nil && *entry.Disabled {
			enabled = false
		}
		servers = append(servers, McpServer{
			Name:    name,
			Type:    entry.Type,
			Enabled: enabled,
			Command: parseCommandField(entry.Command),
			URL:     entry.URL,
			Headers: entry.Headers,
			Env:     entry.Env,
		})
	}
	// 稳定排序（按名称），UI 顺序一致。
	sortMcpServers(servers)
	return servers, nil
}

// SaveMcpServers 把服务器列表写回 mcp.json（优先写已存在的 cwd/mcp.json，
// 否则写 ~/.unifyai/mcp.json，目录不存在自动创建）。
func (s *Service) SaveMcpServers(servers []McpServer) error {
	path := mcpConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("unifyai: 创建配置目录失败: %w", err)
	}
	m := make(map[string]any, len(servers))
	for _, srv := range servers {
		// 只写 disabled：unifyai 同步时以 `if (!config.disabled)` 过滤（loadMcpConfig），
		// enabled 不写（normalizeMcp 的 enabled 判断只是兜底，避免双字段冗余）。
		entry := map[string]any{
			"type":     srv.Type,
			"disabled": !srv.Enabled,
		}
		if len(srv.Command) > 0 {
			entry["command"] = srv.Command
		}
		if srv.URL != "" {
			entry["url"] = srv.URL
		}
		if len(srv.Headers) > 0 {
			entry["headers"] = srv.Headers
		}
		if len(srv.Env) > 0 {
			entry["env"] = srv.Env
		}
		m[srv.Name] = entry
	}
	doc := map[string]any{"mcpServers": m}
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("unifyai: 编码 mcp.json 失败: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("unifyai: 写入 mcp.json 失败: %w", err)
	}
	s.lg.Info("unifyai: 已保存 mcp.json", "path", path, "count", len(servers))
	return nil
}

// mcpConfigPath 解析 mcp.json 路径：cwd 优先，回退 ~/.unifyai/mcp.json（写入时创建）。
// 包级变量，测试可替换。
var mcpConfigPath = func() string {
	if p, err := os.Getwd(); err == nil {
		if c := filepath.Join(p, "mcp.json"); fileExists(c) {
			return c
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".unifyai", "mcp.json")
	}
	return "mcp.json"
}

// parseCommandField 兼容 command 为数组或字符串两种写法。
func parseCommandField(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && strings.TrimSpace(str) != "" {
		return []string{str}
	}
	return nil
}

// sortMcpServers 按名称排序（稳定展示顺序）。
func sortMcpServers(servers []McpServer) {
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
}
