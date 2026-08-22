package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Channel is a persisted upstream configuration and its model catalog.
type Channel struct {
	ID            string         `json:"id"`
	Position      int            `json:"-"`
	Name          string         `json:"name"`         // Key 名称
	ChannelName   string         `json:"channel_name"` // 渠道名称（Base URL 组级，同组同步一致）
	BaseURL       string         `json:"base_url"`
	APIKeyCipher  string         `json:"api_key_cipher"`
	ManualEnabled bool           `json:"manual_enabled"`
	SyncBilling   bool           `json:"sync_billing"`
	ModelsError   string         `json:"models_error"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	Models        []ChannelModel `json:"models"`
}

// ChannelModel is one model discovered for a channel.
type ChannelModel struct {
	Model       string `json:"model"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

// Aggregate is a virtual model and its ordered routing targets.
type Aggregate struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	Targets   []AggregateTarget `json:"targets"`
}

// AggregateTarget pins an aggregate position to a model on one channel.
// ChannelBaseURL 优先（渠道级，按 base_url 组轮询 Key）；其次 ChannelIDs（Key 多选）；最后 ChannelID（兼容单 Key）。
type AggregateTarget struct {
	Position       int      `json:"position"`
	Model          string   `json:"model"`
	ChannelID      string   `json:"channel_id"`
	ChannelIDs     []string `json:"channel_ids"`
	ChannelBaseURL string   `json:"channel_base_url"`
}

// Repository provides the small configuration CRUD surface shared by routing
// plugins. It intentionally leaves routing policy and state transitions out.
type Repository struct{ database *sql.DB }

// NewRepository binds repository operations to an opened database.
func NewRepository(database *sql.DB) (*Repository, error) {
	if database == nil {
		return nil, fmt.Errorf("db: nil database")
	}
	return &Repository{database: database}, nil
}

// Transaction executes fn atomically.
func (r *Repository) Transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	return WithTx(ctx, r.database, fn)
}

// WithTx executes fn in a transaction. It is available to plugins needing a
// short transaction beyond the configuration repositories.
func WithTx(ctx context.Context, database *sql.DB, fn func(*sql.Tx) error) error {
	if database == nil || fn == nil {
		return fmt.Errorf("db: database and transaction function are required")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit transaction: %w", err)
	}
	return nil
}

// ListChannels returns channels and their complete model catalogs.
func (r *Repository) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT id, position, name, channel_name, base_url, api_key_cipher, manual_enabled, sync_billing, models_error, created_at, updated_at FROM channels ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("db: list channels: %w", err)
	}
	defer rows.Close()

	channels := []Channel{}
	byID := make(map[string]int)
	for rows.Next() {
		var channel Channel
		if err := rows.Scan(&channel.ID, &channel.Position, &channel.Name, &channel.ChannelName, &channel.BaseURL, &channel.APIKeyCipher, &channel.ManualEnabled, &channel.SyncBilling, &channel.ModelsError, &channel.CreatedAt, &channel.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan channel: %w", err)
		}
		byID[channel.ID] = len(channels)
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate channels: %w", err)
	}

	modelRows, err := r.database.QueryContext(ctx, `SELECT channel_id, model, source, enabled, first_seen_at, last_seen_at FROM channel_models ORDER BY channel_id, model`)
	if err != nil {
		return nil, fmt.Errorf("db: list channel models: %w", err)
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var channelID string
		var model ChannelModel
		if err := modelRows.Scan(&channelID, &model.Model, &model.Source, &model.Enabled, &model.FirstSeenAt, &model.LastSeenAt); err != nil {
			return nil, fmt.Errorf("db: scan channel model: %w", err)
		}
		channels[byID[channelID]].Models = append(channels[byID[channelID]].Models, model)
	}
	if err := modelRows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate channel models: %w", err)
	}
	return channels, nil
}

