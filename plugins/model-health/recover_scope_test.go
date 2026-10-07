package modelhealth

import (
	"context"
	"database/sql"
	"testing"
)

// TestRecoverChannelClearsModelLevelBreakers 回归：
// 「恢复 Key」必须同时清掉 Key 级熔断与该 Key 下所有模型级熔断。
// 历史实现只清 channel_states，模型级还留着 cooling/disabled，路由照旧跳过，
// 用户点了恢复仍然走不到这个 Key（就是「恢复了还走第三个模型」的根因）。
func TestRecoverChannelClearsModelLevelBreakers(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, updated_at) VALUES ('c','disabled','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count, last_error, updated_at) VALUES
		('c','m1',1,'cooling','2099-01-01T00:00:00Z',3,'上游 429','now'),
		('c','m2',1,'disabled',NULL,1,'auth','now')`); err != nil {
		t.Fatal(err)
	}

	if err := service.RecoverChannel(ctx, "c"); err != nil {
		t.Fatalf("RecoverChannel: %v", err)
	}

	var channelStatus string
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus != statusAvailable {
		t.Fatalf("Key 级应恢复 available，实际 %q", channelStatus)
	}

	for _, m := range []string{"m1", "m2"} {
		var status string
		var failCount int
		var until sql.NullString
		if err := database.QueryRow(`SELECT status, fail_count, disabled_until FROM model_states WHERE channel_id='c' AND model=?`, m).Scan(&status, &failCount, &until); err != nil {
			t.Fatal(err)
		}
		if status != statusAvailable || failCount != 0 || until.Valid {
			t.Fatalf("模型 %s 应清空熔断，实际 status=%q fail=%d until=%v", m, status, failCount, until.String)
		}
	}
}

// TestRecoverChannelKeepsManualOff 恢复只清自动熔断，不强制打开手动关闭的模型。
func TestRecoverChannelKeepsManualOff(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES ('c','m-off',0,'disabled','now')`); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverChannel(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	var manual bool
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='c' AND model='m-off'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		t.Fatal("恢复 Key 不应强制打开手动关闭的模型")
	}
}
