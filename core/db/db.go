// Package db owns the SQLite connection and schema lifecycle.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// DB is the process-local SQLite database used by routing plugins.
// Open creates the parent directory, opens SQLite and applies the required
// connection pragmas before running the versioned schema migrations.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("db: database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("db: create parent directory: %w", err)
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := configure(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := Migrate(context.Background(), sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

// OpenMemory opens an isolated database for tests.
//
// 用唯一名（进程内原子计数）而不是固定 "loadout-test"：named shared-memory 库
// 在最后一个连接关闭前一直存活，固定名会让不同测试复用残留的旧 schema（比如
// 加 v15 列后老测试库没跑迁移就报 "no such column"）。唯一名保证每次全新。
var memDBCounter int64

func OpenMemory() (*sql.DB, error) {
	n := atomic.AddInt64(&memDBCounter, 1)
	return Open(fmt.Sprintf("file:loadout-test-%d?mode=memory&cache=shared", n))
}

func configure(d *sql.DB) error {
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := d.Exec(statement); err != nil {
			return fmt.Errorf("db: %s: %w", statement, err)
		}
	}
	return nil
}

// OpenForStore opens the SQLite database next to the legacy JSON data
// directory, i.e. ~/.loadout/loadout.db.
func OpenForStore(st interface{ Dir() string }) (*sql.DB, error) {
	if st == nil || st.Dir() == "" {
		return nil, fmt.Errorf("db: store data directory is empty")
	}
	return Open(filepath.Join(filepath.Dir(st.Dir()), "loadout.db"))
}
