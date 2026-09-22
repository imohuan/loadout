package failurerules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"loadout/core/config"
)

// headerRuleAI AI 兜底请求的标记 header（本机 loopback 专用）。
const headerRuleAI = "X-Loadout-Rule-AI"

// AI 缓存 TTL：cooldown 类短缓存（到期允许重新判定），disable 类长缓存。
const (
	aiCacheTTLCooldown = 2 * time.Minute
	aiCacheTTLDisable  = 30 * time.Minute
)

// cachedDecision 带 TTL 的缓存条目。
type cachedDecision struct {
	d   Decision
	at  time.Time
	ttl time.Duration
}

// AIResolver AI 兜底判定器：调用网关自身 /v1/chat/completions（指定小模型）
// 分析错误证据，返回裁决。失败/超时/未配置时 Resolve 返回 ok=false。
type AIResolver struct {
	mu          sync.Mutex
	model       string // settings.rule_ai_model；空 = 关闭
	skKey       string // 静态 SK key（兼容）
	baseURL     string // 网关自身地址
	timeout     time.Duration
	decisions   sync.Map      // fingerprint -> cachedDecision
	keyProvider func() string // 动态 SK key 解析（优先于 skKey）
}

func NewAIResolver(model, skKey, baseURL string) *AIResolver {
	return &AIResolver{model: model, skKey: skKey, baseURL: baseURL, timeout: defaultAITimeout}
}

// defaultAITimeout AI 兜底判定超时。实测推理型小模型（如 glm-5.3-flash）返回
// 完整 JSON 需要 9~40s，早期写死的 8s 会让每次判定都超时被丢弃（表现为
// 「AI 兜底永远 ok=false」，看起来像功能没接上）。这里给足余量。
// 失败路径上的等待有代价：调用方（RecordFailure）是同步的，超时过长会拖慢
// 用户请求的失败返回。30s 是「够推理模型答完」与「不把请求挂太久」的折中。
const defaultAITimeout = 30 * time.Second

// SetModel 热更新 AI 模型（设置页保存后调用；空 = 关闭）。
func (a *AIResolver) SetModel(model string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = strings.TrimSpace(model)
}

// SetKeyProvider 注入 SK key 明文解析器（每次 Resolve 时调用，兼容 key 轮换）。
func (a *AIResolver) SetKeyProvider(fn func() string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keyProvider = fn
}

func (a *AIResolver) resolveKey() string {
	if a.keyProvider != nil {
		if k := a.keyProvider(); k != "" {
			return k
		}
	}
	return a.skKey
}

func (a *AIResolver) currentModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// Enabled 返回 AI 兜底是否启用。
func (a *AIResolver) Enabled() bool { return a != nil && a.currentModel() != "" }

// AIModel 返回当前使用的兜底模型名（空 = 未配置）。供上层记录/展示用。
func (a *AIResolver) AIModel() string { return a.currentModel() }

// cached 取缓存判定（带 TTL 过期）。
func (a *AIResolver) cached(fp string) (Decision, bool) {
	v, ok := a.decisions.Load(fp)
	if !ok {
		return Decision{}, false
	}
	c := v.(cachedDecision)
	if time.Since(c.at) > c.ttl {
		a.decisions.Delete(fp)
		return Decision{}, false
	}
	return c.d, true
}

