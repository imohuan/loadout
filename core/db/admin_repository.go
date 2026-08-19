package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"loadout/plugins/types"
)

// 管理后台配置的 SQLite 持久化（migration v5）。
// 嵌套结构（models/via_options/replacements/args/env/headers/tools/tags/skills/targets）
// 以 JSON 文本列存储，与旧 JSON 文件语义一致；settings 为单行表（id=1）。
// 所有方法直接以 types 包结构进出，避免插件层再做 JSON↔结构体转换。

// ==================== 能力路由 ====================

// ListCapabilityRoutes 返回全部能力路由（按 position 排序）。
func (r *Repository) ListCapabilityRoutes(ctx context.Context) ([]types.CapabilityRoute, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT capability, route, models_json, channel_ids_json, via_options_json, replacements_json FROM capability_routes ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("db: list capability routes: %w", err)
	}
	defer rows.Close()
	var routes []types.CapabilityRoute
	for rows.Next() {
		var route types.CapabilityRoute
		var modelsJSON, channelsJSON, viaJSON, replJSON string
		if err := rows.Scan(&route.Capability, &route.Route, &modelsJSON, &channelsJSON, &viaJSON, &replJSON); err != nil {
			return nil, fmt.Errorf("db: scan capability route: %w", err)
		}
		if err := unmarshalEach(modelsJSON, &route.Models, channelsJSON, &route.ChannelIDs, viaJSON, &route.ViaOptions, replJSON, &route.Replacements); err != nil {
			return nil, fmt.Errorf("db: parse capability route: %w", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate capability routes: %w", err)
	}
	return routes, nil
}

// ReplaceCapabilityRoutes 整体替换能力路由表。
func (r *Repository) ReplaceCapabilityRoutes(ctx context.Context, routes []types.CapabilityRoute) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM capability_routes"); err != nil {
			return fmt.Errorf("db: clear capability routes: %w", err)
		}
		for i := range routes {
			route := routes[i]
			models, err := json.Marshal(route.Models)
			if err != nil {
				return fmt.Errorf("db: marshal route models: %w", err)
			}
			channels, err := json.Marshal(route.ChannelIDs)
			if err != nil {
				return fmt.Errorf("db: marshal route channels: %w", err)
			}
			via, err := json.Marshal(route.ViaOptions)
			if err != nil {
				return fmt.Errorf("db: marshal route via options: %w", err)
			}
			repl, err := json.Marshal(route.Replacements)
			if err != nil {
				return fmt.Errorf("db: marshal route replacements: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO capability_routes (position, capability, route, models_json, channel_ids_json, via_options_json, replacements_json) VALUES (?, ?, ?, ?, ?, ?, ?)`, i, route.Capability, route.Route, string(models), string(channels), string(via), string(repl)); err != nil {
				return fmt.Errorf("db: insert capability route: %w", err)
			}
		}
		return nil
	})
}

// ==================== MCP 服务器 ====================