// ReplaceChannels atomically makes the channel configurations and catalogs
// match channels. Foreign-key failures prevent deletion of referenced channels.
func (r *Repository) ReplaceChannels(ctx context.Context, channels []Channel) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		seen := make(map[string]struct{}, len(channels))
		for i := range channels {
			channel := channels[i]
			if channel.ID == "" || channel.Name == "" || channel.BaseURL == "" {
				return fmt.Errorf("db: channels[%d]: id, name, and base_url are required", i)
			}
			if _, ok := seen[channel.ID]; ok {
				return fmt.Errorf("db: channels[%d]: duplicate id %q", i, channel.ID)
			}
			seen[channel.ID] = struct{}{}
			now := nowString()
			if channel.CreatedAt == "" {
				channel.CreatedAt = now
			}
			if channel.UpdatedAt == "" {
				channel.UpdatedAt = now
			}
			channel.Position = i
			if _, err := tx.ExecContext(ctx, `INSERT INTO channels (id, position, name, channel_name, base_url, api_key_cipher, manual_enabled, sync_billing, models_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET position = excluded.position, name = excluded.name, channel_name = excluded.channel_name, base_url = excluded.base_url, api_key_cipher = excluded.api_key_cipher, manual_enabled = excluded.manual_enabled, sync_billing = excluded.sync_billing, models_error = excluded.models_error, updated_at = excluded.updated_at`, channel.ID, channel.Position, channel.Name, channel.ChannelName, channel.BaseURL, channel.APIKeyCipher, channel.ManualEnabled, channel.SyncBilling, channel.ModelsError, channel.CreatedAt, channel.UpdatedAt); err != nil {
				return fmt.Errorf("db: replace channel %q: %w", channel.ID, err)
			}
			if err := replaceChannelModels(ctx, tx, channel.ID, channel.Models); err != nil {
				return err
			}
		}

		rows, err := tx.QueryContext(ctx, "SELECT id FROM channels")
		if err != nil {
			return fmt.Errorf("db: list channels to delete: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("db: scan channel to delete: %w", err)
			}
			if _, ok := seen[id]; !ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM channels WHERE id = ?", id); err != nil {
					return fmt.Errorf("db: delete channel %q: %w", id, err)
				}
			}
		}
		return rows.Err()
	})
}

