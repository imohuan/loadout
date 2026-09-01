# 调查：点安装后 SSE 失效 / 按钮卡住 —— procreg 根因分析

日期：2026-08-31
作者：code-developer
状态：分析完成，待用户确认具体现象

## 结论先行

「点安装 SSE 失效」**不是 procreg 主动破坏 SSE 连接**。procreg 的进程注册/广播逻辑本身正确
（install 走的是同一个全局注册表，会广播 running/done 事件，`deps_install_test.go` 已验证）。
真正脆弱的是**两个环节叠加**：

1. **procreg.broadcast 的非阻塞丢弃机制**（核心风险点，已用测试证明）
2. **前端 installDep 的按钮恢复 / 状态刷新完全依赖 SSE 驱动的 `store.processes`**

## 证据 1：broadcast 会静默丢事件（已用测试证实）

`core/procreg/procreg.go`：

```go
func (r *Registry) broadcast(ev Event) {
    r.mu.Lock()
    for ch := range r.subs {
        select {
        case ch <- ev:
        default:          // ← channel 满时静默丢弃
        }
    }
    r.mu.Unlock()
}
```

`Subscribe()` 的 channel 容量 = 64。写了一个针对性测试
`TestBroadcastDropsWhenChannelFull`（已运行，临时测试，跑完已删除）：
- 慢消费者把 channel 塞满 64 条后，第 65 条事件（相当于 install 的 done）被丢弃。
- 实测只读到 64 条，第 65 条丢。**非阻塞丢弃是设计权衡，但关键事件会因此丢失。**

什么时候会塞满？安装期间：
- 内存采样器每 2 秒对所有运行中进程广播 `SetMem`（sampleInterval=2s）。
- 若同时有多个进程在跑（skill 更新 / unifyai / MCP / 其他 npm 检查），加上 SSE handler
  写慢客户端暂时阻塞，channel 会在 10 秒级内积压满 64。

## 证据 2：前端按钮/状态完全依赖 SSE 驱动的 processes

`frontend/src/views/ManagementView.vue` installDep：

```js
const timer = setInterval(() => {
    const st = store.settledOf(taskId)      // 只从 store.processes（SSE 更新）里查
    if (st === 'done' || st === 'error') settle(...)
}, 1000)
setTimeout(() => settle('install timeout'), 5 * 60 * 1000)
```

- `settledOf` 只查 `store.processes`，而 `store.processes` 只被 SSE 的 snapshot/update 更新。
- 若 SSE 断了且重连慢，或 done 事件被 broadcast 丢弃且重连 snapshot 未覆盖，
  `settledOf` 恒返回 `'running'`/`'missing'`，轮询空转 → 按钮转圈直到 5 分钟超时兜底。

## 证据 3：后端日志显示 install 期间 SSE 连接本身是正常的

`~/.loadout/logs/loadout.log`：
- 12:57:32 install → 12:57:37 refresh（后端自动）
- 12:59:42 install → 12:59:44 refresh
- 13:03:49 install → 13:03:56 refresh
- 上述时间点的 SSE 连接都是长连接（137s~332s），没有「点安装导致连接断开」的对应关系。
- 但存在大量短连接（560ms/900ms/6547ms）和并发连接（多个 tab / 重连），说明 SSE 本身在用户环境不太稳定。

## 判断

- 「点安装 SSE 就失效」更像是在**特定时机**（SSE 恰好断开/积压，或后端重启清空历史）下的表现，
  而非 install 点击的确定性副作用。
- 根因机制：**SSE update/done 事件丢失 → store.processes 不更新 → 前端轮询/对账拿不到终态。**

## 待确认（需要用户提供）

1. 是否**每次**点安装都失效，还是偶发？
2. 失效时：进程面板（底部）有没有显示 install 进程？它停在 running 还是直接消失？
3. 失效时：按钮是转圈（加载态）还是已恢复但状态没刷新？
4. 用的是浏览器标签页还是桌面 WebView？是否同时开了多个页面？

## 可能的改进方向（未实施，待确认后再定）

1. procreg：`Subscribe` channel 容量加大（64 → 256+）；或 broadcast 对「满」时至少保证
   最新状态可追溯（如把最新快照合并进 update，避免关键终态丢失）。
2. 前端：settledOf 轮询改造成「SSE + 主动查后端」双通道；按钮恢复改用更可靠的后端轮询
   （如查 /api/processes 或让 refresh 返回 taskId 的终态），不单依赖 SSE。