// ListMCPServers 返回全部 MCP 服务器（按 position 排序）。
func (r *Repository) ListMCPServers(ctx context.Context) ([]types.MCPServer, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT id, name, description, transport, command, args_json, env_json, url, headers_json, enabled FROM mcp_servers ORDER BY position, name`)
	if err != nil {
		return nil, fmt.Errorf("db: list mcp servers: %w", err)
	}
	defer rows.Close()
	var servers []types.MCPServer
	for rows.Next() {
		var srv types.MCPServer
		var argsJSON, envJSON, headersJSON string
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.Description, &srv.Transport, &srv.Command, &argsJSON, &envJSON, &srv.URL, &headersJSON, &srv.Enabled); err != nil {
			return nil, fmt.Errorf("db: scan mcp server: %w", err)
		}
		if err := unmarshalEach(argsJSON, &srv.Args, envJSON, &srv.Env, headersJSON, &srv.Headers); err != nil {
			return nil, fmt.Errorf("db: parse mcp server %q: %w", srv.Name, err)
		}
		servers = append(servers, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate mcp servers: %w", err)
	}
	return servers, nil
}

// ReplaceMCPServers 整体替换 MCP 服务器列表。
func (r *Repository) ReplaceMCPServers(ctx context.Context, servers []types.MCPServer) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_servers"); err != nil {
			return fmt.Errorf("db: clear mcp servers: %w", err)
		}
		for i := range servers {
			srv := servers[i]
			args, err := json.Marshal(srv.Args)
			if err != nil {
				return fmt.Errorf("db: marshal server args: %w", err)
			}
			env, err := json.Marshal(srv.Env)
			if err != nil {
				return fmt.Errorf("db: marshal server env: %w", err)
			}
			headers, err := json.Marshal(srv.Headers)
			if err != nil {
				return fmt.Errorf("db: marshal server headers: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_servers (id, position, name, description, transport, command, args_json, env_json, url, headers_json, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, srv.ID, i, srv.Name, srv.Description, srv.Transport, srv.Command, string(args), string(env), srv.URL, string(headers), boolInt(srv.Enabled)); err != nil {
				return fmt.Errorf("db: insert mcp server %q: %w", srv.Name, err)
			}
		}
		return nil
	})
}

// ==================== 分组 ====================

// ListGroups 返回全部分组（按 position 排序）。
func (r *Repository) ListGroups(ctx context.Context) ([]types.Group, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT name, tools_json FROM mcp_groups ORDER BY position, name`)
	if err != nil {
		return nil, fmt.Errorf("db: list groups: %w", err)
	}
	defer rows.Close()
	var groups []types.Group
	for rows.Next() {
		var group types.Group
		var toolsJSON string
		if err := rows.Scan(&group.Name, &toolsJSON); err != nil {
			return nil, fmt.Errorf("db: scan group: %w", err)
		}
		if err := json.Unmarshal([]byte(toolsJSON), &group.Tools); err != nil {
			return nil, fmt.Errorf("db: parse group %q tools: %w", group.Name, err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate groups: %w", err)
	}
	return groups, nil
}

// ReplaceGroups 整体替换分组列表。
func (r *Repository) ReplaceGroups(ctx context.Context, groups []types.Group) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM mcp_groups"); err != nil {
			return fmt.Errorf("db: clear groups: %w", err)
		}
		for i := range groups {
			group := groups[i]
			tools, err := json.Marshal(group.Tools)
			if err != nil {
				return fmt.Errorf("db: marshal group tools: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mcp_groups (name, position, tools_json) VALUES (?, ?, ?)`, group.Name, i, string(tools)); err != nil {
				return fmt.Errorf("db: insert group %q: %w", group.Name, err)
			}
		}
		return nil
	})
}

// ==================== 工具开关 ====================

// ListToolStates 返回全部工具开关。
func (r *Repository) ListToolStates(ctx context.Context) ([]types.ToolState, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT server_id, tool_name, enabled, category, tags_json FROM tools_state ORDER BY server_id, tool_name`)
	if err != nil {
		return nil, fmt.Errorf("db: list tool states: %w", err)
	}
	defer rows.Close()
	var states []types.ToolState
	for rows.Next() {
		var state types.ToolState
		var tagsJSON string
		if err := rows.Scan(&state.ServerID, &state.ToolName, &state.Enabled, &state.Category, &tagsJSON); err != nil {
			return nil, fmt.Errorf("db: scan tool state: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &state.Tags); err != nil {
			return nil, fmt.Errorf("db: parse tool state tags: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate tool states: %w", err)
	}
	return states, nil
}

// ReplaceToolStates 整体替换工具开关列表。
func (r *Repository) ReplaceToolStates(ctx context.Context, states []types.ToolState) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM tools_state"); err != nil {
			return fmt.Errorf("db: clear tool states: %w", err)
		}
		for _, state := range states {
			tags, err := json.Marshal(state.Tags)
			if err != nil {
				return fmt.Errorf("db: marshal state tags: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO tools_state (server_id, tool_name, enabled, category, tags_json) VALUES (?, ?, ?, ?, ?)`, state.ServerID, state.ToolName, boolInt(state.Enabled), state.Category, string(tags)); err != nil {
				return fmt.Errorf("db: insert tool state %q/%q: %w", state.ServerID, state.ToolName, err)
			}
		}
		return nil
	})
}

// ==================== 技能清单 ====================

// ListSkills 返回全部技能清单。
func (r *Repository) ListSkills(ctx context.Context) ([]types.Skill, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT name, description, source, installed_at, version, updated_at FROM skills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list skills: %w", err)
	}
	defer rows.Close()
	var skills []types.Skill
	for rows.Next() {
		var skill types.Skill
		if err := rows.Scan(&skill.Name, &skill.Description, &skill.Source, &skill.InstalledAt, &skill.Version, &skill.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan skill: %w", err)
		}
		skills = append(skills, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate skills: %w", err)
	}
	return skills, nil
}

