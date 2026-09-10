# 调查：「同步时获取数据」后数据预览里 OpenCodex 模型为 0，刷新元数据才有效

日期：2026-09-10
结论性质：链路已查清 + 修复已落地并端到端验证（两个仓库都已提交）

## 一、现象

UnifyAI 页面里，不点「更新元数据」直接进页面（走 `--list all` 同步取数）时，
「数据预览 → OpenCodex 模型」显示 0 个；点了「更新元数据」之后才正常。

## 二、根因（一句话）

**unifyai CLI 请求 OpenCodex 代理只等 3 秒，而代理冷启动要 10 秒以上。**
所以「本次进程第一次去问代理」必然超时 → CLI 报 `degraded: true` + 空模型列表 → 页面显示 0 个。
紧接着再点一次，代理已经热了（几十毫秒返回），于是看起来「点一下刷新就好了」。

原代码 `src/core/config-loader.mjs`：

```js
const timeoutId = setTimeout(() => controller.abort(), 3000); // 3秒超时
```

## 三、对照实验（决定性证据，本机真机实测）

把代理晾到冷状态（130~150 秒不打），然后用**同一份配置文件**分别跑旧代码和新代码：

| 版本 | 代理冷启动实测 | CLI 输出 |
|---|---|---|
| 旧代码（3s 超时） | 10.4s ~ 11.3s | `rawCount=0 count=0 degraded=true`（3.7s 就放弃了） |
| 新代码（20s 超时 + 重试一次） | 10.4s ~ 11.3s | `rawCount=39 count=30 degraded=false`（0.8s 返回） |

代理热了之后只要 44~81ms。所以问题**只在冷启动那一下**，正好对上「刚进页面是 0、点一下就有了」。

## 四、修复

### 1. unifyai CLI（`D:\Code\Git\unifyai`，commit defe894）—— 根治

文件 `src/core/config-loader.mjs`：

- 新增 `const PROXY_TIMEOUT_MS = 20000;`（原来是硬编码 3000）。
- `tryFetchFromProxy` 改成重试包装器（`MAX_ATTEMPTS = 2`），**只在「超时」时重试**
  （连接被拒绝 / DNS 解析失败重试也没意义，直接返回）。
- 原函数体拆成 `tryFetchFromProxyOnce`，所有 return 补 `timedOut` 字段。
- 超时提示文案改用 `PROXY_TIMEOUT_MS`，不再是写死的 "3s"。

### 2. loadout 后端（`plugins/unifyai/service.go`）—— 双保险

CLI 已经能自己扛住冷启动，后端再加一层退避重试，且**同时覆盖 `/all` 和 `/models` 两条路**
（之前只有 `/models` 有）：

- `queryModels` / `queryModelsOnce`：抽出公共查询逻辑，`queryModelsOnce` 返回 `retryable`
  （判定条件 `res.Degraded && len(res.Models) == 0`）。
- `ListAll(enableVision)` 同样带退避重试，内部拆出 `listAllOnce`。
- `queryModelsAttempts = 2`、`queryModelsRetryDelay = 500ms` 做成 `var` 便于测试缩短等待。
- `enrichFromOpenRouter`：代理拿不到上下文窗口时，用本地 OpenRouter 元数据缓存按
  「完整 id / 裸模型名」两级匹配补 `contextWindow`，让「同步取数」与「刷新元数据」结果一致。
- 新增 `OpenCodexModelsLive` + 路由 `GET /api/unifyai/opencodex-models-live`：原样透传 CLI 输出，
  用于诊断 CLI 与 UI 的字段名差异。

### 3. `enableVision` 从 sync.json 泄漏（顺带修掉）

`sync.json` 里残留 `enableVision: true`，而 UI 开关是关的 —— 旧值一旦被沿用，行为就和界面对不上。
现在 `enableVision` **只由调用方显式传入**，后端不再从 sync.json 读：`ListAll(enableVision bool)`、
`/api/unifyai/all?enableVision=1`、前端 `fetchAllConfig(enableVision)`。

### 4. 前端展示兜底

`normalizeOpenCodexModels`（`frontend/src/lib/unifyai.ts`）：CLI 的 `count` 字段缺失时用
`models.length` 兜底，避免有数据也显示「0 个模型」。

## 五、验证

- **CLI 对照实验**：见第三节表格，旧代码失败 / 新代码成功，代理冷启动耗时 10.4~11.3s。
- **端到端（新二进制，:3111，代理冷却 150s）**：
  - `GET /api/unifyai/all` → `count=30, models=30, degraded=false`（**就是页面「同步取数」走的那条**）
  - `GET /api/unifyai/opencodex-models` → `count=30, models=30`
  - 首个模型 `deepseek-v4-pro` 的 `contextWindow=1048576`（enrich 生效）
- **单测**：`go build ./...` 通过；`go clean -testcache` 后
  `./plugins/unifyai/...`、`./plugins/admin-api/...`、`./core/procreg/...` 全绿；
  新增 `cold_proxy_retry_test.go`（重试确实触发）、`enable_vision_stale_test.go`（已反向验证：
  注入旧行为时测试确实失败）。
- **前端**：`npx vue-tsc -b` 退出码 0。

## 六、遗留 / 未做

- **CLI 缓存字段名不一致**：CLI 写缓存用压缩名（`context`），读匹配时找的是
  `context / context_window / context_length`。后端已用 `enrichFromOpenRouter` 兜住，
  CLI 侧未动（属上游设计）。
- **`--list all` 的 `count` 字段缺失**：前端已兜底，CLI 侧未补。
- 未命中的模型（`deepseek-auto`、`-ga-` 内部名）在 OpenRouter 里本就没有，
  界面保留空白上下文而不是误填。

## 七、上线提醒

`bin/loadout.exe` 已用 `scripts/build-server.ps1` 重建（2026-09-10 13:46）。
**用户当前运行的 27584 还是旧二进制，需要重启服务才能生效。**
