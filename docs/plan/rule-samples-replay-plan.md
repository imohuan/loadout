# 失败规则「样本回放 / AI 生成」计划

> 目标：补齐用户指出的三块缺口——(1) AI 兜底真正可用；(2) AI 生成规则的多轮机制；(3)「回撤」（拉历史失败 → 去重 → 批量回放测试 → 表格看通过/不通过 → AI 生成草稿待人工确认）。
> 参照物：\`D:\\Code\\Git\\web2api\` 的规则体系（samples + replay + author）。

## 背景：现状与差距（已核实）

当前失败链路（\`plugins/model-health/rules_integration.go:recordFailureRuled\`）：

\`\`\`
上游失败 → RecordFailure → catalogAllows 过滤 → isInternalRoutingError 过滤
        → rules.Evaluate（按 priority 升序匹配）
             ├─ 命中 → 规则动作
             └─ 未命中 → evaluateAIOnce（异步）→ 本次默认 cooldown
                          → AI 结果缓存 + spawnDraft 落草稿（confirmed=0）
        → executor.Execute 写状态 → recordDecisionLog 写判定日志
\`\`\`

实测数据（\`rule_decisions\` 594 条）：

| 指标 | 值 |
|---|---|
| 规则命中 | 423 |
| 未命中（落默认 cooldown） | 171 |
| AI 实际使用 | 0（\`rule_ai_model\` 为空） |

三个缺口：

1. **AI 兜底依赖手工配置**，未配则整条链路不存在；且 171 条未命中里 502/11133 这类本可被规则/AI 覆盖的全落默认冷却。
2. **AI 生成规则无「多轮」机制**：现在一次失败 = 一次判定 = 一条草稿，没有 web2api 的 \`author\`（生成 → 自查 → 修订，多轮，带进度）。
3. **「回撤」完全缺失**：没有样本库、没有批量回放、没有「通过/不通过 + 原因」表格、没有批量 AI 生成入口。

web2api 对应实现（已读源码）：

- 样本库：\`rule_samples\`（来源 = 系统自带 / 真实失败任务；带 \`expectedVerdict\` + \`confirmed\`）
- 批量回放：\`POST /api/samples/{platform}/replay\` → 返回 \`{results[], confirmedOk, confirmedTotal}\`
- AI 兜底测试：\`POST /api/samples/{platform}/ai-fallback\` + SSE 进度 \`/ai-progress\`（done/total）
- AI 生成规则：\`POST /api/rules/{platform}/author\` → 返回 \`sessionId\`，轮询会话查 \`rounds\`，产出进候选区待确认
- 前端过滤维度：匹配 / 未命中 / AI兜底 / 不一致（预期≠实际）

## 设计决策

- **样本来源**：直接用现有 \`rule_decisions\` 反推，不引入新概念。导入时按「状态码 + 业务码 + 错误摘要指纹」去重。
- **回放是纯函数**：对样本复用 \`Engine.VerifyRule\` 的匹配语义，不写任何状态、不调上游、不调 AI（除非显式开 AI 模式）。
- **AI 多轮**：\`author\` 生成草稿后，用同批样本自动回放自检；不一致则带着失败原因再让 AI 修订一轮，最多 N 轮（默认 3）。每轮结果落库，前端可见。
- **人工确认不可绕过**：AI 产出永远是 \`confirmed=0\` 草稿，必须人工确认才进匹配集。
- **只加不改**：现有 CRUD / 判定日志 / 默认规则 / 恢复默认全部保留，新增能力以新表 + 新接口 + 新 tab 形式加入。

## 任务拆解

### M1 后端：样本库表与接口
- [ ] M1.1 迁移 v40：建 \`rule_samples\` 表
      \`\`\`sql
      CREATE TABLE rule_samples (
        id TEXT PRIMARY KEY,
        provider_base_url TEXT NOT NULL DEFAULT '',
        model TEXT NOT NULL DEFAULT '',
        status_code INTEGER NOT NULL DEFAULT 0,
        body_code TEXT NOT NULL DEFAULT '',
        message TEXT NOT NULL DEFAULT '',
        evidence_json TEXT NOT NULL DEFAULT '{}',  -- 完整 Evidence 快照
        fingerprint TEXT NOT NULL,                  -- 去重键
        source TEXT NOT NULL DEFAULT 'real',        -- real | builtin
        expected_verdict TEXT NOT NULL DEFAULT '',  -- 人工标注的预期（空=未标注）
        confirmed INTEGER NOT NULL DEFAULT 0,       -- 预期是否已确认
        note TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
      );
      CREATE UNIQUE INDEX idx_rule_samples_fp ON rule_samples(fingerprint);
      \`\`\`
- [ ] M1.2 \`plugins/failure-rules/samples.go\`：Store 方法
      - \`ImportFromDecisions(ctx, limit)\`：从 \`rule_decisions\` 导入，按 fingerprint 去重（\`INSERT OR IGNORE\`）
      - \`ListSamples(ctx, filter)\` / \`UpdateSampleExpected(ctx, id, verdict, confirmed)\` / \`DeleteSample(ctx, id)\`
      - \`ListByIDs(ctx, ids)\`
- [ ] M1.3 \`Replay(ctx, ids, useAI)\`：对样本跑规则，返回每条的 \`{sampleId, ok, matchedRuleId, matchedRuleName, verdict, expected, reason}\` + 汇总 \`{okCount, total, confirmedOk, confirmedTotal, inconsistent}\`
      - **关键**：复用 \`Engine.VerifyRule\` 的匹配逻辑；不写状态、不调上游
- [ ] M1.4 测试：导入去重、回放命中/未命中/不一致三种结果

### M2 后端：AI 多轮生成规则（author）
- [ ] M2.1 \`plugins/failure-rules/author.go\`：\`Author(ctx, sample) (sessionId, err)\`
      - 组装「失败证据 + 现有规则摘要」提示词 → 调 AI 产出规则草稿
      - 用样本回放自检 → 不一致则带失败原因再修订，最多 3 轮
      - 每轮 \`{round, verdict, matched, ok, reason}\` 落 \`rule_author_sessions\`
- [ ] M2.2 迁移 v40 一并建 \`rule_author_sessions\`（session_id / sample_id / status / rounds_json / draft_rule_id / error）
- [ ] M2.3 接口：\`POST /api/rule-samples/author\`、\`GET /api/rule-samples/author/{sessionId}\`
- [ ] M2.4 测试：多轮收敛、达到上限仍不一致、AI 不可用时的降级

### M3 后端：HTTP 接口与契约
- [ ] M3.1 \`GET /api/rule-samples\`（筛选：source/verdict/model）
- [ ] M3.2 \`POST /api/rule-samples/import\`（导入历史失败）
- [ ] M3.3 \`POST /api/rule-samples/replay\`（批量回放，body: ids + useAI）
- [ ] M3.4 \`PATCH /api/rule-samples/{id}\`（标注预期/确认）、\`DELETE /api/rule-samples/{id}\`
- [ ] M3.5 contracts 接口扩展 + admin-api 路由注册（独立前缀，避免与 {id} 冲突）

### M4 前端：失败规则页新增「样本回放」tab
- [ ] M4.1 第三个 tab「样本回放」
- [ ] M4.2 工具栏按钮：导入历史失败 / 批量回放 / AI 兜底测试 / AI 生成规则
- [ ] M4.3 表格列：来源 / 状态码 / 业务码 / 错误摘要（tooltip）/ 匹配规则 / 判定 / 预期 / 结果 / 操作
- [ ] M4.4 结果态徽标：已匹配（绿）/ 未命中（琥珀）/ AI兜底（蓝）/ 不一致（红，标注原因）
- [ ] M4.5 过滤：匹配 / 未命中 / AI兜底 / 不一致 + 搜索
- [ ] M4.6 AI 生成进度列（轮次 + 状态），完成后草稿进规则列表的 AI 草稿区待确认

### M5 验证
- [ ] M5.1 \`go build ./...\` + 相关包测试
- [ ] M5.2 真实接口验证：导入 → 回放 → 标注 → AI 生成 → 人工确认
- [ ] M5.3 前端 \`vue-tsc\` + \`npm run build\`
- [ ] M5.4 浏览器实测（内置浏览器可用时）

## 完成标准

1. 能一键把历史失败导入样本库，重复导入不产生重复行
2. 能批量回放，得到一张带「通过/不通过 + 原因」的表格
3. 能点按钮让 AI 生成规则，看见轮次进度，生成物是需人工确认的草稿
4. AI 生成的规则经确认后能真正参与匹配（命中后写判定日志）
5. 原有能力（CRUD / 判定日志 / 恢复默认 / 平台筛选）全部不回归

## 风险

- **AI 多轮的成本与耗时**：每轮一次完整推理（实测 9~40s）。用样本数上限 + 并发去重 + 会话状态兜底，避免卡死请求。
- **回放必须无副作用**：严禁经 \`RecordFailure\` 路径（会写状态/禁 Key）。只走纯匹配求值。
- **并行任务冲突**：仓库有其他任务在改前端表格组件（AxTable），只动自己的文件。

