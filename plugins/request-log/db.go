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
	// 老库（表已存在）拿不到 CREATE TABLE 里的新列，这里补迁移。
	if err := migrateRequestLogSchema(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

// requestLogSchema 独立库 DDL：request_logs 单表（主键仅 id UUID）+ 单行配置表。
// result 对齐 route-log 语义：running / success / failed / stream_interrupted。
//
// bytes 列：这一行在库里大约占多少字节（见 rowBytes 的说明）。写入时就记好，
// 容量清理直接 SUM(bytes)，不必扫全表对正文 length() 求和。
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
  bytes INTEGER NOT NULL DEFAULT 0,
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

// rowFixedOverhead 每行的固定开销估算（rowid、页头、索引项摊销等）。
//
// 目的不是精确到字节（那要跟 SQLite 的内部布局较劲，得不偿失），而是让
// SUM(bytes) 与文件真实大小处在同一量级：内容体积总是略小于文件大小，
// 差得越多，容量清理就越依赖 VACUUM 复测兜底。给一个偏保守的常量既简单
// 又足够让估算贴合。
const rowFixedOverhead = 128

// rowBytes 计算一行的 bytes 值：请求体 + 响应体 + 固定开销。
// 一旦 request_json/response_json 被改写（收尾、自愈还原），必须重算。
func rowBytes(requestJSON, responseJSON string) int64 {
	return int64(len(requestJSON)+len(responseJSON)) + rowFixedOverhead
}

// migrateRequestLogSchema 为已存在的老库补列并回填。
//
// CREATE TABLE IF NOT EXISTS 对老库是空操作——表已存在，新加的列不会出现。
// 所以每次打开都要显式检查 bytes 列，缺了就 ALTER 补上，并把历史行回填一次
// （老行 bytes 默认 0，不回填的话容量清理会以为它们不占空间，永远不删）。
func migrateRequestLogSchema(database *sql.DB) error {
	columns, err := requestLogColumns(database)
	if err != nil {
		return err
	}
	if columns["bytes"] {
		return nil
	}
	if _, err := database.Exec(`ALTER TABLE request_logs ADD COLUMN bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("request-log: add bytes column: %w", err)
	}
	// 回填历史行：只对还没算过的（bytes = 0）算一次。
	if _, err := database.Exec(`UPDATE request_logs SET bytes = length(request_json) + length(COALESCE(response_json, '')) + ? WHERE bytes = 0`, rowFixedOverhead); err != nil {
		return fmt.Errorf("request-log: backfill bytes: %w", err)
	}
	return nil
}

// requestLogColumns 返回 request_logs 现有的列名集合，用于判断要不要迁移。
func requestLogColumns(database *sql.DB) (map[string]bool, error) {
	rows, err := database.Query(`SELECT name FROM pragma_table_info('request_logs')`)
	if err != nil {
		return nil, fmt.Errorf("request-log: read columns: %w", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
