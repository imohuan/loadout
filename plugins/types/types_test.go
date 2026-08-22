package types

import (
	"encoding/json"
	"testing"
)

func TestMatchChannelScope(t *testing.T) {
	cases := []struct {
		name      string
		ids       []string
		baseURLs  []string
		channelID string
		reqBase   string
		want      bool
	}{
		// 全渠道 / 通配
		{"both empty = all", nil, nil, "", "", true},
		{"both empty = all any channel", nil, nil, "ch-a", "https://a/v1", true},
		{"star = all", []string{"*"}, nil, "", "", true},
		{"star = all any channel", []string{"*"}, nil, "ch-z", "", true},

		// Key 级
		{"key exact hit", []string{"ch-b"}, nil, "ch-b", "https://b/v1", true},
		{"key miss", []string{"ch-b"}, nil, "ch-a", "https://a/v1", false},
		{"key unknown channel", []string{"ch-b"}, nil, "", "", false},

		// 渠道级
		{"base url hit", nil, []string{"https://b/v1"}, "ch-b", "https://b/v1", true},
		{"base url hit trailing slash", nil, []string{"https://b/v1/"}, "ch-b", "https://b/v1", true},
		{"base url miss", nil, []string{"https://b/v1"}, "ch-a", "https://a/v1", false},
		// 关键：纯渠道级（channel_ids 空）不能被 MatchChannel 的「空=全渠道」误判为全渠道。
		{"pure channel-level not all", nil, []string{"https://b/v1"}, "ch-a", "https://a/v1", false},
		{"pure channel-level unknown", nil, []string{"https://b/v1"}, "", "", false},

		// 并存（渠道级 + Key 级互斥，不同渠道）
		{"mixed key hit", []string{"ch-k"}, []string{"https://b/v1"}, "ch-k", "", true},
		{"mixed base hit", []string{"ch-k"}, []string{"https://b/v1"}, "ch-b", "https://b/v1", true},
		{"mixed miss", []string{"ch-k"}, []string{"https://b/v1"}, "ch-x", "https://x/v1", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MatchChannelScope(c.ids, c.baseURLs, c.channelID, c.reqBase)
			if got != c.want {
				t.Fatalf("MatchChannelScope(%v, %v, %q, %q) = %v, want %v",
					c.ids, c.baseURLs, c.channelID, c.reqBase, got, c.want)
			}
		})
	}
}

func TestChannelBaseURLMatches(t *testing.T) {
	if ChannelBaseURLMatches([]string{"https://a/v1"}, "https://a/v1/") != true {
		t.Fatal("尾斜杠应归一化后命中")
	}
	if ChannelBaseURLMatches(nil, "https://a/v1") != false {
		t.Fatal("空渠道级列表不应命中")
	}
	if ChannelBaseURLMatches([]string{"https://a/v1"}, "") != false {
		t.Fatal("请求 base_url 为空不应命中")
	}
}

func TestCapabilityRouteFieldRulesJSON(t *testing.T) {
	in := CapabilityRoute{
		Models:     []string{"*"},
		Capability: "field_filter",
		Route:      "proxy",
		FieldRules: &FieldRules{
			RequestStrip:  []string{"client_metadata", "a.b.c"},
			RequestKeep:   []string{"model", "messages"},
			ResponseStrip: []string{"choices.0.usage"},
			ResponseHeaderStrip:   []string{"X-Internal"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out CapabilityRoute
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.FieldRules == nil || len(out.FieldRules.RequestStrip) != 2 || out.FieldRules.RequestStrip[0] != "client_metadata" {
		t.Fatalf("FieldRules roundtrip 失败: %+v", out.FieldRules)
	}
	// 未配置时 FieldRules 应为 nil（omitempty 不输出，反序列化为 nil）
	var out2 CapabilityRoute
	if err := json.Unmarshal([]byte(`{"capability":"vision"}`), &out2); err != nil {
		t.Fatal(err)
	}
	if out2.FieldRules != nil {
		t.Fatalf("未配置 FieldRules 应为 nil，实际 %+v", out2.FieldRules)
	}
}
