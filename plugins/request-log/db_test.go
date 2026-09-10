package requestlog

import (
	"database/sql"
	"testing"
)

func TestOpenRequestLogDB(t *testing.T) {
	database, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var tables int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('request_logs','request_log_config')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 2 {
		t.Fatalf("tables = %d, want 2", tables)
	}

	// 脱敏开关默认开（redact=1），且配置行已初始化
	var redact int
	if err := database.QueryRow("SELECT redact FROM request_log_config WHERE id = 1").Scan(&redact); err != nil {
		t.Fatal(err)
	}
	if redact != 1 {
		t.Fatalf("redact = %d, want 1", redact)
	}

	// bytes 列必须存在于新库（容量清理靠它求和）。
	columns, err := requestLogColumns(database)
	if err != nil {
		t.Fatal(err)
	}
	if !columns["bytes"] {
		t.Fatalf("bytes column missing on a fresh DB: %v", columns)
	}
}

func TestOpenRequestLogDBEmptyPath(t *testing.T) {
	if _, err := openRequestLogDB(""); err == nil {
		t.Fatal("empty path should error")
	}
}

// TestMigrateAddsBytesColumn 老库（无 bytes 列）打开时自动补列并回填历史行。
//
// 关键：CREATE TABLE IF NOT EXISTS 对已存在的表是空操作，不加迁移的话老库永远
// 拿不到 bytes 列；更要紧的是历史行 bytes 会停在 0，容量清理会以为它们不占空间，
// 于是怎么都删不掉——正是「15GB 改小上限却没反应」这类问题的翻版。
func TestMigrateAddsBytesColumn(t *testing.T) {
	path := t.TempDir() + "/request-log.db"

	// 用手工建的老 schema（没有 bytes 列）造一个旧库，并塞两条历史行。
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE request_logs (
		  id TEXT PRIMARY KEY,
		  request_id TEXT NOT NULL,
		  model TEXT NOT NULL DEFAULT '',
		  channel TEXT NOT NULL DEFAULT '',
		  http_status INTEGER,
		  stream INTEGER NOT NULL DEFAULT 0,
		  started_at TEXT NOT NULL,
		  finished_at TEXT,
		  duration_ms INTEGER,
		  result TEXT NOT NULL DEFAULT 'running',
		  request_json TEXT NOT NULL,
		  response_json TEXT,
		  created_at TEXT NOT NULL
		);`); err != nil {
		t.Fatal(err)
	}
	req := "0123456789"
	resp := "abcdefghij"
	if _, err := old.Exec(`INSERT INTO request_logs(id, request_id, started_at, result, request_json, response_json, created_at) VALUES ('old1', 'old1', '2026-01-01T00:00:00Z', 'success', ?, ?, '2026-01-01T00:00:00Z')`, req, resp); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// 用正常路径打开：应自动补列 + 回填。
	database, err := openRequestLogDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	columns, err := requestLogColumns(database)
	if err != nil {
		t.Fatal(err)
	}
	if !columns["bytes"] {
		t.Fatalf("bytes column not added by migration: %v", columns)
	}

	var got int64
	if err := database.QueryRow(`SELECT bytes FROM request_logs WHERE id = 'old1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := rowBytes(req, resp)
	if got != want {
		t.Fatalf("backfilled bytes = %d, want %d", got, want)
	}
}

// TestRowBytesCountsBothBodies bytes 必须把请求体和响应体都算进去。
func TestRowBytesCountsBothBodies(t *testing.T) {
	if got, want := rowBytes("12345", "123"), int64(8+rowFixedOverhead); got != want {
		t.Fatalf("rowBytes = %d, want %d", got, want)
	}
	// 只有请求体（半条日志，还没收尾）。
	if got, want := rowBytes("12345", ""), int64(5+rowFixedOverhead); got != want {
		t.Fatalf("rowBytes(request only) = %d, want %d", got, want)
	}
}
