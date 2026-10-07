package modelhealth

import (
	"context"
	"database/sql"
	"testing"
)

// TestRecoverPlatformByBaseURL 平台级恢复：按 base_url 找到该组所有 Key，
// 逐个清掉 Key 级 + 模型级熔断；不碰其它平台，也不强制打开手动关闭的模型。
func TestRecoverPlatformByBaseURL(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES
		('b1','B1','https://plat.example/v1',1,0,'now','now'),
		('b2','B2','https://plat.example/v1',1,0,'now','now'),
		('other','OTHER','https://other.example/v1',1,0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	// 两个平台都写入 Key 级 + 模型级熔断。
	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, updated_at) VALUES ('b1','disabled','now'),('b2','cooling','now'),('other','disabled','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('b1','m',0,'disabled','now'),
		('b2','m',1,'disabled','now'),
		('other','m',1,'disabled','now')`); err != nil {
		t.Fatal(err)
	}

	affected, err := service.RecoverPlatformByBaseURL(ctx, "https://plat.example/v1")
	if err != nil {
		t.Fatalf("RecoverPlatformByBaseURL: %v", err)
	}
	if affected == 0 {
		t.Fatal("应报告有受影响的行")
	}

	// 本平台的 Key 级与模型级都应恢复。
	for _, id := range []string{"b1", "b2"} {
		var status string
		if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id=?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != statusAvailable {
			t.Fatalf("%s Key 级应恢复，实际 %q", id, status)
		}
		var mStatus string
		if err := database.QueryRow(`SELECT status FROM model_states WHERE channel_id=? AND model='m'`, id).Scan(&mStatus); err != nil {
			t.Fatal(err)
		}
		if mStatus != statusAvailable {
			t.Fatalf("%s 模型级应恢复，实际 %q", id, mStatus)
		}
	}

	// 另一个平台不受影响。
	var otherStatus string
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='other'`).Scan(&otherStatus); err != nil {
		t.Fatal(err)
	}
	if otherStatus != statusDisabled {
		t.Fatalf("其它平台不应被改动，实际 %q", otherStatus)
	}

	// 手动关闭的模型不被强制打开。
	var manual bool
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='b1' AND model='m'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		t.Fatal("平台恢复不应强制打开手动关闭的模型")
	}
}

// TestRecoverPlatformByBaseURLTrailingSlash 归一化比较：尾斜杠差异属同一平台。
func TestRecoverPlatformByBaseURLTrailingSlash(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES ('c2','C2','http://c/',1,0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, updated_at) VALUES ('c2','disabled','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverPlatformByBaseURL(ctx, "http://c"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c2'`).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("c2 的渠道状态行不应被删")
		}
		t.Fatal(err)
	}
	if status != statusAvailable {
		t.Fatalf("尾斜杠差异应视为同一平台，实际 %q", status)
	}
}
