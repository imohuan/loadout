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
}, {
	version: 6,
	name:    "channel-name",
	sql: `
ALTER TABLE channels ADD COLUMN channel_name TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 7,
	name:    "api-key-cipher",
	sql: `
ALTER TABLE gateway_keys ADD COLUMN api_key_cipher TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 8,
	name:    "aggregate-target-channel-level",
	sql: `
ALTER TABLE aggregate_targets RENAME TO aggregate_targets_old;
CREATE TABLE aggregate_targets (
  aggregate_id INTEGER NOT NULL REFERENCES aggregates(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  model TEXT NOT NULL,
  channel_id TEXT REFERENCES channels(id),
  channel_ids_json TEXT NOT NULL DEFAULT '[]',
  channel_base_url TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (aggregate_id, position)
);
INSERT INTO aggregate_targets (aggregate_id, position, model, channel_id, channel_ids_json, channel_base_url)
  SELECT aggregate_id, position, model, channel_id, '[]', '' FROM aggregate_targets_old;
DROP TABLE aggregate_targets_old;
`,
}, {
		version: 9,
		name:    "capability-routes-channel-level",
		sql: `
ALTER TABLE capability_routes ADD COLUMN channel_base_urls_json TEXT NOT NULL DEFAULT '[]';
`,
	}, {
		version: 10,
		name:    "volc-free-quota",
		sql: `
-- 火山引擎免费额度插件：每条配置对应一个渠道 Key（channel_id）的一对 AK/SK。
-- access_key 明文存（控制台查询方便），secret_key 用 AES-GCM 加密（与渠道 key 同级别保护）。
CREATE TABLE volc_quota_config (
  channel_id TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
  access_key TEXT NOT NULL DEFAULT '',
  secret_key_cipher TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_synced_at TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);

-- 每次刷新后的资源包视图：channel_id + model 维度记录该模型的 free 配额余量。
-- model 为归一化后的资源包 product 标识（用于 UI 显示），unit/amount 由 SDK 返回。
-- status: ok / exhausted / unknown
CREATE TABLE volc_quota_models (
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  product_name TEXT NOT NULL DEFAULT '',
  total_amount INTEGER NOT NULL DEFAULT 0,
  available_amount INTEGER NOT NULL DEFAULT 0,
  used_amount INTEGER NOT NULL DEFAULT 0,
  unit TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown',
  synced_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, model)
);

-- 模型请求结束后记录 (channel_id, model) 的使用次数，供 UI 与禁用匹配辅助使用。
-- 不影响路由决策，仅作历史统计。
CREATE TABLE volc_quota_usage (
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model TEXT NOT NULL,
  use_count INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, model)
);
CREATE INDEX idx_volc_quota_models_status ON volc_quota_models(status);
CREATE INDEX idx_volc_quota_usage_last_used_at ON volc_quota_usage(last_used_at DESC);
`,
	}, {
		version: 11,
		name:    "volc-quota-account-alignment",
		sql: `
-- 免费额度按火山账号（AK/SK）对齐，而非按渠道 Key：同一账号可有多个 Key 共享额度。
-- account_id = SHA256(access_key) 前 16 位（Go 代码计算，见 service.accountID）。
-- 旧快照/统计无 account_id 且无法在 SQL 内可靠反推指纹，直接清空重建——额度会在
-- 下一次刷新时按账号重新拉取，使用统计重新累计。
-- 1) 配置表加 account_id（用于归并同一账号的多个 Key 配置）。
ALTER TABLE volc_quota_config ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_volc_quota_config_account_id ON volc_quota_config(account_id);

-- 2) 额度快照表重建：主键 (account_id, model)。
CREATE TABLE volc_quota_models_new (
  account_id TEXT NOT NULL,
  model TEXT NOT NULL,
  product_name TEXT NOT NULL DEFAULT '',
  total_amount INTEGER NOT NULL DEFAULT 0,
  available_amount INTEGER NOT NULL DEFAULT 0,
  used_amount INTEGER NOT NULL DEFAULT 0,
  unit TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown',
  synced_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, model)
);
DROP TABLE volc_quota_models;
ALTER TABLE volc_quota_models_new RENAME TO volc_quota_models;
CREATE INDEX idx_volc_quota_models_status ON volc_quota_models(status);

-- 3) 使用统计表重建：主键 (account_id, model)。
CREATE TABLE volc_quota_usage_new (
  account_id TEXT NOT NULL,
  model TEXT NOT NULL,
  use_count INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, model)
);
DROP TABLE volc_quota_usage;
ALTER TABLE volc_quota_usage_new RENAME TO volc_quota_usage;
CREATE INDEX idx_volc_quota_usage_last_used_at ON volc_quota_usage(last_used_at DESC);
`,
	}, {
		version: 12,
		name:    "volc-quota-force-block",
		sql: `
-- 强制关停：volc_quota_config.force_block=1 时，即使 model_states 被手动恢复，
-- 请求也按 volc_quota_models.status='exhausted' 直接拦截（不依赖 model_states 冷却）。
ALTER TABLE volc_quota_config ADD COLUMN force_block INTEGER NOT NULL DEFAULT 0;
`,
	}, {
		version: 13,
		name:    "volc-quota-local-remaining",
		sql: `
-- 本地递减余额：不依赖 billing API（429 不可靠），每次请求成功后扣减 total_tokens。
-- local_remaining = 初始总额度 - 已用 token（本地递减）；initial_total = 首次刷新写入的总额。
-- 当 local_remaining <= 0 时拦截请求（force_block=1 生效），不需要等 billing API 确认。
ALTER TABLE volc_quota_models ADD COLUMN initial_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE volc_quota_models ADD COLUMN local_remaining INTEGER NOT NULL DEFAULT 0;
`,
	}, {
		version: 14,
		name:    "volc-quota-packages",
		sql: `
-- 资源包逐条明细：同 Product（如 ark_bd）下包含几十种不同模型配置
-- （ConfigurationCode 如 Doubao_Seed_2.1_pro_data_collaboration / DeepSeek_V4_flash...），
-- 聚合到 model 会丢失"哪个模型还有额度"的信息。此表按 InstanceNo 逐条保存，
-- 供 UI 像 main.go 输出那样展示每个资源包的 ConfigurationName / Status / 额度。
CREATE TABLE volc_quota_packages (
  account_id TEXT NOT NULL,
  instance_no TEXT NOT NULL,
  product TEXT NOT NULL DEFAULT '',
  product_name TEXT NOT NULL DEFAULT '',
  configuration_code TEXT NOT NULL DEFAULT '',
  configuration_name TEXT NOT NULL DEFAULT '',
  total_amount INTEGER NOT NULL DEFAULT 0,
  available_amount INTEGER NOT NULL DEFAULT 0,
  used_amount INTEGER NOT NULL DEFAULT 0,
  unit TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  effective_time TEXT NOT NULL DEFAULT '',
  expiry_time TEXT NOT NULL DEFAULT '',
  synced_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, instance_no)
);
CREATE INDEX idx_volc_quota_packages_product ON volc_quota_packages(account_id, product);
`,
	}, {
		version: 15,
		name:    "volc-quota-packages-local-remaining",
		sql: `
-- 资源包级本地扣减余额：扣减锚点从 volc_quota_models（Product 聚合名，匹配不上 API 模型名）
-- 改为 volc_quota_packages（configuration_code 提取名）。每个资源包行独立维护
-- initial_total（首次刷新总额）与 local_remaining（每次请求扣减），UI 逐条展示。
-- model 列：从 configuration_code 提取的模型名（去资源包类型后缀），扣减/拦截的匹配锚点。
ALTER TABLE volc_quota_packages ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE volc_quota_packages ADD COLUMN initial_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE volc_quota_packages ADD COLUMN local_remaining INTEGER NOT NULL DEFAULT 0;
`,
	}, {
		version: 16,
		name:    "drop-volc-quota-models",
		sql: `
-- 删除 volc_quota_models 聚合表（用户要求）：扣减/拦截/UI 都改走 volc_quota_packages
-- （逐条资源包账本 + 同步扣减初始逻辑），聚合视图没必要维护。DROP 而非保留空表。
DROP TABLE volc_quota_models;
`,
	}, {
		version: 17,
		name:    "route-attempts-channel-level",
		sql: `
-- 聚合目标三种粒度（单 Key / Key 多选 / 渠道级）落库：让请求日志里被跳过的
-- 候选 attempt 能完整还原"目标跨了哪几个 Key / 哪个 base_url 组"，前端据此
-- 渲染"@ 渠道名(Key1, Key2)"而非空 channel_id。
--
-- channel_id 已存在；保留向后兼容。新列：
--   channel_ids_json    TEXT NOT NULL DEFAULT '[]'  —— Key 多选（aggregate 目标 ChannelIDs）
--   channel_base_url    TEXT NOT NULL DEFAULT ''    —— 渠道级（aggregate 目标 ChannelBaseURL）
ALTER TABLE route_attempts ADD COLUMN channel_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE route_attempts ADD COLUMN channel_base_url TEXT NOT NULL DEFAULT '';
`,
	}, {
		version: 18,
		name:    "route-requests-final-channel-level",
		sql: `
-- 与 v17 对应：route_requests 的最终目标（Finish 阶段锁定）也要承载三种粒度。
-- 否则聚合目标 rejected 时 list 视图的「最终目标」列只能看到 final_channel_id（Key 多选场景为空），
-- 渲染不出 "@ 渠道名(Key1, Key2)"。
ALTER TABLE route_requests ADD COLUMN final_channel_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE route_requests ADD COLUMN final_channel_base_url TEXT NOT NULL DEFAULT '';
`,
	}, {
		version: 19,
		name:    "route-attempts-first-byte-at",
		sql: `
-- 流式 attempt 收到上游响应头的时刻（TTFB），配合 started_at 前端可算
-- "等待响应 Xs"，配合当前时间/ finished_at 可算 "输出中 Ys"。
-- 运行中由 model-gateway 写入；流结束的 success UPSERT 用 COALESCE 保留旧值。
ALTER TABLE route_attempts ADD COLUMN first_byte_at TEXT;
`,
	}, {
		version: 20,
		name:    "capability-routes-field-rules",
		sql: `
-- field_filter 能力插件的字段规则（嵌套 JSON 列，与 via_options/replacements 同模式）。
ALTER TABLE capability_routes ADD COLUMN field_rules_json TEXT NOT NULL DEFAULT '{}';
`,
	}, {
		version: 21,
		name:    "route-logs-channel-name-snapshot",
		sql: `
-- 渠道名称快照：写日志时把 channel_name（渠道名）落库。
-- Key 被删除后前端仍能显示「@渠道名(Unknown)」，否则只剩 channel_id 无从反查渠道名。
ALTER TABLE route_attempts ADD COLUMN channel_name TEXT NOT NULL DEFAULT '';
ALTER TABLE route_requests ADD COLUMN final_channel_name TEXT NOT NULL DEFAULT '';
`,
	}, {
		version: 22,
		name:    "route-logs-error-body",
		sql: `
-- 上游原始错误响应体（截断到 8KB）：与 error_message（解析后的 message 字段）
-- 互补。error_message 只保留一行摘要，前端 model-gateway 失败的「上游返回错误(N)」
-- 看不到具体厂商返回的 code/msg/extError 字段，定位 400/429/500 根因时只能翻
-- loadout.log。落库后 /api/route-logs 和 /api/route-logs/{id} 直接带出，前端折叠
-- 面板里展示完整 JSON；list 行也能从 route_requests.error_body 拿到最后一次渠道的
-- raw body。attempt 行单条存储便于「切换渠道」场景下逐个排查。
ALTER TABLE route_attempts ADD COLUMN error_body TEXT NOT NULL DEFAULT '';
ALTER TABLE route_requests ADD COLUMN error_body TEXT NOT NULL DEFAULT '';
`, }, {
		version: 23,
		name:    "route-attempts-step-no-text",
		sql: `
-- step_no 从 INTEGER 改为 TEXT：支持点分层级编号（"1" 主请求、"1.1" 视觉识别、"1.2" 续流）。
-- SQLite 不支持 ALTER COLUMN TYPE，重建表迁移。新表 = v1 建表列 + 之后所有加列：
--   v3  stream/prompt_tokens/completion_tokens/cached_tokens
--   v17 channel_ids_json/channel_base_url
--   v19 first_byte_at
--   v21 channel_name
--   v22 error_body
-- 外键：本迁移在 Migrate 的单个事务里执行，事务开始前已 PRAGMA foreign_keys=OFF
-- （见 Migrate 注释）。否则 DROP route_attempts 的隐式 DELETE 会对 route_attempts_new
-- 触发 ON DELETE SET NULL，把刚拷贝的 previous_attempt_id 清空。
CREATE TABLE route_attempts_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL REFERENCES route_requests(request_id) ON DELETE CASCADE,
  previous_attempt_id INTEGER REFERENCES route_attempts(id) ON DELETE SET NULL,
  step_no TEXT NOT NULL,
  action TEXT NOT NULL,
  model TEXT NOT NULL,
  channel_id TEXT,
  channel_ids_json TEXT NOT NULL DEFAULT '[]',
  channel_base_url TEXT NOT NULL DEFAULT '',
  channel_name TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT,
  first_byte_at TEXT,
  result TEXT NOT NULL,
  failure_class TEXT NOT NULL DEFAULT '',
  status_code INTEGER,
  error_message TEXT NOT NULL DEFAULT '',
  error_body TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER,
  stream INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  UNIQUE(request_id, step_no)
);
INSERT INTO route_attempts_new (id, request_id, previous_attempt_id, step_no, action, model, channel_id, channel_ids_json, channel_base_url, channel_name, started_at, finished_at, first_byte_at, result, failure_class, status_code, error_message, error_body, duration_ms, stream, prompt_tokens, completion_tokens, cached_tokens, metadata_json)
  SELECT id, request_id, previous_attempt_id, CAST(step_no AS TEXT), action, model, channel_id, channel_ids_json, channel_base_url, channel_name, started_at, finished_at, first_byte_at, result, failure_class, status_code, error_message, error_body, duration_ms, stream, prompt_tokens, completion_tokens, cached_tokens, metadata_json FROM route_attempts;
DROP TABLE route_attempts;
ALTER TABLE route_attempts_new RENAME TO route_attempts;
CREATE INDEX idx_route_attempts_request_step ON route_attempts(request_id, step_no);
CREATE INDEX idx_route_attempts_channel_started_at ON route_attempts(channel_id, started_at DESC);
CREATE INDEX idx_route_attempts_model_started_at ON route_attempts(model, started_at DESC);
CREATE INDEX idx_route_attempts_result_started_at ON route_attempts(result, started_at DESC);
`,
	}, {
		version: 24,
		name:    "route-requests-request-log-id",
		sql: `
-- request-log 插件的关联列：route_requests 行指向独立库 request-log.db 的
-- request_logs 表主键 UUID。UUID 由 request-log 插件在 proxy:before-attempt
-- （请求发出之前）生成并 UPDATE 本列；route-log 列表/详情带出，前端据此跳转。
-- 可空：未命中 request_log 能力路由的请求为 NULL。不加 UNIQUE（每行独立生成，
-- 天然唯一；SQLite 对 NULL 不做唯一性检查）。不加索引（无按此列查询需求）。
ALTER TABLE route_requests ADD COLUMN request_log_id TEXT;
`,
	}, {
		version: 25,
		name:    "route-attempts-request-log-id",
		sql: `
-- request-log 插件 per-attempt 关联列：route_attempts 行指向 request_logs 独立库
-- 主键 UUID。UUID 由 request-log 插件在每次 proxy:before-attempt 生成并暂存
-- pipe.Metadata[__request_log_attempt_id]，model-gateway 写 attempt 行时落本列。
-- 可空：未命中 request_log 能力路由的 attempt（含视觉子段）为 NULL。
ALTER TABLE route_attempts ADD COLUMN request_log_id TEXT;
`,
	}}

// Migrate applies all pending schema migrations and rejects an incompatible
// database instead of trying to infer a recovery path.
func Migrate(ctx context.Context, database *sql.DB) error {
	// SQLite 不支持 ALTER COLUMN TYPE，改列类型需重建表（v23 起 route_attempts）。
	// 重建过程在事务里 DROP 旧表，隐式 DELETE 会触发外键 ON DELETE 动作——例如
	// route_attempts_new.previous_attempt_id 引用被 DROP 的旧表，SET NULL 会把刚
	// 拷贝的数据清掉。PRAGMA foreign_keys 在事务内是 no-op，必须在事务外关闭、
	// 提交后再恢复。出错时调用方（db.Open）会关闭连接，无需在此兜底恢复。
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("db: disable foreign keys: %w", err)
	}

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
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("db: re-enable foreign keys: %w", err)
	}
	return nil
}

func migrationChecksum(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", sum[:])
}
