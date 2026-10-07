package modelhealth

import (
	"context"
	"testing"
)

// TestRecoverAllChannelsClearsBothLevels 「恢复全部平台」必须同时清 Key 级与模型级熔断，
// 但不动手动开关。只清 Key 级会让模型级继续挡路（与「恢复 Key」同一个坑）。
func TestRecoverAllChannelsClearsBothLevels(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, updated_at) VALUES ('c','disabled','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m-manual-off',0,'disabled','now'),
		('c','m-auto',1,'cooling','now')`); err != nil {
		t.Fatal(err)
	}

	if _, err := service.RecoverAllChannels(ctx); err != nil {
		t.Fatal(err)
	}

	var channelStatus string
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus != statusAvailable {
		t.Fatalf("Key 级应恢复，实际 %q", channelStatus)
	}
	var autoStatus string
	if err := database.QueryRow(`SELECT status FROM model_states WHERE channel_id='c' AND model='m-auto'`).Scan(&autoStatus); err != nil {
		t.Fatal(err)
	}
	if autoStatus != statusAvailable {
		t.Fatalf("模型级自动熔断应恢复，实际 %q", autoStatus)
	}
	var manual bool
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='c' AND model='m-manual-off'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		t.Fatal("恢复全部平台不应强制打开手动关闭的模型")
	}
}
