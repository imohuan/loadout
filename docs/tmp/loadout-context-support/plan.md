# Loadout: /v1/models 支持输出每模型上下文 (context_length)

## 目标
让 Loadout 的 `/v1/models` 与 `/v2/models` 给每个模型带上真实的 `context_length`，
避免 opencodex 等工具因读不到上下文而统一回落 128000。
上下文来源（按用户定的"稳"方案）：
1. 渠道上游 `/v1/models` 探测返回的 context（当前被丢弃）——主来源，自包含、不依赖 openrouter 缓存；
2. 探测没有时，从 openrouter 元数据缓存 `~/.unifyai/cache/openrouter-models.json` 匹配补（次要兜底，可选）。

## 现状（已核实）
- 路由注册：`plugins/model-gateway/plugin.go` — `/v1/models`→`HandleModels`，`/v2/models`→`HandleModelsV2`，`/v1/{path...}`→`HandleProxy`。
- `/v1/models` 聚合渠道模型：`service.go` `HandleModels`(242) / `HandleModelsV2`(313) → `collectChannelModels`(202) 输出 `{id, object}`，无 context。
- 探测：`plugins/admin-api/service.go` `fetchChannelModels`(507)/`probeChannelModels`(552) 只解析 id，丢弃 context。
- 存储（DB 主路径）：`core/db/repository.go` — `db.ChannelModel`(28) 无 context 列；表 `channel_models`(migrate.go:32) 无 context 列。
  DB 持久化点：`plugins/admin-api/routing.go` handleChannelCreateDB(133)、handleChannelUpdateDB(242)、handleChannelRefreshModelsDB(294)、handleChannelModelsReplaceDB(373)、以及 replace 空目录自动探测(377)。
- JSON 兜底路径（`s.routing==nil`，legacy）：`types.Channel`、`service.go` 600-850 各处；此路径非当前实例活动路径。
- openrouter 元数据缓存已可读：`plugins/unifyai/service.go` `metadataCachePath()` 读 `~/.unifyai/cache/openrouter-models.json`（OpenRouterMeta{id,context,output,...}）。

## 改动方案（DB 主路径；JSON legacy 仅保持不坏、不带 context）
1. **迁移** `core/db/migrate.go` 追加 version 30：`ALTER TABLE channel_models ADD COLUMN context INTEGER NOT NULL DEFAULT 0;`
2. **结构** `core/db/repository.go` `db.ChannelModel` 加 `Context int64`（json:"context"），SELECT/INSERT 补该列。
3. **探测** `plugins/admin-api/service.go`：新增 `fetchChannelModelDetails(...) ([]ProbedModel, error)` 解析 context（读 `context_length`/`max_model_len`/`context_window` 等，参考 unifyai + opencodex 认知字段）；保留原 `fetchChannelModels`（id-only，供 preview / JSON / test 用）。`ProbedModel{Model,Context}`。
4. **DB 持久化点** `routing.go` 各 DB 路径改用它并带 context 落库。
5. **输出** `plugins/model-gateway/service.go` `HandleModels`/`HandleModelsV2` 组装 data 时，从 `collectChannelModels` 带出每模型 context，map 上 `context_length`（>0 才写）。
6. `collectChannelModels` / `channelModelEntry` 增加 Context 透传；DB 路径的 `ListChannels`/模型读取已带 context。

## 验证
- `go build ./...`
- 写单测：探测解析 context、HandleModels 输出含 context_length、迁移后列存在。
- 手动：加一个 openrouter 渠道，`curl /v1/models` 看是否带 context_length。
