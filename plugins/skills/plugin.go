// Package skills 实现 Loadout 的技能仓库与预设切换插件：
// 维护 ~/.loadout/skills 的清单（skills.json）、技能预设（presets.json）、
// 当前生效预设（settings.json），并按预设把仓库技能链接到 ~/.agents/skills。
package skills

import (
	"database/sql"
	"log/slog"

	"loadout/core/config"
	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
)

// skillPlugin 技能仓库插件的实现：在 Apply 中组装 Service 并注册为 "skills"。
type skillPlugin struct{}

// New 创建 skills 插件（符合插件约定：导出 func New() plugin.Plugin）。
func New() plugin.Plugin {
	return &skillPlugin{}
}

// Manifest 声明插件元数据：名称为 "skills"，依赖 store/logger/db，提供 "skills" 服务。
func (p *skillPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:    "skills",
		Version: "0.1.0",
		Inject:  []string{"store", "logger", "db"},
		Provide: []string{"skills"},
	}
}

// Apply 组装 Service：从容器取 store/logger/db，使用默认目录创建服务并注册；
// 按 config 开关启动技能监听（递归监听 / 定时全量扫描可单独开启），
// 监听器随插件卸载通过 ctx.Effect 停止。
func (p *skillPlugin) Apply(ctx plugin.Context) error {
	st := ctx.Get("store").(*store.Store)
	lg := ctx.Get("logger").(*slog.Logger)
	database := ctx.Get("db").(*sql.DB)
	svc := NewService(st, lg, config.SkillsDir, config.ResolveAgentSkillsDir())
	if database != nil {
		if repo, err := db.NewRepository(database); err == nil {
			svc.SetRepository(repo)
		} else {
			lg.Warn("skills: 创建 SQLite 仓储失败，继续使用 JSON 存储", "err", err)
		}
	}
	ctx.Set("skills", svc)

	if config.SkillWatchRecursive || config.SkillWatchPolling {
		w := NewWatcher(svc, config.SkillWatchRecursive, config.SkillWatchPolling,
			config.SkillWatchDebounce, config.SkillWatchPollInterval)
		if err := w.Start(); err != nil {
			lg.Warn("skills: 监听启动失败，自动同步不可用", "err", err)
		} else {
			ctx.Effect(w.Stop)
		}
	}
	return nil
}
