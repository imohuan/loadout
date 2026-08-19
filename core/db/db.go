// Package db owns the SQLite connection and schema lifecycle.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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
func OpenMemory() (*sql.DB, error) {
	return Open("file:loadout-test?mode=memory&cache=shared")
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
