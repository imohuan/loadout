package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{{
	version: 1,
	name:    "routing_schema",
	sql: `
CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  api_key_cipher TEXT NOT NULL DEFAULT '',
  manual_enabled INTEGER NOT NULL DEFAULT 1,
  sync_billing INTEGER NOT NULL DEFAULT 0,
  models_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE channel_models (
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'probe',
  enabled INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  PRIMARY KEY (channel_id, model)
);
CREATE TABLE aggregates (
  id INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE aggregate_targets (
  aggregate_id INTEGER NOT NULL REFERENCES aggregates(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  model TEXT NOT NULL,
  channel_id TEXT NOT NULL REFERENCES channels(id),
  PRIMARY KEY (aggregate_id, position)
);
CREATE TABLE channel_states (
  channel_id TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'available',
  disabled_until TEXT,
  fail_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  last_failure_class TEXT NOT NULL DEFAULT '',
  last_success_at TEXT,
  last_checked_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE model_states (
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  manual_enabled INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'available',
  disabled_until TEXT,
  fail_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  last_failure_class TEXT NOT NULL DEFAULT '',
  last_success_at TEXT,
  last_checked_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (channel_id, model)
);
CREATE TABLE route_requests (
  request_id TEXT PRIMARY KEY,
  requested_model TEXT NOT NULL,
  virtual_model TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  result TEXT NOT NULL DEFAULT 'running',
  final_model TEXT,
  final_channel_id TEXT,
  http_status INTEGER,
  duration_ms INTEGER,
  error_message TEXT NOT NULL DEFAULT ''
);
CREATE TABLE route_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL REFERENCES route_requests(request_id) ON DELETE CASCADE,
  previous_attempt_id INTEGER REFERENCES route_attempts(id) ON DELETE SET NULL,
  step_no INTEGER NOT NULL,
  action TEXT NOT NULL,
  model TEXT NOT NULL,
  channel_id TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  result TEXT NOT NULL,
  failure_class TEXT NOT NULL DEFAULT '',
  status_code INTEGER,
  error_message TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  UNIQUE(request_id, step_no)
);
CREATE INDEX idx_route_requests_started_at ON route_requests(started_at DESC);
CREATE INDEX idx_route_requests_requested_model_started_at ON route_requests(requested_model, started_at DESC);
CREATE INDEX idx_route_attempts_request_step ON route_attempts(request_id, step_no);
CREATE INDEX idx_route_attempts_channel_started_at ON route_attempts(channel_id, started_at DESC);
CREATE INDEX idx_route_attempts_model_started_at ON route_attempts(model, started_at DESC);
CREATE INDEX idx_route_attempts_result_started_at ON route_attempts(result, started_at DESC);
CREATE TABLE data_imports (
  source_name TEXT PRIMARY KEY,
  source_checksum TEXT NOT NULL,
  imported_at TEXT NOT NULL,
  report_path TEXT NOT NULL
);`,
}, {
	version: 2,
	name:    "channel-priority",
	sql: `
ALTER TABLE channels ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
UPDATE channels SET position = rowid - 1 WHERE position = 0;
`,
}, {
	version: 3,
	name:    "route-attempts-usage",
	sql: `
ALTER TABLE route_attempts ADD COLUMN stream INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_attempts ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_attempts ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_attempts ADD COLUMN cached_tokens INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 4,
	name:    "route-requests-usage",
	sql: `
ALTER TABLE route_requests ADD COLUMN stream INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_requests ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_requests ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_requests ADD COLUMN cached_tokens INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 5,
	name:    "admin-config-tables",
	sql: `
CREATE TABLE capability_routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  position INTEGER NOT NULL DEFAULT 0,
  capability TEXT NOT NULL,
  route TEXT NOT NULL,
  models_json TEXT NOT NULL DEFAULT '[]',
  channel_ids_json TEXT NOT NULL DEFAULT '[]',
  via_options_json TEXT NOT NULL DEFAULT '[]',
  replacements_json TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE mcp_servers (
  id TEXT PRIMARY KEY,
  position INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT 'stdio',
  command TEXT NOT NULL DEFAULT '',
  args_json TEXT NOT NULL DEFAULT '[]',
  env_json TEXT NOT NULL DEFAULT '{}',
  url TEXT NOT NULL DEFAULT '',
  headers_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE mcp_groups (
  name TEXT PRIMARY KEY,
  position INTEGER NOT NULL DEFAULT 0,
  tools_json TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE tools_state (
  server_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  category TEXT NOT NULL DEFAULT '',
  tags_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (server_id, tool_name)
);
CREATE TABLE skills (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  installed_at TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE presets (
  name TEXT PRIMARY KEY,
  skills_json TEXT NOT NULL DEFAULT '[]',
  target TEXT NOT NULL DEFAULT '',
  targets_json TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  active_preset TEXT NOT NULL DEFAULT '',
  active_preset_target TEXT NOT NULL DEFAULT '',
  active_preset_targets_json TEXT NOT NULL DEFAULT '[]',
  default_model TEXT NOT NULL DEFAULT ''
);
CREATE TABLE gateway_keys (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL DEFAULT '',
  hash TEXT NOT NULL,
  models_json TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  header_name TEXT NOT NULL DEFAULT ''
);
CREATE TABLE users (
  username TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  password_changed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_capability_routes_position ON capability_routes(position);
CREATE INDEX idx_mcp_servers_position ON mcp_servers(position);
CREATE INDEX idx_mcp_groups_position ON mcp_groups(position);
CREATE INDEX idx_gateway_keys_kind ON gateway_keys(kind);
`,
}}

// Migrate applies all pending schema migrations and rejects an incompatible
// database instead of trying to infer a recovery path.
func Migrate(ctx context.Context, database *sql.DB) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("db: create migration table: %w", err)
	}

	rows, err := tx.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("db: read migrations: %w", err)
	}
	defer rows.Close()

	expected := 1
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("db: scan migration: %w", err)
		}
		if version != expected {
			return fmt.Errorf("db: migration versions are not contiguous: expected %d, got %d", expected, version)
		}
		if version > len(migrations) {
			return fmt.Errorf("db: database version %d is newer than this program", version)
		}
		current := migrations[version-1]
		if name != current.name || checksum != migrationChecksum(current.sql) {
			return fmt.Errorf("db: migration %d checksum or name does not match", version)
		}
		expected++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: iterate migrations: %w", err)
	}

	for _, migration := range migrations[expected-1:] {
		if migration.version != expected {
			return fmt.Errorf("db: program migrations are not contiguous at %d", migration.version)
		}
		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("db: apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			migration.version, migration.name, migrationChecksum(migration.sql), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("db: record migration %d: %w", migration.version, err)
		}
		expected++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit migrations: %w", err)
	}
	return nil
}

func migrationChecksum(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", sum[:])
}
