package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/store"
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
	if err := database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('schema_migrations', 'data_imports', 'channels', 'channel_models', 'aggregates', 'aggregate_targets', 'channel_states', 'model_states', 'route_requests', 'route_attempts', 'capability_routes', 'mcp_servers', 'mcp_groups', 'tools_state', 'skills', 'presets', 'settings', 'gateway_keys', 'users', 'volc_quota_config', 'volc_quota_models', 'volc_quota_usage')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 22 {
		t.Fatalf("schema tables = %d, want 22", tables)
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
	if count != 15 {
		t.Fatalf("schema_migrations count = %d, want 15", count)
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
			// 程序当前有 15 条迁移，插入 version 16 才能模拟"比程序更新"的库。
			if _, err := database.Exec("INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (16, 'future', 'future', 'now')"); err != nil {
				t.Fatal(err)
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