// ListChannelModels returns a channel's catalog.
func (r *Repository) ListChannelModels(ctx context.Context, channelID string) ([]ChannelModel, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT model, source, enabled, first_seen_at, last_seen_at FROM channel_models WHERE channel_id = ? ORDER BY model`, channelID)
	if err != nil {
		return nil, fmt.Errorf("db: list channel models: %w", err)
	}
	defer rows.Close()
	models := []ChannelModel{}
	for rows.Next() {
		var model ChannelModel
		if err := rows.Scan(&model.Model, &model.Source, &model.Enabled, &model.FirstSeenAt, &model.LastSeenAt); err != nil {
			return nil, fmt.Errorf("db: scan channel model: %w", err)
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate channel models: %w", err)
	}
	return models, nil
}

// ReplaceChannelModels atomically replaces one channel's catalog.
func (r *Repository) ReplaceChannelModels(ctx context.Context, channelID string, models []ChannelModel) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error { return replaceChannelModels(ctx, tx, channelID, models) })
}

func replaceChannelModels(ctx context.Context, tx *sql.Tx, channelID string, models []ChannelModel) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE id = ?)", channelID).Scan(&exists); err != nil {
		return fmt.Errorf("db: find channel %q: %w", channelID, err)
	}
	if !exists {
		return fmt.Errorf("db: channel %q does not exist", channelID)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM channel_models WHERE channel_id = ?", channelID); err != nil {
		return fmt.Errorf("db: clear channel models for %q: %w", channelID, err)
	}
	// Prepared statement 复用：渠道模型可能上百个，逐条 Exec 会反复 prepare，
	// 复用一个 statement 显著降低保存延迟。
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO channel_models (channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("db: prepare channel model insert: %w", err)
	}
	defer stmt.Close()
	seen := make(map[string]struct{}, len(models))
	for i := range models {
		model := models[i]
		if model.Model == "" {
			return fmt.Errorf("db: channel %q models[%d]: model is required", channelID, i)
		}
		if _, ok := seen[model.Model]; ok {
			return fmt.Errorf("db: channel %q models[%d]: duplicate model %q", channelID, i, model.Model)
		}
		seen[model.Model] = struct{}{}
		now := nowString()
		if model.Source == "" {
			model.Source = "probe"
		}
		if model.FirstSeenAt == "" {
			model.FirstSeenAt = now
		}
		if model.LastSeenAt == "" {
			model.LastSeenAt = now
		}
		if _, err := stmt.ExecContext(ctx, channelID, model.Model, model.Source, model.Enabled, model.FirstSeenAt, model.LastSeenAt); err != nil {
			return fmt.Errorf("db: replace channel %q model %q: %w", channelID, model.Model, err)
		}
	}
	return nil
}

// UpdateChannelModelsError updates a channel's models_error field.
func (r *Repository) UpdateChannelModelsError(ctx context.Context, channelID, modelsError string) error {
	if _, err := r.database.ExecContext(ctx, `UPDATE channels SET models_error = ?, updated_at = ? WHERE id = ?`, modelsError, nowString(), channelID); err != nil {
		return fmt.Errorf("db: update channel %q models error: %w", channelID, err)
	}
	return nil
}

// ListAggregates returns virtual models with ordered targets.
func (r *Repository) ListAggregates(ctx context.Context) ([]Aggregate, error) {
	rows, err := r.database.QueryContext(ctx, "SELECT id, name, enabled, created_at, updated_at FROM aggregates ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("db: list aggregates: %w", err)
	}
	defer rows.Close()
	aggregates := []Aggregate{}
	byID := make(map[int64]int)
	for rows.Next() {
		var aggregate Aggregate
		if err := rows.Scan(&aggregate.ID, &aggregate.Name, &aggregate.Enabled, &aggregate.CreatedAt, &aggregate.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan aggregate: %w", err)
		}
		byID[aggregate.ID] = len(aggregates)
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate aggregates: %w", err)
	}

	targetRows, err := r.database.QueryContext(ctx, "SELECT aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url FROM aggregate_targets ORDER BY aggregate_id, position")
	if err != nil {
		return nil, fmt.Errorf("db: list aggregate targets: %w", err)
	}
	defer targetRows.Close()
	for targetRows.Next() {
		var aggregateID int64
		var target AggregateTarget
		var channelID sql.NullString
		var channelIDsJSON string
		if err := targetRows.Scan(&aggregateID, &target.Position, &target.Model, &channelID, &channelIDsJSON, &target.ChannelBaseURL); err != nil {
			return nil, fmt.Errorf("db: scan aggregate target: %w", err)
		}
		if channelID.Valid {
			target.ChannelID = channelID.String
		}
		if channelIDsJSON != "" && channelIDsJSON != "[]" {
			if err := json.Unmarshal([]byte(channelIDsJSON), &target.ChannelIDs); err != nil {
				return nil, fmt.Errorf("db: parse aggregate target channel_ids: %w", err)
			}
		}
		aggregates[byID[aggregateID]].Targets = append(aggregates[byID[aggregateID]].Targets, target)
	}
	if err := targetRows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate aggregate targets: %w", err)
	}
	return aggregates, nil
}

// ReplaceAggregates atomically replaces virtual models and their targets.
func (r *Repository) ReplaceAggregates(ctx context.Context, aggregates []Aggregate) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		seen := make(map[string]struct{}, len(aggregates))
		for i := range aggregates {
			aggregate := aggregates[i]
			if aggregate.Name == "" {
				return fmt.Errorf("db: aggregates[%d]: name is required", i)
			}
			if _, ok := seen[aggregate.Name]; ok {
				return fmt.Errorf("db: aggregates[%d]: duplicate name %q", i, aggregate.Name)
			}
			seen[aggregate.Name] = struct{}{}
			now := nowString()
			if aggregate.CreatedAt == "" {
				aggregate.CreatedAt = now
			}
			if aggregate.UpdatedAt == "" {
				aggregate.UpdatedAt = now
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO aggregates (name, enabled, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`, aggregate.Name, aggregate.Enabled, aggregate.CreatedAt, aggregate.UpdatedAt); err != nil {
				return fmt.Errorf("db: replace aggregate %q: %w", aggregate.Name, err)
			}
			var id int64
			if err := tx.QueryRowContext(ctx, "SELECT id FROM aggregates WHERE name = ?", aggregate.Name).Scan(&id); err != nil {
				return fmt.Errorf("db: find aggregate %q: %w", aggregate.Name, err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM aggregate_targets WHERE aggregate_id = ?", id); err != nil {
				return fmt.Errorf("db: clear aggregate %q targets: %w", aggregate.Name, err)
			}
			for position, target := range aggregate.Targets {
				// 校验：model 必填；channel_id / channel_ids / channel_base_url 至少一个非空。
				if target.Model == "" {
					return fmt.Errorf("db: aggregate %q targets[%d]: model is required", aggregate.Name, position)
				}
				if target.ChannelID == "" && len(target.ChannelIDs) == 0 && target.ChannelBaseURL == "" {
					return fmt.Errorf("db: aggregate %q targets[%d]: channel_id, channel_ids or channel_base_url is required", aggregate.Name, position)
				}
			channelIDsJSON := "[]"
			if len(target.ChannelIDs) > 0 {
				data, err := json.Marshal(target.ChannelIDs)
				if err != nil {
					return fmt.Errorf("db: marshal aggregate %q target %d channel_ids: %w", aggregate.Name, position, err)
				}
				channelIDsJSON = string(data)
			}
			// 渠道级 / Key 多选时 channel_id 为空：必须写 NULL（空字符串会被外键当有效值 → FK 失败）。
			var channelID any
			if target.ChannelID != "" {
				channelID = target.ChannelID
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO aggregate_targets (aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url) VALUES (?, ?, ?, ?, ?, ?)", id, position, target.Model, channelID, channelIDsJSON, target.ChannelBaseURL); err != nil {
				return fmt.Errorf("db: replace aggregate %q target %d: %w", aggregate.Name, position, err)
			}
			}
		}

		rows, err := tx.QueryContext(ctx, "SELECT name FROM aggregates")
		if err != nil {
			return fmt.Errorf("db: list aggregates to delete: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("db: scan aggregate to delete: %w", err)
			}
			if _, ok := seen[name]; !ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM aggregates WHERE name = ?", name); err != nil {
					return fmt.Errorf("db: delete aggregate %q: %w", name, err)
				}
			}
		}
		return rows.Err()
	})
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }
