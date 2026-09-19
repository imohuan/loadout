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

// headerRuleAI AI 兜底请求的标记 header（网关侧识别用，防递归由 engine 的
// 求值入口不暴露给带该 header 的请求保证——AI 请求走内部 loopback，不经过
// RecordFailure 求值入口）。
const headerRuleAI = "X-Loadout-Rule-AI"

// AIResolver AI 兜底判定器：调用网关自身 /v1/chat/completions（指定小模型）
// 分析错误证据，返回裁决。失败/超时/未配置时 Resolve 返回 ok=false。
type AIResolver struct {
	mu        sync.Mutex
	model     string // settings.rule_ai_model；空 = 关闭
	skKey     string // 网关 SK key
	baseURL   string // 网关自身地址
	timeout   time.Duration
	decisions sync.Map // fingerprint -> decision（短 TTL 语义由进程生命周期兜底）
}

// NewAIResolver 创建 AI 解析器。model 为空时 Resolve 永远返回 false。
func NewAIResolver(model, skKey, baseURL string) *AIResolver {
	return &AIResolver{model: model, skKey: skKey, baseURL: baseURL, timeout: 8 * time.Second}
}

// Enabled 返回 AI 兜底是否启用。
func (a *AIResolver) Enabled() bool { return a != nil && a.model != "" }

// SetModel 热更新 AI 模型（设置页保存后调用；空 = 关闭）。
func (a *AIResolver) SetModel(model string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = strings.TrimSpace(model)
}

// cached 取缓存判定。
func (a *AIResolver) cached(fp string) (Decision, bool) {
	if v, ok := a.decisions.Load(fp); ok {
		return v.(Decision), true
	}
	return Decision{}, false
}

// aiVerdictSchema AI 返回的结构。
type aiVerdictSchema struct {
	Verdict         string `json:"verdict"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
	Recover         string `json:"recover,omitempty"`
	Reason          string `json:"reason"`
}

// Resolve 调用 AI 分析失败证据。
func (a *AIResolver) Resolve(ctx context.Context, ev Evidence, fp string) (Decision, bool) {
	if !a.Enabled() {
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
		"model": a.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(a.baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Decision{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.skKey)
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
		AIModel: a.model,
		AIRaw:   truncate(content, 4000),
	}
	// recover/cooldown 从 AI 结果透传到动作执行层（extra 字段）。
	if v.Recover != "" {
		d.Reason = d.Reason + "|recover=" + v.Recover
		if v.CooldownSeconds > 0 {
			d.Reason = d.Reason + "|cooldown_seconds=" + fmt.Sprint(v.CooldownSeconds)
		}
	}
	a.decisions.Store(fp, d)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// ensure config import used (baseURL defaults documented); config 包引用保留给
// 后续读取默认端口等能力。
var _ = config.DataDir

func jsonUnmarshal(data string, v any) error { return json.Unmarshal([]byte(data), &v) }
