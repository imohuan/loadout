package workbuddypanel

import (
	"context"
	"fmt"
	"strings"
)

// wbModelRaw 上游 /console/enterprises/personal/models 的单条模型。
type wbModelRaw struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Credits           string `json:"credits"`
	DescriptionZh     string `json:"descriptionZh"`
	MaxOutputTokens   int    `json:"maxOutputTokens"`
	MaxInputTokens    int    `json:"maxInputTokens"`
	SupportsImages    bool   `json:"supportsImages"`
	SupportsToolCall  bool   `json:"supportsToolCall"`
	SupportsReasoning bool   `json:"supportsReasoning"`
	OnlyReasoning     bool   `json:"onlyReasoning"`
	IsDefault         bool   `json:"isDefault"`
	Reasoning         struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
	ContextWindow struct {
		DefaultLength int `json:"defaultLength"`
	} `json:"contextWindow"`
}

// fetchModelsHost 拉模型倍率与能力列表（URL 剥离统一在 call() 内处理）。
func (c *codebuddyClient) fetchModelsHost(ctx context.Context, baseURL, apiKey string) ([]WbModel, error) {
	var data struct {
		Models []wbModelRaw `json:"models"`
	}
	if err := c.call(ctx, baseURL, apiKey, "GET", "/console/enterprises/personal/models", nil, &data); err != nil {
		return nil, err
	}
	out := make([]WbModel, 0, len(data.Models))
	for _, m := range data.Models {
		model := WbModel{
			ID: m.ID, Name: m.Name, Credits: m.Credits,
			Description: m.DescriptionZh,
			Context:     m.ContextWindow.DefaultLength, MaxOutput: m.MaxOutputTokens,
			SupportsImages: m.SupportsImages, SupportsTool: m.SupportsToolCall,
			SupportsReasoning: m.SupportsReasoning, OnlyReasoning: m.OnlyReasoning,
			IsDefault: m.IsDefault, ReasoningEffort: m.Reasoning.Effort,
		}
		if m.Credits != "" {
			if f, err := strconvParseFloat(m.Credits); err == nil {
				model.Rate = f
			}
		}
		out = append(out, model)
	}
	sortWbModels(out)
	return out, nil
}

// urlParse 剥掉 base_url 的路径，只保留 scheme://host（端口保留）。
func urlParse(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", fmt.Errorf("invalid url: %s", raw)
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?"); j >= 0 {
		rest = rest[:j]
	}
	return raw[:i+3] + rest, nil
}

// sortWbModels 按倍率升序。
func sortWbModels(ms []WbModel) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Rate < ms[j-1].Rate; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}
