package requestlog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openRequestLogDB 打开 request-log 独立库（默认与 loadout.db 同级）。
//
// 不能复用 core/db.Open：它固定跑 loadout 的全局迁移（migrate.go migrations），
// 会在本库上执行与 request_logs 无关的 DDL。这里自行 sql.Open + pragmas +
// CREATE TABLE IF NOT EXISTS。单表无演进历史，无需版本机制；将来加列再自建
// 轻量版本表。
func openRequestLogDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("request-log: database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("request-log: create parent directory: %w", err)
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("request-log: open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := sqlDB.Exec(statement); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("request-log: %s: %w", statement, err)
		}
	}
	if _, err := sqlDB.Exec(requestLogSchema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("request-log: create schema: %w", err)
	}
	return sqlDB, nil
}

// requestLogSchema 独立库 DDL：request_logs 单表（主键仅 id UUID）+ 单行配置表。
// result 对齐 route-log 语义：running / success / failed / stream_interrupted。
const requestLogSchema = `
CREATE TABLE IF NOT EXISTS request_logs (
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
);
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id ON request_logs(request_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_started_at ON request_logs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_channel ON request_logs(channel, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_result ON request_logs(result, started_at DESC);
CREATE TABLE IF NOT EXISTS request_log_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  redact INTEGER NOT NULL DEFAULT 1
);
INSERT OR IGNORE INTO request_log_config(id, redact) VALUES (1, 1);
`
