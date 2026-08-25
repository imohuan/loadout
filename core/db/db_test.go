package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

func TestOpenAppliesSchemaAndSettings(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()

	var foreignKeys, busyTimeout int
	var journalMode string
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("PRAGMAs = journal_mode:%s foreign_keys:%d busy_timeout:%d", journalMode, foreignKeys, busyTimeout)
	}
	var tables int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('schema_migrations', 'data_imports', 'channels', 'channel_models', 'aggregates', 'aggregate_targets', 'channel_states', 'model_states', 'route_requests', 'route_attempts', 'capability_routes', 'mcp_servers', 'mcp_groups', 'tools_state', 'skills', 'presets', 'settings', 'gateway_keys', 'users', 'volc_quota_config', 'volc_quota_usage')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 21 {
		t.Fatalf("schema tables = %d, want 21", tables)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadout.db")
	database := mustOpen(t, path)
	database.Close()
	database = mustOpen(t, path)
	defer database.Close()

	var count int
	if err := database.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 26 {
		t.Fatalf("schema_migrations count = %d, want 26", count)
	}
}

func TestMigrateRejectsIncompatibleHistory(t *testing.T) {
	for name, corrupt := range map[string]func(*sql.DB){
		"checksum mismatch": func(database *sql.DB) {
			if _, err := database.Exec("UPDATE schema_migrations SET checksum = 'wrong' WHERE version = 1"); err != nil {
				t.Fatal(err)
			}
		},
		"newer database": func(database *sql.DB) {
			// 程序当前有 25 条迁移，插入 version 26 才能模拟"比程序更新"的库。
			if _, err := database.Exec("INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (27, 'future', 'future', 'now')"); err != nil {				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "loadout.db")
			database := mustOpen(t, path)
			corrupt(database)
			// 先关库再重开，避免 Windows 上文件句柄未释放导致清理失败。
			database.Close()
			if _, err := Open(path); err == nil {
				t.Fatal("Open succeeded with incompatible migration history")
			}
		})
	}
}

