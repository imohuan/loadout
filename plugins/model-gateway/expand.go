package modelgateway

import (
	"strings"

	"loadout/core/db"
)

// ResolvedKey 是聚合 target / 视觉候选展开后的单个候选 Key。
type ResolvedKey struct {
	ChannelID string
	BaseURL   string // 归一化（去尾斜杠）
	Name      string // Key 名（日志/展示用）
}

// NormalizeBaseURL 去掉尾部斜杠，用于渠道组（base_url）比较。
func NormalizeBaseURL(s string) string {
	return strings.TrimRight(s, "/")
}

// ExpandCandidateKeys 把聚合 target / 视觉候选展开为有序的候选 Key 列表。
//   - channelBaseURL 非空（渠道级）：返回该 base_url 组下所有启用的 Key（按渠道顺序）；
//   - 否则按 channelIDs（Key 多选）展开，channelIDs 为空时回退单 channelID（兼容老数据）；
//   - 输入 id 不存在于渠道表时静默跳过（不报错，容忍脏数据）。
func ExpandCandidateKeys(channelID string, channelIDs []string, channelBaseURL string, channels []db.Channel) []ResolvedKey {
	byID := make(map[string]db.Channel, len(channels))
	for _, ch := range channels {
		byID[ch.ID] = ch
	}

	// 渠道级：整组所有启用 Key。
	if channelBaseURL != "" {
		target := NormalizeBaseURL(channelBaseURL)
		var out []ResolvedKey
		for _, ch := range channels {
			if !ch.ManualEnabled {
				continue
			}
			if NormalizeBaseURL(ch.BaseURL) == target {
				out = append(out, ResolvedKey{ChannelID: ch.ID, BaseURL: ch.BaseURL, Name: ch.Name})
			}
		}
		return out
	}

	// Key 多选 / 单 Key：按声明顺序去重。
	var ids []string
	if len(channelIDs) > 0 {
		ids = channelIDs
	} else if channelID != "" {
		ids = []string{channelID}
	}
	var out []ResolvedKey
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		ch, ok := byID[id]
		if !ok {
			continue
		}
		seen[id] = true
		out = append(out, ResolvedKey{ChannelID: ch.ID, BaseURL: ch.BaseURL, Name: ch.Name})
	}
	return out
}