// ReplaceSkills 整体替换技能清单。
func (r *Repository) ReplaceSkills(ctx context.Context, skills []types.Skill) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM skills"); err != nil {
			return fmt.Errorf("db: clear skills: %w", err)
		}
		for _, skill := range skills {
			if _, err := tx.ExecContext(ctx, `INSERT INTO skills (name, description, source, installed_at, version, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, skill.Name, skill.Description, skill.Source, skill.InstalledAt, skill.Version, skill.UpdatedAt); err != nil {
				return fmt.Errorf("db: insert skill %q: %w", skill.Name, err)
			}
		}
		return nil
	})
}

// ==================== 技能预设 ====================

// ListPresets 返回全部技能预设。
func (r *Repository) ListPresets(ctx context.Context) ([]types.Preset, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT name, skills_json, target, targets_json FROM presets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list presets: %w", err)
	}
	defer rows.Close()
	var presets []types.Preset
	for rows.Next() {
		var preset types.Preset
		var skillsJSON, targetsJSON string
		if err := rows.Scan(&preset.Name, &skillsJSON, &preset.Target, &targetsJSON); err != nil {
			return nil, fmt.Errorf("db: scan preset: %w", err)
		}
		if err := unmarshalEach(skillsJSON, &preset.Skills, targetsJSON, &preset.Targets); err != nil {
			return nil, fmt.Errorf("db: parse preset %q: %w", preset.Name, err)
		}
		presets = append(presets, preset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate presets: %w", err)
	}
	return presets, nil
}

// ReplacePresets 整体替换预设列表。
func (r *Repository) ReplacePresets(ctx context.Context, presets []types.Preset) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM presets"); err != nil {
			return fmt.Errorf("db: clear presets: %w", err)
		}
		for _, preset := range presets {
			skills, err := json.Marshal(preset.Skills)
			if err != nil {
				return fmt.Errorf("db: marshal preset skills: %w", err)
			}
			targets, err := json.Marshal(preset.Targets)
			if err != nil {
				return fmt.Errorf("db: marshal preset targets: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO presets (name, skills_json, target, targets_json) VALUES (?, ?, ?, ?)`, preset.Name, string(skills), preset.Target, string(targets)); err != nil {
				return fmt.Errorf("db: insert preset %q: %w", preset.Name, err)
			}
		}
		return nil
	})
}

// ==================== 运行时设置（单行） ====================

// GetSettings 读取运行时设置；不存在返回零值（不报错）。
func (r *Repository) GetSettings(ctx context.Context) (types.Settings, error) {
	var settings types.Settings
	var targetsJSON string
	err := r.database.QueryRowContext(ctx, `SELECT active_preset, active_preset_target, active_preset_targets_json, default_model FROM settings WHERE id = 1`).Scan(&settings.ActivePreset, &settings.ActivePresetTarget, &targetsJSON, &settings.DefaultModel)
	if err != nil {
		if err == sql.ErrNoRows {
			return types.Settings{}, nil
		}
		return types.Settings{}, fmt.Errorf("db: get settings: %w", err)
	}
	if err := json.Unmarshal([]byte(targetsJSON), &settings.ActivePresetTargets); err != nil {
		return types.Settings{}, fmt.Errorf("db: parse settings targets: %w", err)
	}
	return settings, nil
}

// PutSettings 写入运行时设置（id=1 单行 upsert）。
func (r *Repository) PutSettings(ctx context.Context, settings types.Settings) error {
	targets, err := json.Marshal(settings.ActivePresetTargets)
	if err != nil {
		return fmt.Errorf("db: marshal settings targets: %w", err)
	}
	if _, err := r.database.ExecContext(ctx, `INSERT INTO settings (id, active_preset, active_preset_target, active_preset_targets_json, default_model) VALUES (1, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET active_preset = excluded.active_preset, active_preset_target = excluded.active_preset_target, active_preset_targets_json = excluded.active_preset_targets_json, default_model = excluded.default_model`, settings.ActivePreset, settings.ActivePresetTarget, string(targets), settings.DefaultModel); err != nil {
		return fmt.Errorf("db: put settings: %w", err)
	}
	return nil
}

// ==================== 网关密钥 ====================

