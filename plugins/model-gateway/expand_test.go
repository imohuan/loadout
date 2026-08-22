package modelgateway

import (
	"slices"
	"testing"

	"loadout/core/db"
)

func TestExpandCandidateKeys(t *testing.T) {
	channels := []db.Channel{
		{ID: "k1", Name: "Key1", BaseURL: "https://a.example/v1", ManualEnabled: true},
		{ID: "k2", Name: "Key2", BaseURL: "https://a.example/v1/", ManualEnabled: true}, // 尾斜杠差异
		{ID: "k3", Name: "Key3", BaseURL: "https://a.example/v1", ManualEnabled: false},
		{ID: "k4", Name: "Key4", BaseURL: "https://b.example/v1", ManualEnabled: true},
	}

	t.Run("channel level", func(t *testing.T) {
		got := ExpandCandidateKeys("", nil, "https://a.example/v1/", channels)
		var ids []string
		for _, k := range got {
			ids = append(ids, k.ChannelID)
		}
		// k3 手动禁用应被过滤。
		if !slices.Equal(ids, []string{"k1", "k2"}) {
			t.Fatalf("channel level ids = %v, want [k1 k2]", ids)
		}
	})

	t.Run("multi key keeps order", func(t *testing.T) {
		got := ExpandCandidateKeys("", []string{"k4", "k1"}, "", channels)
		var ids []string
		for _, k := range got {
			ids = append(ids, k.ChannelID)
		}
		if !slices.Equal(ids, []string{"k4", "k1"}) {
			t.Fatalf("multi key ids = %v, want [k4 k1]", ids)
		}
	})

	t.Run("multi key dedups", func(t *testing.T) {
		got := ExpandCandidateKeys("", []string{"k1", "k1", "k2"}, "", channels)
		if len(got) != 2 {
			t.Fatalf("dedup len = %d, want 2", len(got))
		}
	})

	t.Run("single key fallback", func(t *testing.T) {
		got := ExpandCandidateKeys("k4", nil, "", channels)
		if len(got) != 1 || got[0].ChannelID != "k4" {
			t.Fatalf("single key = %+v", got)
		}
	})

	t.Run("single key overridden by ids", func(t *testing.T) {
		got := ExpandCandidateKeys("k1", []string{"k4"}, "", channels)
		if len(got) != 1 || got[0].ChannelID != "k4" {
			t.Fatalf("ids should win over single key, got %+v", got)
		}
	})

	t.Run("unknown id skipped", func(t *testing.T) {
		got := ExpandCandidateKeys("", []string{"missing", "k1"}, "", channels)
		if len(got) != 1 || got[0].ChannelID != "k1" {
			t.Fatalf("unknown id should be skipped, got %+v", got)
		}
	})

	t.Run("empty returns none", func(t *testing.T) {
		if got := ExpandCandidateKeys("", nil, "", channels); len(got) != 0 {
			t.Fatalf("empty = %+v", got)
		}
	})
}
