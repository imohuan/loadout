package adminapi

import (
	"context"
	"errors"

	"loadout/core/store"
	"loadout/plugins/types"
)

// 管理后台配置统一数据访问层：SQLite 优先，JSON fallback。
// 所有 handler 通过这里的读/写方法访问配置，避免散落的 readSlice/st.Write。

// ==================== 能力路由 ====================

func (s *Service) readCapabilityRoutes(ctx context.Context) ([]types.CapabilityRoute, error) {
	if s.routing != nil {
		routes, err := s.routing.ListCapabilityRoutes(ctx)
		if err == nil {
			if routes == nil {
				routes = []types.CapabilityRoute{}
			}
			return routes, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读能力路由失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.CapabilityRoute](s.st, types.FileCapabilityRoutes)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.CapabilityRoute{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeCapabilityRoutes(ctx context.Context, routes []types.CapabilityRoute) error {
	if s.routing != nil {
		if err := s.routing.ReplaceCapabilityRoutes(ctx, routes); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写能力路由到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileCapabilityRoutes, routes)
}

// ==================== MCP 服务器 ====================

func (s *Service) readMCPServers(ctx context.Context) ([]types.MCPServer, error) {
	if s.routing != nil {
		servers, err := s.routing.ListMCPServers(ctx)
		if err == nil {
			if servers == nil {
				servers = []types.MCPServer{}
			}
			return servers, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读 MCP 服务器失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.MCPServer](s.st, types.FileMCPServers)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.MCPServer{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeMCPServers(ctx context.Context, servers []types.MCPServer) error {
	if s.routing != nil {
		if err := s.routing.ReplaceMCPServers(ctx, servers); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写 MCP 服务器到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileMCPServers, servers)
}

// ==================== 分组 ====================

func (s *Service) readGroups(ctx context.Context) ([]types.Group, error) {
	if s.routing != nil {
		groups, err := s.routing.ListGroups(ctx)
		if err == nil {
			if groups == nil {
				groups = []types.Group{}
			}
			return groups, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读分组失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.Group](s.st, types.FileGroups)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.Group{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeGroups(ctx context.Context, groups []types.Group) error {
	if s.routing != nil {
		if err := s.routing.ReplaceGroups(ctx, groups); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写分组到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileGroups, groups)
}

// ==================== 工具开关 ====================

func (s *Service) readToolStates(ctx context.Context) ([]types.ToolState, error) {
	if s.routing != nil {
		states, err := s.routing.ListToolStates(ctx)
		if err == nil {
			if states == nil {
				states = []types.ToolState{}
			}
			return states, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读工具开关失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.ToolState](s.st, types.FileToolsState)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.ToolState{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeToolStates(ctx context.Context, states []types.ToolState) error {
	if s.routing != nil {
		if err := s.routing.ReplaceToolStates(ctx, states); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写工具开关到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileToolsState, states)
}

// ==================== 运行时设置 ====================

func (s *Service) readSettings(ctx context.Context) (types.Settings, error) {
	if s.routing != nil {
		settings, err := s.routing.GetSettings(ctx)
		if err == nil {
			return settings, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读设置失败，回退 JSON", "err", err)
	}
	var settings types.Settings
	if err := s.st.Read(types.FileSettings, &settings); err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return types.Settings{}, nil
		}
		return types.Settings{}, err
	}
	return settings, nil
}

func (s *Service) writeSettings(ctx context.Context, settings types.Settings) error {
	if s.routing != nil {
		if err := s.routing.PutSettings(ctx, settings); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写设置到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileSettings, settings)
}

// ==================== MCP endpoint key ====================

func (s *Service) readMCPKeys(ctx context.Context) ([]types.MCPKey, error) {
	if s.routing != nil {
		keys, err := s.routing.ListMCPKeys(ctx)
		if err == nil {
			if keys == nil {
				keys = []types.MCPKey{}
			}
			return keys, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读 MCP key 失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.MCPKey](s.st, types.FileMCPKeys)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return []types.MCPKey{}, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeMCPKeys(ctx context.Context, keys []types.MCPKey) error {
	if s.routing != nil {
		if err := s.routing.ReplaceMCPKeys(ctx, keys); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写 MCP key 到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileMCPKeys, keys)
}

// ==================== 技能清单（admin-api 直读场景） ====================

func (s *Service) readSkillsList(ctx context.Context) ([]types.Skill, error) {
	if s.routing != nil {
		skills, err := s.routing.ListSkills(ctx)
		if err == nil {
			return skills, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读技能清单失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.Skill](s.st, types.FileSkills)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writeSkillsList(ctx context.Context, skills []types.Skill) error {
	if s.routing != nil {
		if err := s.routing.ReplaceSkills(ctx, skills); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写技能清单到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FileSkills, skills)
}

// ==================== 技能预设 ====================

func (s *Service) readPresetsList(ctx context.Context) ([]types.Preset, error) {
	if s.routing != nil {
		presets, err := s.routing.ListPresets(ctx)
		if err == nil {
			return presets, nil
		}
		s.lg.Warn("admin-api: 从 SQLite 读预设失败，回退 JSON", "err", err)
	}
	items, err := readSlice[types.Preset](s.st, types.FilePresets)
	if err != nil {
		if errors.Is(err, store.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return items, nil
}

func (s *Service) writePresetsList(ctx context.Context, presets []types.Preset) error {
	if s.routing != nil {
		if err := s.routing.ReplacePresets(ctx, presets); err == nil {
			return nil
		} else {
			s.lg.Warn("admin-api: 写预设到 SQLite 失败，回退 JSON", "err", err)
		}
	}
	return s.st.Write(types.FilePresets, presets)
}