// ListAPIKeys 返回全部模型 API key（kind=api）。
func (r *Repository) ListAPIKeys(ctx context.Context) ([]types.APIKey, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT id, name, prefix, hash, models_json, enabled, created_at FROM gateway_keys WHERE kind = 'api' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("db: list api keys: %w", err)
	}
	defer rows.Close()
	var keys []types.APIKey
	for rows.Next() {
		var key types.APIKey
		var modelsJSON string
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &key.Hash, &modelsJSON, &key.Enabled, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scan api key: %w", err)
		}
		if err := json.Unmarshal([]byte(modelsJSON), &key.Models); err != nil {
			return nil, fmt.Errorf("db: parse api key models: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate api keys: %w", err)
	}
	return keys, nil
}

// ReplaceAPIKeys 整体替换模型 API key 列表。
func (r *Repository) ReplaceAPIKeys(ctx context.Context, keys []types.APIKey) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM gateway_keys WHERE kind = 'api'"); err != nil {
			return fmt.Errorf("db: clear api keys: %w", err)
		}
		for _, key := range keys {
			models, err := json.Marshal(key.Models)
			if err != nil {
				return fmt.Errorf("db: marshal api key models: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_keys (id, kind, name, prefix, hash, models_json, enabled, created_at, endpoint, header_name) VALUES (?, 'api', ?, ?, ?, ?, ?, ?, '', '')`, key.ID, key.Name, key.Prefix, key.Hash, string(models), boolInt(key.Enabled), key.CreatedAt); err != nil {
				return fmt.Errorf("db: insert api key %q: %w", key.ID, err)
			}
		}
		return nil
	})
}

// ListMCPKeys 返回全部 MCP endpoint key（kind=mcp）。
func (r *Repository) ListMCPKeys(ctx context.Context) ([]types.MCPKey, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT endpoint, header_name, hash FROM gateway_keys WHERE kind = 'mcp' ORDER BY endpoint`)
	if err != nil {
		return nil, fmt.Errorf("db: list mcp keys: %w", err)
	}
	defer rows.Close()
	var keys []types.MCPKey
	for rows.Next() {
		var key types.MCPKey
		if err := rows.Scan(&key.Endpoint, &key.HeaderName, &key.Hash); err != nil {
			return nil, fmt.Errorf("db: scan mcp key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate mcp keys: %w", err)
	}
	return keys, nil
}

// ReplaceMCPKeys 整体替换 MCP endpoint key 列表。
func (r *Repository) ReplaceMCPKeys(ctx context.Context, keys []types.MCPKey) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM gateway_keys WHERE kind = 'mcp'"); err != nil {
			return fmt.Errorf("db: clear mcp keys: %w", err)
		}
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_keys (id, kind, name, prefix, hash, models_json, enabled, created_at, endpoint, header_name) VALUES (?, 'mcp', '', '', ?, '[]', 1, '', ?, ?)`, key.Endpoint, key.Hash, key.Endpoint, key.HeaderName); err != nil {
				return fmt.Errorf("db: insert mcp key %q: %w", key.Endpoint, err)
			}
		}
		return nil
	})
}

// ==================== 管理员账号 ====================

// ListUsers 返回全部管理员账号。
func (r *Repository) ListUsers(ctx context.Context) ([]types.User, error) {
	rows, err := r.database.QueryContext(ctx, `SELECT username, password_hash, password_changed FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("db: list users: %w", err)
	}
	defer rows.Close()
	var users []types.User
	for rows.Next() {
		var user types.User
		if err := rows.Scan(&user.Username, &user.PasswordHash, &user.PasswordChanged); err != nil {
			return nil, fmt.Errorf("db: scan user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate users: %w", err)
	}
	return users, nil
}

// ReplaceUsers 整体替换管理员账号列表。
func (r *Repository) ReplaceUsers(ctx context.Context, users []types.User) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM users"); err != nil {
			return fmt.Errorf("db: clear users: %w", err)
		}
		for _, user := range users {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, password_changed) VALUES (?, ?, ?)`, user.Username, user.PasswordHash, boolInt(user.PasswordChanged)); err != nil {
				return fmt.Errorf("db: insert user %q: %w", user.Username, err)
			}
		}
		return nil
	})
}

// ==================== 工具函数 ====================

// unmarshalEach 依次把 JSON 文本列反序列化到对应目标。
func unmarshalEach(pairs ...any) error {
	for i := 0; i < len(pairs); i += 2 {
		raw, ok := pairs[i].(string)
		if !ok {
			return fmt.Errorf("db: expected json string at position %d", i)
		}
		if err := json.Unmarshal([]byte(raw), pairs[i+1]); err != nil {
			return fmt.Errorf("db: parse json column: %w", err)
		}
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
