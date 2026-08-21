package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

type ImportFileReport struct {
	Source  string   `json:"source"`
	Mapped  []string `json:"mapped"`
	Skipped []string `json:"skipped"`
	Failed  []string `json:"failed"`
}

type ImportReport struct {
	Files        []ImportFileReport `json:"files"`
	JSONPath     string             `json:"json_path"`
	MarkdownPath string             `json:"markdown_path"`
}

// ImportJSON uses the standard backups directory for the first routing-data
// import. It is deliberately a one-shot operation tracked by data_imports.
func ImportJSON(ctx context.Context, database *sql.DB, st *store.Store) error {
	_, err := ImportLegacyJSON(ctx, database, st, filepath.Join(filepath.Dir(st.Dir()), "backups"))
	return err
}

// ImportLegacyJSON imports routing JSON as one transaction, writes both JSON
// and Markdown reports, and keeps an immutable copy of every input file.
func ImportLegacyJSON(ctx context.Context, database *sql.DB, st *store.Store, backupRoot string) (*ImportReport, error) {
	if database == nil || st == nil {
		return nil, errors.New("db: database and store are required")
	}
	when := time.Now().UTC()
	destination := filepath.Join(backupRoot, when.Format("20060102T150405Z"))
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return nil, fmt.Errorf("db: create backup directory: %w", err)
	}
	report := &ImportReport{
		JSONPath:     filepath.Join(destination, "migration-report.json"),
		MarkdownPath: filepath.Join(destination, "migration-report.md"),
	}
	type source struct {
		name string
		body []byte
	}
	var sources []source
	for _, name := range []string{types.FileChannels, types.FileAggregates, types.FileModelHealth} {
		body, err := os.ReadFile(filepath.Join(st.Dir(), name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report, fmt.Errorf("db: read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(destination, name), body, 0o600); err != nil {
			return report, fmt.Errorf("db: backup %s: %w", name, err)
		}
		sources = append(sources, source{name: name, body: body})
	}
	if len(sources) == 0 {
		return report, writeImportReport(report)
	}

	err := WithTx(ctx, database, func(tx *sql.Tx) error {
		for _, source := range sources {
			file := ImportFileReport{Source: source.name}
			checksum := checksum(source.body)
			var previous string
			err := tx.QueryRowContext(ctx, `SELECT source_checksum FROM data_imports WHERE source_name = ?`, source.name).Scan(&previous)
			if err == nil {
				if previous != checksum {
					return fmt.Errorf("db: legacy source %s changed after import", source.name)
				}
				file.Skipped = append(file.Skipped, "already imported")
				report.Files = append(report.Files, file)
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			switch source.name {
			case types.FileChannels:
				var values []types.Channel
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				file.Skipped = append(file.Skipped, unknownChannelFields(source.body)...)
				if err := importChannels(ctx, tx, values); err != nil {
					return err
				}
			case types.FileAggregates:
				var values []types.AggregateModel
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importAggregates(ctx, tx, values); err != nil {
					return err
				}
			case types.FileModelHealth:
				var values []types.ModelHealth
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importHealth(ctx, tx, values); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO data_imports(source_name, source_checksum, imported_at, report_path) VALUES (?, ?, ?, ?)`, source.name, checksum, when.Format(time.RFC3339Nano), report.JSONPath); err != nil {
				return err
			}
			file.Mapped = append(file.Mapped, source.name)
			report.Files = append(report.Files, file)
		}
		return nil
	})
	if err != nil {
		if len(report.Files) == 0 {
			report.Files = append(report.Files, ImportFileReport{Source: "routing import", Failed: []string{err.Error()}})
		}
		_ = writeImportReport(report)
		return report, err
	}
	return report, writeImportReport(report)
}

func importChannels(ctx context.Context, tx *sql.Tx, values []types.Channel) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for position, channel := range values {
		manual := channel.ManualEnabled || channel.Enabled
		if _, err := tx.ExecContext(ctx, `INSERT INTO channels(id, position, name, base_url, api_key_cipher, manual_enabled, sync_billing, models_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, channel.ID, position, channel.Name, channel.BaseURL, channel.APIKeyCipher, manual, channel.SyncBilling, channel.ModelsError, now, now); err != nil {
			return fmt.Errorf("db: import channel %q: %w", channel.ID, err)
		}
		for _, model := range channel.Models {
			if _, err := tx.ExecContext(ctx, `INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES (?, ?, 'legacy', 1, ?, ?)`, channel.ID, model, now, now); err != nil {
				return fmt.Errorf("db: import channel model %q/%q: %w", channel.ID, model, err)
			}
		}
	}
	return nil
}

func importAggregates(ctx context.Context, tx *sql.Tx, values []types.AggregateModel) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, aggregate := range values {
		result, err := tx.ExecContext(ctx, `INSERT INTO aggregates(name, enabled, created_at, updated_at) VALUES (?, 1, ?, ?)`, aggregate.Name, now, now)
		if err != nil {
			return fmt.Errorf("db: import aggregate %q: %w", aggregate.Name, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for position, target := range aggregate.Targets {
			// 渠道级 / Key 多选时 channel_id 为空 → 写 NULL（空字符串会被外键当有效值 → FK 失败）。
			var channelID any
			if target.ChannelID != "" {
				channelID = target.ChannelID
			}
			channelIDsJSON := "[]"
			if len(target.ChannelIDs) > 0 {
				data, err := json.Marshal(target.ChannelIDs)
				if err != nil {
					return fmt.Errorf("db: import aggregate %q target %d channel_ids: %w", aggregate.Name, position, err)
				}
				channelIDsJSON = string(data)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO aggregate_targets(aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url) VALUES(?, ?, ?, ?, ?, ?)`, id, position, target.Model, channelID, channelIDsJSON, target.ChannelBaseURL); err != nil {
				return fmt.Errorf("db: import aggregate target %q: %w", aggregate.Name, err)
			}
		}
	}
	return nil
}

func importHealth(ctx context.Context, tx *sql.Tx, values []types.ModelHealth) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, value := range values {
		model, channel, ok := strings.Cut(value.Model, "@")
		if !ok || model == "" || channel == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count, last_error, last_failure_class, last_checked_at, updated_at) VALUES (?, ?, 1, ?, ?, ?, ?, '', ?, ?) ON CONFLICT(channel_id, model) DO UPDATE SET status=excluded.status, disabled_until=excluded.disabled_until, fail_count=excluded.fail_count, last_error=excluded.last_error, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at`, channel, model, value.Status, value.DisabledUntil, value.FailCount, value.LastError, value.LastChecked, now); err != nil {
			return fmt.Errorf("db: import model health %q: %w", value.Model, err)
		}
	}
	return nil
}

func unknownChannelFields(body []byte) []string {
	var values []map[string]json.RawMessage
	if json.Unmarshal(body, &values) != nil {
		return nil
	}
	known := map[string]bool{"id": true, "name": true, "base_url": true, "api_key_cipher": true, "enabled": true, "manual_enabled": true, "sync_billing": true, "models": true, "models_error": true, "created_at": true, "updated_at": true}
	var skipped []string
	for _, value := range values {
		for field := range value {
			if !known[field] {
				skipped = append(skipped, "unknown field: "+field)
			}
		}
	}
	return skipped
}

func writeImportReport(report *ImportReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(report.JSONPath, data, 0o600); err != nil {
		return err
	}
	var markdown strings.Builder
	for _, file := range report.Files {
		fmt.Fprintf(&markdown, "## %s\n\n### Mapped\n%s\n\n### Skipped\n%s\n\n### Failed\n%s\n\n", file.Source, strings.Join(file.Mapped, "\n"), strings.Join(file.Skipped, "\n"), strings.Join(file.Failed, "\n"))
	}
	return os.WriteFile(report.MarkdownPath, []byte(markdown.String()), 0o600)
}

func checksum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