func TestImportJSONIsIdempotent(t *testing.T) {
	root := t.TempDir()
	st, err := store.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("channels.json", []map[string]any{{
		"id": "alpha", "name": "Alpha", "base_url": "https://alpha.example/v1", "api_key_cipher": "ciphertext", "enabled": true, "models": []string{"gpt-test"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("aggregates.json", []map[string]any{{
		"name": "auto", "targets": []map[string]string{{"model": "gpt-test", "channel_id": "alpha"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("model_health.json", []map[string]any{{
		"model": "gpt-test@alpha", "status": "cooling", "fail_count": 2, "last_error": "busy",
	}}); err != nil {
		t.Fatal(err)
	}

	database, err := OpenForStore(st)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := ImportJSON(context.Background(), database, st); err != nil {
		t.Fatal(err)
	}
	if err := ImportJSON(context.Background(), database, st); err != nil {
		t.Fatal(err)
	}

	var cipher string
	if err := database.QueryRow("SELECT api_key_cipher FROM channels WHERE id = 'alpha'").Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if cipher != "ciphertext" {
		t.Fatalf("api_key_cipher = %q", cipher)
	}
	var imports, targets, states int
	if err := database.QueryRow("SELECT count(*) FROM data_imports").Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT count(*) FROM aggregate_targets").Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("SELECT count(*) FROM model_states").Scan(&states); err != nil {
		t.Fatal(err)
	}
	if imports != 3 || targets != 1 || states != 1 {
		t.Fatalf("imports:%d targets:%d states:%d", imports, targets, states)
	}
	var reportPath string
	if err := database.QueryRow("SELECT report_path FROM data_imports WHERE source_name = 'channels.json'").Scan(&reportPath); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), `"mapped"`) || !strings.Contains(string(report), `"skipped"`) || !strings.Contains(string(report), `"failed"`) {
		t.Fatalf("report lacks migration fields: %s", report)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(reportPath), "channels.json")); err != nil {
		t.Fatalf("channels backup missing: %v", err)
	}
}

func TestImportJSONRollsBackAllFiles(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("channels.json", []map[string]any{{"id": "alpha", "name": "Alpha", "base_url": "https://alpha.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write("aggregates.json", []map[string]any{{
		"name": "auto", "targets": []map[string]string{{"model": "gpt-test", "channel_id": "missing"}},
	}}); err != nil {
		t.Fatal(err)
	}
	database, err := OpenForStore(st)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := ImportJSON(context.Background(), database, st); err == nil {
		t.Fatal("ImportJSON succeeded with an unknown aggregate target channel")
	}
	for _, table := range []string{"channels", "aggregates", "data_imports"} {
		var count int
		if err := database.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after failed import", table, count)
		}
	}
}

func TestImportLegacyJSONUsesProvidedBackupRootAndWritesMarkdown(t *testing.T) {
	root := t.TempDir()
	st, err := store.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write("channels.json", []map[string]any{{"id": "alpha", "name": "Alpha", "base_url": "https://alpha.example", "unknown": true}}); err != nil {
		t.Fatal(err)
	}
	database, err := OpenForStore(st)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	backupRoot := filepath.Join(root, "legacy-backups")
	report, err := ImportLegacyJSON(context.Background(), database, st, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(report.JSONPath, backupRoot) || !strings.HasPrefix(report.MarkdownPath, backupRoot) {
		t.Fatalf("report paths do not use backup root: %+v", report)
	}
	markdown, err := os.ReadFile(report.MarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "### Mapped") || !strings.Contains(string(markdown), "### Skipped") || !strings.Contains(string(markdown), "### Failed") {
		t.Fatalf("markdown report lacks required sections: %s", markdown)
	}
	if len(report.Files) != 1 || len(report.Files[0].Skipped) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestAggregateTargetPreventsChannelDelete(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, created_at, updated_at) VALUES ('alpha', 'Alpha', 'https://alpha.example', 'now', 'now');
INSERT INTO aggregates(name, created_at, updated_at) VALUES ('auto', 'now', 'now');
INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id) VALUES (1, 0, 'gpt-test', 'alpha')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("DELETE FROM channels WHERE id = 'alpha'"); err == nil {
		t.Fatal("channel deletion succeeded despite aggregate target reference")
	}
}

func TestRepositoryReplacesRoutingConfigurationAtomically(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	repo, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	channels := []Channel{{
		ID: "alpha", Name: "Alpha", BaseURL: "https://alpha.example", APIKeyCipher: "ciphertext", ManualEnabled: true,
		Models: []ChannelModel{{Model: "gpt-test", Source: "probe", Enabled: true}},
	}}
	if err := repo.ReplaceChannels(ctx, channels); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceAggregates(ctx, []Aggregate{{Name: "auto", Enabled: true, Targets: []AggregateTarget{{Model: "gpt-test", ChannelID: "alpha"}}}}); err != nil {
		t.Fatal(err)
	}

	gotChannels, err := repo.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotAggregates, err := repo.ListAggregates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotChannels) != 1 || len(gotChannels[0].Models) != 1 || gotChannels[0].APIKeyCipher != "ciphertext" {
		t.Fatalf("channels = %+v", gotChannels)
	}
	if len(gotAggregates) != 1 || len(gotAggregates[0].Targets) != 1 {
		t.Fatalf("aggregates = %+v", gotAggregates)
	}

	if err := repo.ReplaceChannels(ctx, nil); err == nil {
		t.Fatal("removing a channel referenced by an aggregate should fail")
	}
	if got, err := repo.ListChannels(ctx); err != nil || len(got) != 1 {
		t.Fatalf("failed replacement changed rows: channels=%+v err=%v", got, err)
	}
	if err := repo.ReplaceChannelModels(ctx, "missing", nil); err == nil {
		t.Fatal("replacing models for a missing channel should fail")
	}
}

func mustOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestMigrateStepNoText(t *testing.T) {
	ctx := context.Background()
	// 手动构造 v22 库：绕过 Open 的自动 Migrate，按 migrations[0:22] 建 schema 并记录
	// schema_migrations，插入 INTEGER step_no 的历史数据后，调 Migrate 只应用 v23，
	// 验证重建（类型转换、数据保留、外键/唯一约束、索引）。
	d, err := sql.Open("sqlite", fmt.Sprintf("file:loadout-stepno-%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	if err := configure(d); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:len(migrations)-1] {
		if _, err := d.Exec(m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
		if _, err := d.Exec("INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			m.version, m.name, migrationChecksum(m.sql), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("record migration %d: %v", m.version, err)
		}
	}
	insertAttempt := func(requestID string, step int, prev any, action string, tokens int) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO route_attempts(request_id, previous_attempt_id, step_no, action, model, channel_id, channel_ids_json, channel_base_url, channel_name, started_at, result, failure_class, status_code, error_message, error_body, duration_ms, stream, prompt_tokens, completion_tokens, cached_tokens, metadata_json)
VALUES (?, ?, ?, ?, 'gpt-test', 'chan1', '["chan1"]', 'https://chan1.example', 'Chan1', 'now', 'success', '', 200, '', '', 100, 1, ?, ?, ?, '{}')`,
			requestID, prev, step, action, tokens, tokens*2, tokens*3); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec("INSERT INTO route_requests(request_id, requested_model, started_at, result) VALUES ('req1', 'gpt-test', 'now', 'success')"); err != nil {
		t.Fatal(err)
	}
	insertAttempt("req1", 1, nil, "首次尝试", 10)
	insertAttempt("req1", 2, 1, "视觉识别", 20)

	// 库只有 1..22，Migrate 只应用 v23（含 foreign_keys 关/开处理）
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}

	// 1) step_no 列类型改为 TEXT
	var typeName string
	found := false
	rows, err := d.Query("PRAGMA table_info(route_attempts)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "step_no" {
			found = true
			typeName = typ
		}
	}
	rows.Close()
	if !found || typeName != "TEXT" {
		t.Fatalf("step_no type = %q (found=%v), want TEXT", typeName, found)
	}

	// 2) 历史数据保留：step_no 转文本、previous_attempt_id 未被 SET NULL、token 列未丢
	var step string
	var prev sql.NullInt64
	var stream, prompt, completion, cached int
	if err := d.QueryRow("SELECT step_no, previous_attempt_id, stream, prompt_tokens, completion_tokens, cached_tokens FROM route_attempts WHERE id = 2").Scan(&step, &prev, &stream, &prompt, &completion, &cached); err != nil {
		t.Fatal(err)
	}
	if step != "2" || !prev.Valid || prev.Int64 != 1 || stream != 1 || prompt != 20 || completion != 40 || cached != 60 {
		t.Fatalf("migrated row = step:%q prev:%+v stream:%d tokens:%d/%d/%d", step, prev, stream, prompt, completion, cached)
	}

	// 3) 点分层级可插入，且唯一约束生效："1"、"1.1"、"1.2" 互不冲突
	for _, s := range []string{"1.1", "1.2"} {
		if _, err := d.Exec("INSERT INTO route_attempts(request_id, step_no, action, model, started_at, result) VALUES ('req1', ?, '视觉识别', 'gpt-test', 'now', 'success')", s); err != nil {
			t.Fatalf("insert %q: %v", s, err)
		}
	}
	if _, err := d.Exec("INSERT INTO route_attempts(request_id, step_no, action, model, started_at, result) VALUES ('req1', '1', 'dup', 'gpt-test', 'now', 'running')"); err == nil {
		t.Fatal("duplicate (request_id='req1', step_no='1') should violate UNIQUE")
	}

	// 4) ON CONFLICT(request_id, step_no) UPSERT 语义不变
	if _, err := d.Exec("INSERT INTO route_attempts(request_id, step_no, action, model, started_at, result) VALUES ('req1', '1.1', '视觉识别', 'gpt-test', 'now', 'success') ON CONFLICT(request_id, step_no) DO UPDATE SET result='failed'"); err != nil {
		t.Fatalf("upsert '1.1': %v", err)
	}
	var upserted string
	if err := d.QueryRow("SELECT result FROM route_attempts WHERE request_id='req1' AND step_no='1.1'").Scan(&upserted); err != nil {
		t.Fatal(err)
	}
	if upserted != "failed" {
		t.Fatalf("upsert result = %q, want failed", upserted)
	}

	// 5) v1 的 4 个索引重建齐全
	for _, idx := range []string{"idx_route_attempts_request_step", "idx_route_attempts_channel_started_at", "idx_route_attempts_model_started_at", "idx_route_attempts_result_started_at"} {
		var n int
		if err := d.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s missing after rebuild", idx)
		}
	}

	// 6) 迁移后外键仍生效：删除父请求级联删除 attempts
	if _, err := d.Exec("DELETE FROM route_requests WHERE request_id = 'req1'"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := d.QueryRow("SELECT count(*) FROM route_attempts WHERE request_id = 'req1'").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("cascade delete left %d rows", remaining)
	}
}

func TestCapabilityRouteFieldRulesPersist(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	repo, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	routes := []types.CapabilityRoute{{
		Models:     []string{"gpt-4o"},
		Capability: "field_filter",
		Route:      "proxy",
		FieldRules: &types.FieldRules{RequestStrip: []string{"client_metadata"}, ResponseHeaderStrip: []string{"X-Internal"}},
	}}
	if err := repo.ReplaceCapabilityRoutes(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListCapabilityRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FieldRules == nil ||
		len(got[0].FieldRules.RequestStrip) != 1 || got[0].FieldRules.RequestStrip[0] != "client_metadata" ||
		len(got[0].FieldRules.ResponseHeaderStrip) != 1 || got[0].FieldRules.ResponseHeaderStrip[0] != "X-Internal" {
		t.Fatalf("FieldRules 持久化失败: %+v", got)
	}
}