// aiVerdictSchema AI 返回的结构。
type aiVerdictSchema struct {
	Verdict         string `json:"verdict"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
	Recover         string `json:"recover,omitempty"`
	Reason          string `json:"reason"`
}

// chat 用兜底模型跑一次非流式对话，返回去围栏后的正文。
//
// 超时取「resolver 默认」与「调用方 context 剩余时间」中更宽的那个：
// AI 判定（Resolve）在失败链路上，宁快勿慢，用 defaultAITimeout；
// AI 生成规则（Author）是后台任务，提示词更长、推理更久，调用方会传入更长的
// context。若一律套 30s，author 每轮都会在拿到响应前超时（实测踩过）。
//
// AI 判定（Resolve）与 AI 生成规则（作者 Author）都走这一条通道：同一个模型、
// 同一把 SK key、同一份超时与防递归 header。抽出来避免两处各写一遍 HTTP 细节
// （此前 CreateDraft 那类「复制粘贴漏改列名」的坑就是这么来的）。
func (a *AIResolver) chat(ctx context.Context, prompt string) (string, error) {
	model := a.currentModel()
	if model == "" {
		return "", fmt.Errorf("failure-rules: AI 兜底模型未配置")
	}
	skKey := a.resolveKey()
	if skKey == "" {
		return "", fmt.Errorf("failure-rules: 无可用 SK key，AI 不可用")
	}
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(a.baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+skKey)
	req.Header.Set(headerRuleAI, "1")

	resp, err := (&http.Client{Timeout: chatTimeout(ctx, a.timeout)}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failure-rules: AI 返回 %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("failure-rules: AI 响应解析失败")
	}
	return stripCodeFence(chatResp.Choices[0].Message.Content), nil
}

// chatTimeout 单次 AI 对话的 HTTP 超时：取默认值与调用方 deadline 中更宽的那个。
// 调用方没设 deadline 时用默认值。
func chatTimeout(ctx context.Context, def time.Duration) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain > def {
			return remain
		}
	}
	return def
}

// stripCodeFence 去掉模型爱包的 ```json ... ``` 围栏。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// Resolve 调用 AI 分析失败证据。
func (a *AIResolver) Resolve(ctx context.Context, ev Evidence, fp string) (Decision, bool) {
	model := a.currentModel()
	if model == "" {
		return Decision{}, false
	}
	if d, ok := a.cached(fp); ok {
		return d, true
	}

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	prompt := fmt.Sprintf(
		"分析这次模型 API 调用失败，返回 JSON（不要 markdown 代码块）：\n"+
			"{\"verdict\":\"disable_key|disable_model|cooldown|switch_next|ignore\","+
			"\"cooldown_seconds\":数字,\"recover\":\"never|daily|fixed\",\"reason\":\"一句话\"}\n"+
			"判定原则：额度用尽/余额不足→disable_key+daily；密钥无效→disable_key+never；"+
			"限速→cooldown 120；超时/网络→cooldown 30 或 ignore；"+
			"上下文超长→switch_next；参数错误(4xx)→ignore。\n"+
			"HTTP状态码: %d\n业务码: %s\n错误信息: %s",
		ev.StatusCode, ev.BodyCode, truncate(ev.Message, 500))

	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	})
	skKey := a.resolveKey()
	if skKey == "" {
		// 无可用 SK key：AI 兜底不可用（调用方走默认动作）。
		return Decision{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(a.baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Decision{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+skKey)
	req.Header.Set(headerRuleAI, "1")

	resp, err := (&http.Client{Timeout: a.timeout}).Do(req)
	if err != nil {
		return Decision{}, false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Decision{}, false
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return Decision{}, false
	}
	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var v aiVerdictSchema
	if err := json.Unmarshal([]byte(content), &v); err != nil || v.Verdict == "" {
		return Decision{}, false
	}
	if !validAIVerdict(v.Verdict) {
		return Decision{}, false
	}
	d := Decision{
		Verdict: v.Verdict,
		Reason:  v.Reason,
		AIModel: model,
		AIRaw:   truncate(content, 4000),
		// AI 附加参数直接进 Action（不再拼 Reason 字符串当数据总线）。
		Action: Action{
			Verdict:         v.Verdict,
			Recover:         v.Recover,
			CooldownSeconds: v.CooldownSeconds,
		},
	}
	ttl := aiCacheTTLCooldown
	if v.Verdict == VerdictDisableKey || v.Verdict == VerdictDisableModel || v.Verdict == VerdictDisableProvider {
		ttl = aiCacheTTLDisable
	}
	a.decisions.Store(fp, cachedDecision{d: d, at: time.Now(), ttl: ttl})
	return d, true
}

func validAIVerdict(v string) bool {
	switch v {
	case VerdictDisableKey, VerdictDisableModel, VerdictDisableProvider,
		VerdictCooldown, VerdictIgnore, VerdictRetrySame, VerdictSwitchNext:
		return true
	}
	return false
}

// truncate 按 **rune** 截断（不是字节）。
//
// 上游错误信息基本都是中文：按字节切会把一个汉字拦腰截断，产生 � 乱码，
// 这个乱码会顺着「样本指纹 → 样本 id → 草稿规则名」一路带到前端（实测踩过）。
// n 表示最多保留的字符数。
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

var _ = config.DataDir

func jsonUnmarshal(data string, v any) error { return json.Unmarshal([]byte(data), &v) }
