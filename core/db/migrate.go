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
`}, {
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
`}, {
	version: 26,
	name:    "process-history",
	sql: `
-- procreg 统一命令执行器的历史记录持久化：进程结束后写本表，后端重启不丢。
-- 之前历史仅存内存（上限 50 条且重启清空），导致 UI 进程历史只有几行。
-- log_json：进程完整日志行（JSON 数组字符串），UI 展开历史时展示。
CREATE TABLE process_history (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  proc_id TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  cmd TEXT NOT NULL DEFAULT '',
  pid INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'done',
  started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL,
  exit_code INTEGER,
  mem_bytes INTEGER NOT NULL DEFAULT 0,
  log_json TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX idx_process_history_proc_ended ON process_history(proc_id, ended_at);
CREATE INDEX idx_process_history_ended_at ON process_history(ended_at DESC);
`,
}, {
	version: 27,
	name:    "settings-use-global-cmd",
	sql: `
-- 运行时设置缺 use_global_cmd 列：布尔开关被 PutSettings 静默丢弃，
-- GetSettings 永远回读 false，导致前端 watch 回写触发重复保存。
ALTER TABLE settings ADD COLUMN use_global_cmd INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 28,
	name:    "capability-routes-injections",
	sql: `
-- message_inject 能力插件：往请求 messages 注入自定义内容（夹装子内部分，与 via_options/replacements/field_rules 同模式）。
ALTER TABLE capability_routes ADD COLUMN injections_json TEXT NOT NULL DEFAULT '[]';
`,
}, {
	version: 29,
	name:    "mcp-servers-builtin",
	sql: `
-- 内置端点注册的自连 MCP server（如多模态 /mcp/multimodal）打内置标记，前端显示「内置」标签。
ALTER TABLE mcp_servers ADD COLUMN builtin INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 30,
	name:    "channel-models-context",
	sql: `
-- /v1/models 输出需要每模型上下文：渠道探测 /v1/models 时读到的 context_length
-- 落本列，HandleModels 据此给每个模型带 context_length（0 = 未探测到，输出时不写该字段）。
ALTER TABLE channel_models ADD COLUMN context INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 31,
	name:    "settings-request-log-retention",
	sql: `
-- 日志保留策略（转发日志页「日志大小」按钮旁的设置项）：
--   request_log_max_age_days 只保留最近多少天的完整请求日志（0 = 不限）；
--   request_log_max_size_mb  完整请求日志库最大占用 MB（0 = 不限），超限按时间从旧到新删（FIFO）。
-- 两列都归 0 时行为与改造前一致：日志无限增长，只能手动清空。
ALTER TABLE settings ADD COLUMN request_log_max_age_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE settings ADD COLUMN request_log_max_size_mb INTEGER NOT NULL DEFAULT 0;
`,
}, {
	version: 32,
	name:    "aggregate-model-config",
	sql: `
-- 聚合（虚拟）模型的「模型配置」：虚拟模型对外在 /v1/models 那一行的属性
-- （上下文长度、最大输出、视觉/推理/工具调用能力）。
-- 存 JSON（{context_length, max_output_tokens, capabilities[]}），NULL = 未配置：
-- 输出只带 id/object，与改造前一致。
ALTER TABLE aggregates ADD COLUMN config_json TEXT;
`,
}, {
	version: 33,
	name:    "smart-tool-descriptions",
	sql: `
-- $smart 端点入口工具（status/get/invoke）的描述覆盖：用户可在管理后台改默认描述。
-- 只存覆盖条目，没记录的工具继续用硬编码默认描述。
CREATE TABLE smart_tool_desc (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);
`,
}, {
	version: 34,
	name:    "route-stats-archive",
	sql: `
-- 清空转发日志前的统计归档：Clear 先把现有 route_requests 按"本地日 + 最终模型"
-- 聚合成每日桶存进本表（JSON），再真实 DELETE 日志。/api/stats/models 读取本表
-- 与现场日志合并，保证清空后概览数据不归零。
CREATE TABLE route_stats_archive (
  id TEXT PRIMARY KEY,
  archived_at TEXT NOT NULL,
  snapshot_json TEXT NOT NULL
);
`,
}, {
	version: 35,
	name:    "failure-rules",
	sql: `
-- 失败规则引擎：规则表 + AI 判定日志表 + settings 加 AI 兜底模型列。
-- 规则 = 作用域(provider_base_url/model) + 匹配条件(any/all) + 动作
-- (verdict + 恢复策略)。引擎挂在 model-health.RecordFailure 内部，统一
-- 聚合/非聚合两条失败路径的禁用/冷却裁决（替代双轨硬编码分类）。
-- 时间限定的禁用写 status='cooling' + disabled_until（复用 CheckNow 过期
-- 恢复语义）；status='disabled' 仅永久禁用（recover=never）。
CREATE TABLE failure_rules (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  source TEXT NOT NULL DEFAULT 'manual',
  confirmed INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 100,
  provider_base_url TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  match_json TEXT NOT NULL,
  action_json TEXT NOT NULL,
  hit_count INTEGER NOT NULL DEFAULT 0,
  last_hit_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_failure_rules_priority ON failure_rules(priority, enabled);
CREATE TABLE rule_decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  provider_base_url TEXT NOT NULL DEFAULT '',
  status_code INTEGER NOT NULL DEFAULT 0,
  error_excerpt TEXT NOT NULL DEFAULT '',
  matched_rule_id TEXT NOT NULL DEFAULT '',
  matched_rule_name TEXT NOT NULL DEFAULT '',
  ai_model TEXT NOT NULL DEFAULT '',
  ai_raw TEXT NOT NULL DEFAULT '',
  verdict TEXT NOT NULL DEFAULT '',
  next_action TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX idx_rule_decisions_created ON rule_decisions(created_at DESC);
ALTER TABLE settings ADD COLUMN rule_ai_model TEXT NOT NULL DEFAULT '';

-- 内置种子规则（仅迁移时写入一次；INSERT OR IGNORE 保证与既有数据不冲突。
-- 种子 id 固定，用户可编辑/删除，应用启动不做 upsert）。
INSERT OR IGNORE INTO failure_rules(id, name, enabled, source, confirmed, priority, match_json, action_json, created_at, updated_at) VALUES
('seed-001', '客户端取消请求（不记失败）', 1, 'manual', 1, 10,
 '{"any":[{"field":"message_text","op":"contains","value":"context canceled"}]}',
 '{"verdict":"ignore"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-002', '每日额度用尽（次日恢复）', 1, 'manual', 1, 20,
 '{"any":[{"field":"body_code","op":"eq","value":"14018"},{"field":"message_text","op":"contains","value":"额度已用尽"},{"field":"message_text","op":"contains","value":"quota exhausted"}]}',
 '{"verdict":"disable_key","recover":"daily","daily_reset_hour":12}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-003', '账户余额不足（禁用key）', 1, 'manual', 1, 30,
 '{"any":[{"field":"status_code","op":"eq","value":402}]}',
 '{"verdict":"disable_key","recover":"never"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-004', '无效API密钥（禁用key并连坐渠道）', 1, 'manual', 1, 40,
 '{"any":[{"field":"status_code","op":"eq","value":401},{"field":"message_text","op":"contains","value":"invalid api key"}]}',
 '{"verdict":"disable_key","recover":"never","switch_account":true}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-005', '限速（冷却2分钟，连续5次升级为次日恢复）', 1, 'manual', 1, 50,
 '{"all":[{"field":"status_code","op":"eq","value":429},{"field":"message_text","op":"not_contains","value":"额度"}]}',
 '{"verdict":"cooldown","cooldown_seconds":120,"recover":"fixed","fail_upgrade_count":5,"fail_upgrade_recover":"daily"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-006', '服务过载（冷却1分钟）', 1, 'manual', 1, 60,
 '{"any":[{"field":"status_code","op":"eq","value":503},{"field":"message_text","op":"contains","value":"overloaded"}]}',
 '{"verdict":"cooldown","cooldown_seconds":60,"recover":"fixed"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-007', '上下文超长（跳过该模型）', 1, 'manual', 1, 70,
 '{"any":[{"field":"message_text","op":"contains","value":"context length"},{"field":"message_text","op":"contains","value":"maximum context"}]}',
 '{"verdict":"switch_next"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-008', '网络超时（冷却30秒）', 1, 'manual', 1, 80,
 '{"any":[{"field":"message_text","op":"contains","value":"timeout"},{"field":"message_text","op":"contains","value":"connection reset"}]}',
 '{"verdict":"cooldown","cooldown_seconds":30,"recover":"fixed"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-009', '模型不存在（禁用该模型）', 1, 'manual', 1, 90,
 '{"all":[{"field":"status_code","op":"eq","value":404},{"field":"message_text","op":"contains","value":"model"}]}',
 '{"verdict":"disable_model","recover":"never"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-010', '客户端参数错误（忽略）', 1, 'manual', 1, 100,
 '{"any":[{"field":"status_code","op":"eq","value":400},{"field":"status_code","op":"eq","value":404},{"field":"status_code","op":"eq","value":405}]}',
 '{"verdict":"ignore"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-011', '连接失败（忽略）', 1, 'manual', 1, 110,
 '{"any":[{"field":"message_text","op":"contains","value":"no such host"},{"field":"message_text","op":"contains","value":"connection refused"},{"field":"message_text","op":"contains","value":"no route to host"},{"field":"message_text","op":"contains","value":"dial tcp"},{"field":"message_text","op":"contains","value":"lookup"}]}',
 '{"verdict":"ignore"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-012', 'EOF连接中断（忽略）', 1, 'manual', 1, 120,
'{"any":[{"field":"message_text","op":"contains","value":"eof"}]}',
'{"verdict":"ignore"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`,
}, {
	version: 36,
	name:    "rule-multi-platform",
	sql: `
-- 规则作用域升级：支持多平台与按框架（New API / One API 等通用框架）匹配。
-- channels.framework：渠道所属框架标签（newapi/one-api/…；空 = 自定义/未标注），
--   同框架平台共用「框架规则」；渠道编辑页维护。
ALTER TABLE channels ADD COLUMN framework TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_rules ADD COLUMN scope_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_rules ADD COLUMN provider_base_urls_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE failure_rules ADD COLUMN provider_framework TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 37,
	name:    "rule-decision-channel",
	sql: `
-- 判定日志补充 Key 维度：一次失败发生在「哪个平台的哪个 Key 的哪个模型」上，
-- 便于定位是某个账号额度耗尽还是整个平台故障。
-- 同时冗余存 key 名（渠道编辑改名后日志仍保留当时快照）。
ALTER TABLE rule_decisions ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
ALTER TABLE rule_decisions ADD COLUMN channel_name TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 38,
	name:    "state-rule-attribution",
	sql: `
-- 状态归属：记录「是哪条规则把这条 Key / 模型 判为不可用的」。
-- 模型状态页据此展示命中规则，并可一键跳转到规则页改那条规则，
-- 否则用户只看到「额度用尽」却不知道该去哪调阈值/恢复策略。
ALTER TABLE model_states ADD COLUMN last_rule_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_states ADD COLUMN last_rule_name TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_states ADD COLUMN last_rule_id TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_states ADD COLUMN last_rule_name TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 39,
	name:    "seed-content-blocked-and-model-missing",
	sql: `
-- 基于上游实测（2026-09-20 直连探测各账号）：错误语义不止「额度用尽」一种：
--   403 + code 11140「request illegal / 内容未通过安全审核」：账号正常、请求内容
--     被平台风控拦截，与 Key 健康无关。此前落进 no_rule_matched 默认 2 分钟冷却，
--     同一账号换模型继续报，白白把健康 Key 冷却掉。
--     → 规则：ignore（不记状态、不计失败），下一轮直接换下一个 Key/模型。
--   400 + code 11102「model [x] service info not found」：该平台没有这个模型。
--     此前被 seed-010（400/404/405 一律 ignore）盖住，导致每次请求都重新试一遍
--     这个「平台不存在的模型」（鞭尸）。
--     → 规则：disable_model + never（该 Key 上禁用此模型，需手动恢复；其他模型不受影响）。
--   另修 seed-010 与 seed-009 的优先级矛盾：seed-009（404+含 model → disable_model）
--     优先级 90 高于 seed-010（100），但上游 404 往往不带 "model" 一词，会落到
--     seed-010 被 ignore。这里把 404 从 seed-010 移除（它已有专门规则）。
-- 注意：仅对「数据库里不存在 seed-013/seed-014」的库插入（新装库由 v35 建表后
-- 在此一并插入；已存在同名规则的库不动，尊重用户修改）。
INSERT OR IGNORE INTO failure_rules(id, name, enabled, source, confirmed, priority, match_json, action_json, created_at, updated_at) VALUES
('seed-013', '内容未过安全审核（忽略，换下一个Key）', 1, 'manual', 1, 45,
 '{"all":[{"field":"status_code","op":"eq","value":403},{"field":"body_code","op":"eq","value":"11140"}]}',
 '{"verdict":"ignore"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
('seed-014', '平台不支持该模型（禁用该模型）', 1, 'manual', 1, 55,
 '{"all":[{"field":"body_code","op":"eq","value":"11102"}]}',
 '{"verdict":"disable_model","recover":"never"}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');

-- seed-010 移除 404：404 由 seed-009/seed-014 处理。仅当用户未改过该规则
-- （match_json 仍是 v35 原始值）时才收紧，避免覆盖用户自定义。
UPDATE failure_rules
SET match_json = '{"any":[{"field":"status_code","op":"eq","value":400},{"field":"status_code","op":"eq","value":405}]}',
    updated_at = '2026-01-01T00:00:00Z'
WHERE id = 'seed-010'
  AND match_json = '{"any":[{"field":"status_code","op":"eq","value":400},{"field":"status_code","op":"eq","value":404},{"field":"status_code","op":"eq","value":405}]}'
  AND enabled = 1
  AND priority = 100;
`,
}, {
	version: 40,
	name:    "rule-samples-and-author",
	sql: `
-- 样本库 + AI 规则作者会话（「回撤」能力）。
--
-- rule_samples：把历史失败沉淀成可复用的测试样本。来源两类：
--   real    = 从 rule_decisions 导入的真实线上失败；
--   builtin = 人工新增的构造样本（覆盖已知故障形态）。
-- fingerprint 做唯一键：同一「状态码+业务码+错误摘要」的失败只留一条，
-- 避免把 171 条同类 502 堆成 171 行样本。
-- expected_verdict + confirmed：人工标注的期望判定；只有 confirmed=1 才计入
-- 「不一致」统计（避免把随手填的预期当成标准答案）。
CREATE TABLE rule_samples (
  id TEXT PRIMARY KEY,
  provider_base_url TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  status_code INTEGER NOT NULL DEFAULT 0,
  body_code TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '',
  channel_name TEXT NOT NULL DEFAULT '',
  provider_framework TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'real',
  expected_verdict TEXT NOT NULL DEFAULT '',
  confirmed INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_rule_samples_fp ON rule_samples(fingerprint);
CREATE INDEX idx_rule_samples_source ON rule_samples(source, status_code);

-- rule_author_sessions：AI「多轮」生成规则的会话状态。
-- 一次 author 请求 = 一个会话：AI 出草稿 → 用样本自检 → 不一致就带原因再修订，
-- 最多 N 轮。每轮结果进 rounds_json（前端据此显示「第 k 轮 / 通过与否」），
-- 会话是异步跑的，前端按 session_id 轮询。
CREATE TABLE rule_author_sessions (
  id TEXT PRIMARY KEY,
  sample_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'running',
  rounds INTEGER NOT NULL DEFAULT 0,
  max_rounds INTEGER NOT NULL DEFAULT 3,
  rounds_json TEXT NOT NULL DEFAULT '[]',
  draft_rule_id TEXT NOT NULL DEFAULT '',
  ai_model TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_rule_author_created ON rule_author_sessions(created_at DESC);
`,
}, {
	version: 41,
	name:    "ai-draft-dedup-index",
	sql: `
-- AI 草稿去重的数据库级保证。
--
-- Store.CreateDraft 会先查「同模型 + 同 match + 同 action 的未确认草稿」是否已存在，
-- 存在就复用。但「先查后插」在并发下有 TOCTOU 窗口：两个 goroutine 同时查不到、
-- 同时插入，就会留下两条内容相同的草稿。SQLite 单连接让这个窗口很小，但
-- 用唯一索引把它彻底关掉更稳妥（插入冲突时由调用方忽略即可）。
--
-- 索引只约束 AI 未确认草稿（confirmed=0），已确认的正式规则不受影响
-- （用户完全可能有意保留两条相同规则）。
-- 建索引前先清理历史遗留的重复草稿（保留最早一条），否则建索引会失败。
DELETE FROM failure_rules
WHERE id IN (
  SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY model, match_json, action_json, provider_base_urls_json
      ORDER BY created_at ASC, rowid ASC
    ) AS rn
    FROM failure_rules WHERE source='ai' AND confirmed=0
  ) WHERE rn > 1
);
CREATE UNIQUE INDEX idx_ai_draft_dedup
  ON failure_rules(model, match_json, action_json, provider_base_urls_json)
  WHERE source='ai' AND confirmed=0;
`,
}, {
	version: 42,
	name:    "route-attempts-previous-attempt-index",
	sql: `
-- 清空转发日志慢到「页面卡死」的根因修复。
--
-- route_attempts.previous_attempt_id 是指向本表 id 的自引用外键
-- （ON DELETE SET NULL）。SQLite 要求子键列有索引，否则执行 DELETE 时要对每个
-- 被删的子行做一次全表扫描来找出需要置 NULL 的行。缺索引时清空 route_requests
-- 触发级联删除的代价是「删除行数 x 表总行数」的平方级：线上 10.5 万行实测
-- 几十分钟到小时级，而整个库只开一个连接（db.Open 的 SetMaxOpenConns(1)），
-- 这条慢删除把唯一连接占满，页面其他请求全部排队，看起来就是点一下直接卡死。
-- 加上索引后同一操作降到秒级。
--
-- 列本身当前无人写入（代码里只有测试构造 previous_attempt_id），保留外键语义、
-- 只补索引，是收益最大且改动最小的一步。
CREATE INDEX IF NOT EXISTS idx_route_attempts_previous_attempt
  ON route_attempts(previous_attempt_id);

-- 顺手清理历史孤儿 attempt：父请求已被删、子行却留下来的行。它们会一直拖慢
-- 下一次清空（表越大，平方级越贵），也不属于任何请求，没有任何展示价值。
-- 用 NOT EXISTS 走 route_requests 主键，逐行判断，不依赖外键开关（PRAGMA
-- foreign_keys 在这个迁移事务里是 OFF，级联不会替我们兜底）。
DELETE FROM route_attempts
WHERE NOT EXISTS (
  SELECT 1 FROM route_requests r WHERE r.request_id = route_attempts.request_id
);
`,
}, {
	version: 43,
	name:    "author-stream-and-more-rounds",
	sql: `
-- AI 生成规则改为流式输出 + 20 轮。
--
-- stream_tail：当前轮流式输出的尾部预览（Go 侧只留最后 N 个字符），
--   前端轮询会话时展示成「打字机」效果，让用户确认 AI 还在动、没卡死。
--   单独一列而不是塞进 rounds_json：流式每几个字符就要刷一次，是高频小写入，
--   与轮次结构（低频整体覆盖）分开存互不干扰。
ALTER TABLE rule_author_sessions ADD COLUMN stream_tail TEXT NOT NULL DEFAULT '';
-- max_rounds 默认 3 → 20（复杂故障需要更多修订空间；存量会话保留原值）。
-- SQLite 无 ALTER COLUMN DEFAULT，重建表；会话是临时数据，代价可忽略。
CREATE TABLE rule_author_sessions_new (
  id TEXT PRIMARY KEY,
  sample_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'running',
  rounds INTEGER NOT NULL DEFAULT 0,
  max_rounds INTEGER NOT NULL DEFAULT 20,
  rounds_json TEXT NOT NULL DEFAULT '[]',
  stream_tail TEXT NOT NULL DEFAULT '',
  draft_rule_id TEXT NOT NULL DEFAULT '',
  ai_model TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO rule_author_sessions_new (id, sample_id, status, rounds, max_rounds, rounds_json, stream_tail, draft_rule_id, ai_model, error, created_at, updated_at)
  SELECT id, sample_id, status, rounds, max_rounds, rounds_json, '', draft_rule_id, ai_model, error, created_at, updated_at FROM rule_author_sessions;
DROP TABLE rule_author_sessions;
ALTER TABLE rule_author_sessions_new RENAME TO rule_author_sessions;
CREATE INDEX idx_rule_author_created ON rule_author_sessions(created_at DESC);
`,
}, {
	version: 44,
	name:    "author-stream-kind",
	sql: `
-- 流式预览的类型标签：reasoning=模型在思考 / content=正式输出。
--
-- 实测推理模型把「思考」放在 delta.reasoning_content、正文放在 delta.content，
-- 是分开的两路流。前端要能看出「此刻是在想、还是已经在写结果」，
-- 所以把当前这段增量的类型跟着尾部预览一起存。
ALTER TABLE rule_author_sessions ADD COLUMN stream_kind TEXT NOT NULL DEFAULT '';
`,
}, {
	version: 45,
	name:    "rule-rate-limit-with-reset-time",
	sql: `
-- 限速文案带「重置时刻」的规则（用户实测：腾讯 copilot code 6004 明确给出
-- 「将在 2026-09-23 15:48:27 UTC+8 重置」，旧默认规则却只冷却 2 分钟）。
-- 新规则开启「提取恢复时间」，冷却到上游自己说的那个点。优先级 49 比通用
-- 限速（50）更高。仅对不存在的库插入（尊重用户已修改的同名规则）。
INSERT OR IGNORE INTO failure_rules(id, name, enabled, source, confirmed, priority, match_json, action_json, created_at, updated_at) VALUES
('seed-015', '限速带重置时间（按文案时刻恢复）', 1, 'manual', 1, 49,
 '{"all":[{"field":"status_code","op":"eq","value":429},{"field":"message_text","op":"regex","value":"20\d{2}[-/]\d{2}[-/]\d{2}[ T]\d{1,2}:\d{2}(:\d{2})?"}]}',
 '{"verdict":"cooldown","recover":"fixed","extract_recover_at":true}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`,
}, {
	version: 46,
	name:    "fix-seed015-regex-escape",
	sql: `
-- 修复 v45 写坏的 seed-015 match_json：JSON 里的反斜杠未转义（\d 应为 \\d），
-- 导致 json.Unmarshal 失败、规则列表 500。仅在坏值存在时替换，幂等。
UPDATE failure_rules
SET match_json = REPLACE(match_json, '\d', '\\d')
WHERE id='seed-015' AND match_json LIKE '%\d{2}[-/]%'
`,
}, {
	version: 47,
	name:    "seed015-capture-template",
	sql: `
-- seed-015 的动作从专用开关（extract_recover_at）迁到通用捕获模板
-- （recover_at_template=$0：正则整匹配=时间文本）。仅当仍是旧值时改写，幂等。
UPDATE failure_rules SET action_json = '{"verdict":"cooldown","recover":"fixed","recover_at_template":"$0"}' WHERE id='seed-015' AND action_json = '{"verdict":"cooldown","recover":"fixed","extract_recover_at":true}';
UPDATE failure_rules SET match_json = REPLACE(match_json, '20\\d{2}[-/]\\d{2}[-/]\\d{2}[ T]\\d{1,2}:\\d{2}(:\\d{2})?', '20\\d{2}[-/]\\d{2}[-/]\\d{2}[ T]\\d{1,2}:\\d{2}:\\d{2}') WHERE id='seed-015';
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
