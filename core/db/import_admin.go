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
	"time"

	"loadout/core/store"
	"loadout/plugins/types"
)

// ImportAdminJSON migrates the remaining admin config JSON files (capability
// routes, MCP servers, groups, tool states, skills, presets, settings,
// gateway keys, users) into SQLite tables created by migration v5.
// It is idempotent: each source file is tracked in data_imports by checksum,
// and re-running with unchanged sources is a no-op.
func ImportAdminJSON(ctx context.Context, database *sql.DB, st *store.Store) error {
	if database == nil || st == nil {
		return errors.New("db: database and store are required")
	}
	backupRoot := filepath.Join(filepath.Dir(st.Dir()), "backups")
	when := time.Now().UTC()
	destination := filepath.Join(backupRoot, when.Format("20060102T150405Z"))
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("db: create backup directory: %w", err)
	}

	type source struct {
		name string
		body []byte
	}
	var sources []source
	for _, name := range []string{
		types.FileCapabilityRoutes,
		types.FileMCPServers,
		types.FileGroups,
		types.FileToolsState,
		types.FileSkills,
		types.FilePresets,
		types.FileSettings,
		types.FileAPIKeys,
		types.FileMCPKeys,
		types.FileUsers,
	} {
		body, err := os.ReadFile(filepath.Join(st.Dir(), name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("db: read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(destination, name), body, 0o600); err != nil {
			return fmt.Errorf("db: backup %s: %w", name, err)
		}
		sources = append(sources, source{name: name, body: body})
	}
	if len(sources) == 0 {
		return nil
	}

	return WithTx(ctx, database, func(tx *sql.Tx) error {
		for _, source := range sources {
			checksum := sha256Hex(source.body)
			var previous string
			err := tx.QueryRowContext(ctx, `SELECT source_checksum FROM data_imports WHERE source_name = ?`, source.name).Scan(&previous)
			if err == nil {
				if previous != checksum {
					return fmt.Errorf("db: admin source %s changed after import", source.name)
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			switch source.name {
			case types.FileCapabilityRoutes:
				var values []types.CapabilityRoute
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importCapabilityRoutes(ctx, tx, values); err != nil {
					return err
				}
			case types.FileMCPServers:
				var values []types.MCPServer
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importMCPServers(ctx, tx, values); err != nil {
					return err
				}
			case types.FileGroups:
				var values []types.Group
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importGroups(ctx, tx, values); err != nil {
					return err
				}
			case types.FileToolsState:
				var values []types.ToolState
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importToolStates(ctx, tx, values); err != nil {
					return err
				}
			case types.FileSkills:
				var values []types.Skill
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importSkills(ctx, tx, values); err != nil {
					return err
				}
			case types.FilePresets:
				var values []types.Preset
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importPresets(ctx, tx, values); err != nil {
					return err
				}
			case types.FileSettings:
				var value types.Settings
				if err := json.Unmarshal(source.body, &value); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importSettings(ctx, tx, value); err != nil {
					return err
				}
			case types.FileAPIKeys:
				var values []types.APIKey
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importAPIKeys(ctx, tx, values); err != nil {
					return err
				}
			case types.FileMCPKeys:
				var values []types.MCPKey
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importMCPKeys(ctx, tx, values); err != nil {
					return err
				}
			case types.FileUsers:
				var values []types.User
				if err := json.Unmarshal(source.body, &values); err != nil {
					return fmt.Errorf("db: parse %s: %w", source.name, err)
				}
				if err := importUsers(ctx, tx, values); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO data_imports(source_name, source_checksum, imported_at, report_path) VALUES (?, ?, ?, ?)`, source.name, checksum, when.Format("20060102T150405Z"), ""); err != nil {
				return err
			}
		}
		return nil
	})
}

func importCapabilityRoutes(ctx context.Context, tx *sql.Tx, values []types.CapabilityRoute) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM capability_routes"); err != nil {
		return fmt.Errorf("db: clear capability_routes: %w", err)
	}
	for i, route := range values {
		models, _ := json.Marshal(route.Models)
		channels, _ := json.Marshal(route.ChannelIDs)
		baseURLs, _ := json.Marshal(route.ChannelBaseURLs)
		via, _ := json.Marshal(route.ViaOptions)
		repl, _ := json.Marshal(route.Replacements)
		fieldRulesJSON := "{}"
		if route.FieldRules != nil {
			if b, err := json.Marshal(route.FieldRules); err == nil {
				fieldRulesJSON = string(b)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO capability_routes (position, capability, route, models_json, channel_ids_json, channel_base_urls_json, via_options_json, replacements_json, field_rules_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, i, route.Capability, route.Route, string(models), string(channels), string(baseURLs), string(via), string(repl), fieldRulesJSON); err != nil {
			return fmt.Errorf("db: import capability route: %w", err)
		}
	}
	return nil
}

func importMCPServers(ctx context.Context, tx *sql.Tx, values []types.MCPServer) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_servers"); err != nil {
		return fmt.Errorf("db: clear mcp_servers: %w", err)
	}
	for i, srv := range values {
		args, _ := json.Marshal(srv.Args)
		env, _ := json.Marshal(srv.Env)
		headers, _ := json.Marshal(srv.Headers)
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_servers (id, position, name, description, transport, command, args_json, env_json, url, headers_json, enabled, builtin) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, srv.ID, i, srv.Name, srv.Description, srv.Transport, srv.Command, string(args), string(env), srv.URL, string(headers), boolInt(srv.Enabled), boolInt(srv.Builtin)); err != nil {
			return fmt.Errorf("db: import mcp server %q: %w", srv.Name, err)
		}
	}
	return nil
}

func importGroups(ctx context.Context, tx *sql.Tx, values []types.Group) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_groups"); err != nil {
		return fmt.Errorf("db: clear mcp_groups: %w", err)
	}
	for i, group := range values {
		tools, _ := json.Marshal(group.Tools)
		if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_groups (name, position, tools_json) VALUES (?, ?, ?)`, group.Name, i, string(tools)); err != nil {
			return fmt.Errorf("db: import group %q: %w", group.Name, err)
		}
	}
	return nil
}

func importToolStates(ctx context.Context, tx *sql.Tx, values []types.ToolState) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM tools_state"); err != nil {
		return fmt.Errorf("db: clear tools_state: %w", err)
	}
	for _, state := range values {
		tags, _ := json.Marshal(state.Tags)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tools_state (server_id, tool_name, enabled, category, tags_json) VALUES (?, ?, ?, ?, ?)`, state.ServerID, state.ToolName, boolInt(state.Enabled), state.Category, string(tags)); err != nil {
			return fmt.Errorf("db: import tool state %q/%q: %w", state.ServerID, state.ToolName, err)
		}
	}
	return nil
}

func importSkills(ctx context.Context, tx *sql.Tx, values []types.Skill) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM skills"); err != nil {
		return fmt.Errorf("db: clear skills: %w", err)
	}
	for _, skill := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO skills (name, description, source, installed_at, version, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, skill.Name, skill.Description, skill.Source, skill.InstalledAt, skill.Version, skill.UpdatedAt); err != nil {
			return fmt.Errorf("db: import skill %q: %w", skill.Name, err)
		}
	}
	return nil
}

func importPresets(ctx context.Context, tx *sql.Tx, values []types.Preset) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM presets"); err != nil {
		return fmt.Errorf("db: clear presets: %w", err)
	}
	for _, preset := range values {
		skills, _ := json.Marshal(preset.Skills)
		targets, _ := json.Marshal(preset.Targets)
		if _, err := tx.ExecContext(ctx, `INSERT INTO presets (name, skills_json, target, targets_json) VALUES (?, ?, ?, ?)`, preset.Name, string(skills), preset.Target, string(targets)); err != nil {
			return fmt.Errorf("db: import preset %q: %w", preset.Name, err)
		}
	}
	return nil
}

func importSettings(ctx context.Context, tx *sql.Tx, value types.Settings) error {
	targets, _ := json.Marshal(value.ActivePresetTargets)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (id, active_preset, active_preset_target, active_preset_targets_json, default_model, use_global_cmd) VALUES (1, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET active_preset = excluded.active_preset, active_preset_target = excluded.active_preset_target, active_preset_targets_json = excluded.active_preset_targets_json, default_model = excluded.default_model, use_global_cmd = excluded.use_global_cmd`, value.ActivePreset, value.ActivePresetTarget, string(targets), value.DefaultModel, boolInt(value.UseGlobalCmd)); err != nil {
		return fmt.Errorf("db: import settings: %w", err)
	}
	return nil
}

func importAPIKeys(ctx context.Context, tx *sql.Tx, values []types.APIKey) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM gateway_keys WHERE kind = 'api'"); err != nil {
		return fmt.Errorf("db: clear api keys: %w", err)
	}
	for _, key := range values {
		models, _ := json.Marshal(key.Models)
		if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_keys (id, kind, name, prefix, hash, api_key_cipher, models_json, enabled, created_at, endpoint, header_name) VALUES (?, 'api', ?, ?, ?, ?, ?, ?, ?, '', '')`, key.ID, key.Name, key.Prefix, key.Hash, key.Cipher, string(models), boolInt(key.Enabled), key.CreatedAt); err != nil {
			return fmt.Errorf("db: import api key %q: %w", key.ID, err)
		}
	}
	return nil
}

func importMCPKeys(ctx context.Context, tx *sql.Tx, values []types.MCPKey) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM gateway_keys WHERE kind = 'mcp'"); err != nil {
		return fmt.Errorf("db: clear mcp keys: %w", err)
	}
	for _, key := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_keys (id, kind, name, prefix, hash, models_json, enabled, created_at, endpoint, header_name) VALUES (?, 'mcp', '', '', ?, '[]', 1, '', ?, ?)`, key.Endpoint, key.Hash, key.Endpoint, key.HeaderName); err != nil {
			return fmt.Errorf("db: import mcp key %q: %w", key.Endpoint, err)
		}
	}
	return nil
}

func importUsers(ctx context.Context, tx *sql.Tx, values []types.User) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM users"); err != nil {
		return fmt.Errorf("db: clear users: %w", err)
	}
	for _, user := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, password_changed) VALUES (?, ?, ?)`, user.Username, user.PasswordHash, boolInt(user.PasswordChanged)); err != nil {
			return fmt.Errorf("db: import user %q: %w", user.Username, err)
		}
	}
	return nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
