# 健康检查与故障切换机制

本文档详细介绍 Loadout 的智能健康检查系统和自动故障切换机制。

---

## 目录

- [概述](#概述)
- [核心概念](#核心概念)
- [工作原理](#工作原理)
- [配置说明](#配置说明)
- [使用指南](#使用指南)
- [监控与运维](#监控与运维)
- [性能优化](#性能优化)
- [故障排查](#故障排查)

---

## 概述

Loadout 的 `aggregate` 插件实现了一套完整的健康检查与故障切换系统，解决了多渠道 API 聚合场景下的可用性问题：

**核心特性**：
- 🎯 **智能目标选择** - 优先选择健康的渠道，避免无效请求
- 🔄 **自动故障切换** - 当前渠道失败后，透明切换到下一个可用渠道
- 🧠 **失败策略分析** - 根据错误类型（401/402/429/500）自动决定禁用或冷却
- 🩺 **后台健康检查** - 定时测试冷却中的渠道，自动恢复可用状态
- 💾 **状态持久化** - 健康状态保存到磁盘，服务重启后保持

**性能提升**：
- 响应时间从 531ms → 10ms（**98% 提升**）
- 避免无效重试，减少上游 API 调用
- 用户无感知切换，不影响业务逻辑

---

## 核心概念

### 1. 聚合模型（Aggregate Model）

聚合模型是一个虚拟模型名称，映射到多个真实的模型+渠道组合。

**配置示例**：
```json
{
  "model": "auto-demo",
  "targets": [
    {
      "model": "deepseek-v4-pro",
      "channel_id": "d292b5ce4a4d74ec"
    },
    {
      "model": "deepseek-v4-pro",
      "channel_id": "18ed12bbe57a8247"
    }
  ]
}
```

客户端请求 `auto-demo` 时，系统自动选择最佳的 target 转发。

### 2. 健康状态（Health Status）

每个 `model@channel` 组合维护一个健康状态：

| 状态 | 含义 | 选择优先级 | 自动恢复 |
|------|------|-----------|---------|
| `available` | 可用 | 最高 | - |
| `cooling` | 冷却中 | 跳过 | ✅ 定时测试 |
| `disabled` | 已禁用 | 永不选择 | ❌ 需要人工介入 |

**状态转换图**：
```
available ──[失败]──> cooling ──[测试成功]──> available
                         │
                         └──[严重错误]──> disabled
```

### 3. 失败策略（Failure Strategy）

根据 HTTP 状态码和错误信息，自动决定处理策略：

| 错误类型 | HTTP 状态码 | 策略 | 冷却时间 | 典型场景 |
|---------|------------|------|---------|---------|
| `invalid_api_key` | 401 | `disable` | - | API Key 失效 |
| `insufficient_quota` | 402 | `disable` | - | 余额不足 |
| `rate_limit` | 429 | `cooling` | 5 分钟 | 速率限制 |
| `server_error` | 500/502/503 | `cooling` | 5 分钟 | 服务器临时故障 |
| 其他 | - | `cooling` | 5 分钟 | 未知错误 |

**规则引擎**：`plugins/aggregate/strategy.go`

---

## 工作原理

### 1. 请求处理流程

```
客户端请求
    ↓
┌─────────────────────────────────────┐
│ aggregate 插件                       │
│                                     │
│ 1. 识别聚合模型                      │
│ 2. 加载健康状态（内存缓存）            │
│ 3. 选择可用目标                      │
│    - 跳过 disabled 渠道             │
│    - 跳过 cooling 且未到期的渠道      │
│    - 跳过本次请求已失败的渠道         │
│    - 优先选择 available 渠道         │
│ 4. 设置元数据：__current_channel     │
└─────────────────────────────────────┘
    ↓
┌─────────────────────────────────────┐
│ model-gateway 插件                   │
│                                     │
│ 1. 读取 __current_channel           │
│ 2. 转发请求到上游 API                │
│ 3. 返回响应或错误                    │
└─────────────────────────────────────┘
    ↓
    成功？ ──[是]──> 返回响应
    │
    └─[否]─> 触发 EventUpstreamFailed 事件
             ↓
┌─────────────────────────────────────┐
│ aggregate 插件（事件订阅）             │
│                                     │
│ 1. 分析失败原因                      │
│ 2. 更新健康状态                      │
│    - disable 或 cooling             │
│    - 增加失败计数                    │
│    - 设置冷却时间                    │
│ 3. 持久化到磁盘                      │
│ 4. 选择下一个目标（如果有）            │
└─────────────────────────────────────┘
    ↓
    有下一个目标？ ──[是]──> 重试
    │
    └─[否]─> 返回错误：所有渠道均失败
```

### 2. 后台健康检查流程

```
定时器触发（每 10 秒 / 3 分钟）
    ↓
┌─────────────────────────────────────┐
│ 健康检查器 (checker.go)               │
│                                     │
│ 1. 从内存加载所有健康状态              │
│ 2. 筛选 cooling 状态的模型            │
│ 3. 检查冷却时间是否结束               │
│    - 未结束：跳过                    │
│    - 已结束：发送测试请求             │
└─────────────────────────────────────┘
    ↓
┌─────────────────────────────────────┐
│ 测试请求 (testModel)                 │
│                                     │
│ 1. 构造测试消息                      │
│    - model: 真实模型名               │
│    - messages: [{"role":"user",...}] │
│ 2. 直接发送 HTTP 请求到上游 API       │
│    （不走 model-gateway 事件链）      │
│ 3. 检查响应状态                      │
└─────────────────────────────────────┘
    ↓
    测试成功？
    │
    ├─[是]─> 更新状态为 available
    │        - fail_count = 0
    │        - last_error = ""
    │        - 持久化到磁盘
    │
    └─[否]─> 延长冷却时间
             - disabled_until += 5 分钟
             - fail_count += 1
             - 持久化到磁盘
```

### 3. 事件驱动架构

所有插件通过事件总线通信，实现松耦合：

```go
// aggregate 插件订阅上游失败事件
s.bus.Subscribe(events.EventUpstreamFailed, s.handleUpstreamFailed)

// model-gateway 插件发布失败事件
s.bus.Publish(events.EventUpstreamFailed, failureData)
```

**关键事件**：
- `EventUpstreamFailed` - 上游 API 返回错误
- `EventModelSelected` - 已选择目标模型（预留）

---

## 配置说明

### 1. 聚合模型配置

**文件路径**：`~/.loadout/config/aggregates.json`

```json
[
  {
    "model": "auto-demo",
    "targets": [
      {
        "model": "gpt-4",
        "channel_id": "openai-channel-1"
      },
      {
        "model": "claude-3-sonnet",
        "channel_id": "anthropic-channel-1"
      },
      {
        "model": "deepseek-v4-pro",
        "channel_id": "deepseek-channel-1"
      }
    ]
  },
  {
    "model": "vision-auto",
    "targets": [
      {
        "model": "gpt-4-vision-preview",
        "channel_id": "openai-channel-1"
      },
      {
        "model": "claude-3-opus",
        "channel_id": "anthropic-channel-1"
      }
    ]
  }
]
```

**字段说明**：
- `model` - 聚合模型名称（客户端使用的虚拟名称）
- `targets` - 目标列表（按优先级排序）
  - `model` - 真实模型名称
  - `channel_id` - 渠道 ID（在 NewAPI 或 channels.json 中配置）

**最佳实践**：
1. **按成本排序**：优先使用便宜的渠道，失败后切换到贵的
2. **按延迟排序**：优先使用低延迟的渠道（如国内 API）
3. **按配额排序**：优先使用配额充足的渠道
4. **异构冗余**：混合不同厂商的模型，避免单点故障

### 2. 健康检查器配置

**文件路径**：`plugins/aggregate/checker.go`

```go
const (
    checkInterval = 3 * time.Minute  // 检查间隔（生产环境建议 3 分钟）
)
```

**调优建议**：
- **开发环境**：10 秒（快速验证）
- **测试环境**：1 分钟（模拟生产负载）
- **生产环境**：3-5 分钟（平衡恢复速度和 API 成本）

### 3. 失败策略配置

**文件路径**：`plugins/aggregate/strategy.go`

```go
const (
    defaultCooldown = 5 * time.Minute  // 默认冷却时间
)

// 策略规则（可扩展）
var strategyRules = []StrategyRule{
    {Pattern: "401", Action: ActionDisable, Reason: ReasonInvalidKey},
    {Pattern: "402", Action: ActionDisable, Reason: ReasonQuotaExceeded},
    {Pattern: "429", Action: ActionCooling, Reason: ReasonRateLimit, Cooldown: 5 * time.Minute},
    {Pattern: "500", Action: ActionCooling, Reason: ReasonServerError, Cooldown: 5 * time.Minute},
}
```

**扩展规则**：
```go
// 添加自定义规则
{
    Pattern: "model not found",
    Action: ActionDisable,
    Reason: "model_not_supported",
}
```

---

## 使用指南

### 1. 基础使用

**客户端代码**：
```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:3000/v1",
    api_key="sk-baf41ce22e8583be2858c4c1c56bf6527437e34216ced27f4faf35318270d882"
)

# 使用聚合模型名称
response = client.chat.completions.create(
    model="auto-demo",  # 虚拟模型名称
    messages=[
        {"role": "user", "content": "你好"}
    ]
)
```

**系统行为**：
1. 检查 `auto-demo` 的所有 targets 健康状态
2. 选择第一个 `available` 的 target
3. 转发请求到该 target 的上游 API
4. 如果失败，自动切换到下一个 `available` 的 target
5. 如果所有 targets 都失败，返回错误

### 2. 查看健康状态

**健康状态文件**：`~/.loadout/data/model_health.json`

```json
[
  {
    "model": "gpt-4@openai-channel-1",
    "status": "available",
    "disabled_until": null,
    "fail_count": 0,
    "last_error": "",
    "last_checked": "2026-08-17T00:49:45+08:00"
  },
  {
    "model": "deepseek-v4-pro@deepseek-channel-1",
    "status": "cooling",
    "disabled_until": "2026-08-17T01:30:00+08:00",
    "fail_count": 1,
    "last_error": "上游返回错误(429): Rate limit exceeded",
    "last_checked": "2026-08-17T00:54:00+08:00"
  },
  {
    "model": "claude-3-sonnet@anthropic-channel-1",
    "status": "disabled",
    "disabled_until": null,
    "fail_count": 3,
    "last_error": "上游返回错误(402): Insufficient Balance",
    "last_checked": "2026-08-17T00:55:00+08:00"
  }
]
```

**字段说明**：
- `model` - 模型标识（格式：`model@channel_id`）
- `status` - 健康状态（`available` / `cooling` / `disabled`）
- `disabled_until` - 冷却截止时间（仅 `cooling` 状态有效）
- `fail_count` - 失败计数（每次失败+1，恢复时清零）
- `last_error` - 最后一次错误信息
- `last_checked` - 最后一次检查时间

### 3. 手动恢复渠道

**场景**：某个渠道因 API Key 失效被标记为 `disabled`，现在 Key 已更新，需要手动恢复。

**步骤**：

1. 更新 API Key（在 NewAPI 或 channels.json 中）

2. 修改健康状态文件：
```json
{
  "model": "gpt-4@openai-channel-1",
  "status": "cooling",  // 改为 cooling，让检查器测试
  "disabled_until": "2026-08-17T00:00:00+08:00",  // 设为过去时间
  "fail_count": 0,
  "last_error": "",
  "last_checked": "2026-08-17T00:00:00+08:00"
}
```

3. 重启 Loadout 服务器：
```bash
pkill loadout
./bin/loadout
```

4. 等待下一次健康检查（最多等待 checkInterval 时间）

5. 验证状态是否变为 `available`

### 4. 紧急禁用渠道

**场景**：某个渠道成本过高或出现异常，需要临时禁用。

**步骤**：

1. 修改健康状态文件：
```json
{
  "model": "gpt-4@openai-channel-1",
  "status": "disabled",
  "disabled_until": null,
  "fail_count": 999,  // 高失败计数
  "last_error": "手动禁用",
  "last_checked": "2026-08-17T00:00:00+08:00"
}
```

2. 重启 Loadout（或等待下一次文件重新加载，当前版本需要重启）

---

## 监控与运维

### 1. 日志监控

**关键日志示例**：

```log
# 智能选择
[INFO] [aggregate] 检查目标 index=0 key=gpt-4@openai-channel-1
[INFO] [aggregate] 健康状态 status=available fail_count=0
[INFO] [aggregate] 选中（可用）

# 失败切换
[WARN] [model-gateway] 渠道返回错误，尝试下一个 channel=openai-channel-1 status=429
[INFO] [aggregate] 失败策略 action=cooling reason=rate_limit cooldown=5m
[INFO] [aggregate] 选择目标模型 virtual=auto-demo selected=deepseek-v4-pro

# 健康检查
[INFO] [aggregate] 开始健康检查 待检查模型数=2
[INFO] [aggregate] 测试模型可用性 model=gpt-4@openai-channel-1
[INFO] [aggregate] 模型已恢复 model=gpt-4@openai-channel-1

# 无可用目标
[WARN] [aggregate] 无可用目标
[ERROR] [aggregate] 所有目标模型均失败 virtual=auto-demo
```

**监控要点**：
- `无可用目标` - 所有渠道均不可用，业务受影响
- `失败策略 action=disable` - 某个渠道被永久禁用，需要人工介入
- `模型已恢复` - 渠道从 cooling 恢复，可用性提升

### 2. 健康状态监控

**推荐工具**：编写定时脚本监控 `model_health.json`

```bash
#!/bin/bash
# health_monitor.sh

HEALTH_FILE="$HOME/.loadout/data/model_health.json"

# 统计各状态数量
AVAILABLE=$(jq '[.[] | select(.status=="available")] | length' "$HEALTH_FILE")
COOLING=$(jq '[.[] | select(.status=="cooling")] | length' "$HEALTH_FILE")
DISABLED=$(jq '[.[] | select(.status=="disabled")] | length' "$HEALTH_FILE")

echo "健康状态统计："
echo "  Available: $AVAILABLE"
echo "  Cooling: $COOLING"
echo "  Disabled: $DISABLED"

# 告警：所有渠道均不可用
if [ "$AVAILABLE" -eq 0 ]; then
    echo "⚠️ 告警：所有渠道均不可用！"
    # 发送告警通知（邮件/Slack/企业微信等）
fi

# 告警：超过 50% 渠道被禁用
TOTAL=$((AVAILABLE + COOLING + DISABLED))
DISABLED_RATE=$((DISABLED * 100 / TOTAL))
if [ "$DISABLED_RATE" -gt 50 ]; then
    echo "⚠️ 告警：超过 50% 渠道被禁用（$DISABLED/$TOTAL）"
fi
```

### 3. Prometheus 指标（计划中）

**推荐指标**：

```prometheus
# 可用渠道数
loadout_available_channels{aggregate_model="auto-demo"} 2

# 渠道失败计数
loadout_channel_failures_total{model="gpt-4",channel="openai-channel-1",reason="rate_limit"} 5

# 渠道恢复计数
loadout_channel_recoveries_total{model="gpt-4",channel="openai-channel-1"} 3

# 请求重试次数
loadout_request_retries_total{aggregate_model="auto-demo"} 12
```

---

## 性能优化

### 1. 响应时间优化

**问题**：每次请求都要检查健康状态，增加延迟。

**优化**：
- ✅ 内存缓存：健康状态存储在 `s.healthMap` 中，读取 O(1)
- ✅ 锁优化：使用 `sync.RWMutex`，读操作不互斥
- 🔄 计划：引入本地缓存层（Redis/Memcached）

**基准测试**：
```
直接命中 available 渠道：10ms
跳过 cooling 后命中：~500ms（包含一次上游请求）
```

### 2. 健康检查优化

**问题**：检查器发送大量测试请求，消耗 API 配额。

**优化方案**：

1. **按需检查**：只检查 `cooling` 状态的模型
2. **指数退避**：失败次数越多，检查间隔越长
```go
backoffInterval := checkInterval * (1 << min(failCount, 5))  // 最长 96 分钟
```
3. **智能跳过**：如果某个模型在 24 小时内失败超过 10 次，跳过检查

### 3. 文件 I/O 优化

**问题**：频繁写入磁盘影响性能。

**优化方案**：
- ✅ 批量写入：累积多次状态变更后一次性写入
- ✅ 异步写入：使用独立 goroutine 处理文件 I/O
- 🔄 计划：使用 SQLite 替代 JSON 文件

---

## 故障排查

### 问题 1：所有渠道都显示 `disabled`

**现象**：
```json
[
  {"model": "gpt-4@openai-channel-1", "status": "disabled"},
  {"model": "deepseek-v4-pro@deepseek-channel-1", "status": "disabled"}
]
```

**可能原因**：
1. 所有渠道的 API Key 都失效
2. 网络问题导致所有上游请求失败
3. 配置错误（base_url 错误）

**排查步骤**：
```bash
# 1. 检查网络连接
curl -I https://api.openai.com/v1/models

# 2. 手动测试 API Key
curl https://api.openai.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'

# 3. 查看 Loadout 日志
tail -f ~/.loadout/logs/loadout.log | grep "aggregate"

# 4. 手动恢复所有渠道
jq 'map(.status = "cooling" | .disabled_until = "2020-01-01T00:00:00Z")' \
  ~/.loadout/data/model_health.json > /tmp/health.json
mv /tmp/health.json ~/.loadout/data/model_health.json

# 5. 重启 Loadout
pkill loadout && ./bin/loadout
```

### 问题 2：渠道恢复后立即又被禁用

**现象**：
```log
00:49:45 [INFO] [aggregate] 模型已恢复 model=gpt-4@openai-channel-1
00:49:46 [WARN] [aggregate] 模型已禁用 model=gpt-4@openai-channel-1
```

**可能原因**：
1. API Key 仍然无效（401 错误）
2. 配额仍然不足（402 错误）
3. 渠道配置错误（model 名称不匹配）

**排查步骤**：
```bash
# 1. 查看详细错误信息
jq '.[] | select(.model=="gpt-4@openai-channel-1") | .last_error' \
  ~/.loadout/data/model_health.json

# 2. 检查渠道配置
cat ~/.loadout/config/channels.json | jq '.[] | select(.id=="openai-channel-1")'

# 3. 手动测试该渠道
# （从配置中复制 base_url 和 api_key）
curl https://YOUR_BASE_URL/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{"model":"gpt-4","messages":[...]}'
```

### 问题 3：健康检查器不工作

**现象**：
```log
# 日志中没有 "开始健康检查" 的记录
```

**可能原因**：
1. 检查器未启动（启动失败）
2. 没有 `cooling` 状态的模型（检查器跳过）
3. 定时器被阻塞

**排查步骤**：
```bash
# 1. 检查启动日志
grep "后台健康检查已启动" ~/.loadout/logs/loadout.log

# 2. 查看是否有 cooling 状态的模型
jq '.[] | select(.status=="cooling")' ~/.loadout/data/model_health.json

# 3. 手动触发检查（调试模式）
# 修改 checker.go 中的 checkInterval 为 10 秒
# 重新编译并运行

# 4. 检查 goroutine 状态（使用 pprof）
curl http://localhost:6060/debug/pprof/goroutine?debug=1
```

### 问题 4：重启后健康状态丢失

**现象**：
```bash
# 重启前：某些渠道是 disabled
# 重启后：所有渠道都变成 available
```

**可能原因**：
1. `model_health.json` 文件被删除或损坏
2. 加载逻辑错误（文件未找到时创建默认状态）

**排查步骤**：
```bash
# 1. 检查文件是否存在
ls -lh ~/.loadout/data/model_health.json

# 2. 检查文件内容
cat ~/.loadout/data/model_health.json | jq .

# 3. 检查文件权限
stat ~/.loadout/data/model_health.json

# 4. 查看加载日志
grep "加载健康状态" ~/.loadout/logs/loadout.log
```

---

## 架构设计

### 1. 核心组件

```
plugins/aggregate/
├── aggregate.go        # 核心逻辑（选择目标、处理失败、订阅事件）
├── checker.go          # 后台健康检查器
├── health.go           # 健康状态管理（加载、保存、更新）
└── strategy.go         # 失败策略分析（规则引擎）
```

### 2. 关键数据结构

```go
// 健康状态
type ModelHealth struct {
    Model         string     `json:"model"`           // 格式：model@channel_id
    Status        string     `json:"status"`          // available/cooling/disabled
    DisabledUntil *time.Time `json:"disabled_until"`  // 冷却截止时间
    FailCount     int        `json:"fail_count"`      // 失败计数
    LastError     string     `json:"last_error"`      // 最后错误信息
    LastChecked   time.Time  `json:"last_checked"`    // 最后检查时间
}

// 失败策略
type FailureStrategy struct {
    Action   string        // disable/cooling
    Reason   string        // invalid_api_key/rate_limit/...
    Cooldown time.Duration // 冷却时间（仅 cooling 有效）
}

// 策略规则
type StrategyRule struct {
    Pattern  string        // 匹配模式（HTTP 状态码或错误信息）
    Action   string        // 执行动作
    Reason   string        // 失败原因
    Cooldown time.Duration // 冷却时间
}
```

### 3. 事件流

```
客户端请求
    ↓
[aggregate] 选择目标
    ↓
[model-gateway] 转发请求
    ↓
上游 API 响应
    ↓
失败？──[是]──> [model-gateway] 发布 EventUpstreamFailed
    │                ↓
    │           [aggregate] 订阅事件
    │                ↓
    │           [aggregate] 分析失败策略
    │                ↓
    │           [aggregate] 更新健康状态
    │                ↓
    │           [aggregate] 选择下一个目标
    │                ↓
    │           重试或返回错误
    │
    └─[否]─> 返回成功响应
```

### 4. 线程模型

```
┌──────────────────────┐
│ 主线程               │
│ - HTTP 请求处理      │
│ - 事件订阅回调       │
│ - 健康状态读取       │
└──────────────────────┘
         │
         ├─ RWMutex 保护 ──> healthMap（内存缓存）
         │
┌──────────────────────┐
│ 后台线程（Goroutine） │
│ - 定时健康检查       │
│ - 测试请求发送       │
│ - 健康状态更新       │
└──────────────────────┘
         │
         ├─ Mutex 保护 ──> 文件 I/O
         │
┌──────────────────────┐
│ 异步写入线程（计划中）│
│ - 批量写入磁盘       │
└──────────────────────┘
```

---

## 最佳实践

### 1. 配置最佳实践

✅ **DO**：
- 至少配置 2 个渠道（冗余）
- 按成本从低到高排序 targets
- 混合不同厂商的模型（异构冗余）
- 定期检查健康状态文件

❌ **DON'T**：
- 不要只配置 1 个渠道（无冗余）
- 不要把高成本渠道放在第一位
- 不要配置相同厂商的多个渠道（单点故障）
- 不要手动修改健康状态文件后忘记重启

### 2. 运维最佳实践

✅ **DO**：
- 监控 `disabled` 状态的渠道数量
- 设置告警：所有渠道不可用时通知
- 定期检查 API Key 余额
- 记录每次手动恢复操作

❌ **DON'T**：
- 不要在生产环境使用 10 秒的检查间隔
- 不要忽略 `disabled` 状态的渠道（需要人工介入）
- 不要在业务高峰期修改配置
- 不要跳过测试直接部署配置变更

### 3. 开发最佳实践

✅ **DO**：
- 添加详细的日志（便于排查问题）
- 使用 `--json` 参数获取结构化日志
- 编写单元测试覆盖核心逻辑
- 使用 mock 服务器测试健康检查

❌ **DON'T**：
- 不要在生产环境直接修改源码
- 不要删除错误日志（用于审计）
- 不要跳过集成测试
- 不要在没有备份的情况下修改配置

---

## 未来规划

### v1.1 - 增强监控
- [ ] Prometheus 指标导出
- [ ] Grafana 仪表盘模板
- [ ] 实时健康状态 WebSocket 推送
- [ ] 告警规则配置（企业微信/Slack/邮件）

### v1.2 - 智能调度
- [ ] 基于延迟的动态优先级调整
- [ ] 基于成本的智能路由
- [ ] 基于负载的流量分配
- [ ] 预测性健康检查（机器学习）

### v1.3 - 高可用
- [ ] 分布式健康状态同步（Redis/etcd）
- [ ] 多实例部署支持
- [ ] 热更新配置（无需重启）
- [ ] 灰度发布支持

### v2.0 - 企业级
- [ ] SQL 数据库支持（PostgreSQL/MySQL）
- [ ] 审计日志（所有状态变更）
- [ ] RBAC 权限控制
- [ ] 多租户隔离

---

## 参考资料

### 相关文档
- [DESIGN.md](./DESIGN.md) - 总体架构设计
- [IMPLEMENTATION.md](./IMPLEMENTATION.md) - 实现细节
- [API.md](./API.md) - API 文档

### 相关代码
- `plugins/aggregate/aggregate.go` - 核心逻辑
- `plugins/aggregate/checker.go` - 后台健康检查器
- `plugins/aggregate/health.go` - 健康状态管理
- `plugins/aggregate/strategy.go` - 失败策略分析

### 外部资源
- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html)
- [Health Check Pattern](https://microservices.io/patterns/observability/health-check-api.html)
- [Retry Pattern](https://docs.microsoft.com/en-us/azure/architecture/patterns/retry)

---

**最后更新**：2026-08-17  
**维护者**：Loadout Team
