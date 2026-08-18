# scripts/test-aggregate — 端到端验证「/v1/models 含虚拟模型 + key 白名单」

7 个 PowerShell 脚本，串起来跑完整链路诊断 + 修复验证。临时数据目录
`$env:TEMP\loadout-test-aggregate` 隔离，不污染 `~/.loadout`。

## 使用

```powershell
cd D:\Code\Git\loadout
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\01-build-and-start.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\02-login.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\03-seed-test-data.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\04-probe-v1-models.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\05-probe-aggregates.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\06-test-chat-completions.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\test-aggregate\07-stop-and-report.ps1
```

或写一个 `run-all.ps1` 顺序执行，失败立即停。

## 每个脚本做什么

| # | 脚本 | 作用 |
|---|---|---|
| 01 | `01-build-and-start.ps1` | 编译 `bin\loadout.exe`，临时数据目录，后台启动服务（默认端口 3100，避免与你的 3000 冲突），写 PID，等端口就绪 |
| 02 | `02-login.ps1` | 读首启随机密码，`POST /api/login` 拿 session cookie 存 `admin-cookie.txt` |
| 03 | `03-seed-test-data.ps1` | 幂等清理 → 建 1 channel（不可达占位）+ 1 聚合模型 `auto-demo`（enabled=true）+ 2 个 sk-key（一个不限制，一个 `models=[real-model]`）|
| 04 | `04-probe-v1-models.ps1` | **核心诊断**：用不限制的 sk-key 调 `GET /v1/models`，判断返回是否含 `auto-demo` |
| 05 | `05-probe-aggregates.ps1` | 调 `GET /api/aggregates` 看 `auto-demo.enabled`，若 enabled=false 且 /v1/models 找不到 → 输出根因提示（前端缺 `enabled` 字段）|
| 06 | `06-test-chat-completions.ps1` | 端到端：`/v1/chat/completions` model=`auto-demo`，验证 ① 不限制 key 不被 403（502 因上游不可达属预期）② 限制 key 正确 403 |
| 07 | `07-stop-and-report.ps1` | 停服，汇总 7 步 reports 目录的 JSON |

## 输出

- `reports/0N-*.json`：每步的结构化结果
- `reports/0N-stdout.log`：对应步骤的控制台输出
- `$env:TEMP\loadout-test-aggregate\loadout.log`：服务日志
- `$env:TEMP\loadout-test-aggregate\admin-password`：首启随机密码
- `$env:TEMP\loadout-test-aggregate\admin-cookie.txt`：登录后的 session cookie

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `LOADOUT_TEST_HOME_DIR` | `$env:TEMP\loadout-test-aggregate` | 临时数据目录 |
| `LOADOUT_TEST_PORT` | `3100` | 监听端口 |

## 复现 bug（验证脚本能抓到）

修好前端后正常流程是全绿。若想验证脚本能抓到旧 bug：

```powershell
# 跑完 03 后手动 PUT 一个 enabled=false 的 aggregate
$cookie = (Get-Content $env:TEMP\loadout-test-aggregate\admin-cookie.txt -Raw).Trim()
$channelId = (Get-Content $env:TEMP\loadout-test-aggregate\scripts\test-aggregate\reports\03-seeded.json -Raw | ConvertFrom-Json).channel_id
$json = "[{""name"":""auto-demo"",""enabled"":false,""targets"":[{""model"":""claude-haiku-4-5-20251001"",""channel_id"":""$channelId""}]}]"
Invoke-WebRequest -Uri http://127.0.0.1:3100/api/aggregates -Headers @{Cookie=$cookie;'Content-Type'='application/json'} -Method PUT -Body $json -SkipHttpErrorCheck

# 再跑 04：应该报 ❌ /v1/models 不含 auto-demo
& scripts\test-aggregate\04-probe-v1-models.ps1
```

## 根因与修复

| 现象 | 原因 | 修复 |
|---|---|---|
| 界面创建聚合模型后 `/v1/models` 找不到 | 前端 `AggregateEditor.vue` 的 `submit()` 只发 `{name, targets}`，**没发 `enabled`**，Go 零值 `false` 写入 DB，`model-gateway/aggregateNames` 用 `if a.Enabled` 过滤掉了 | 前端 `submit()` 现在带 `enabled: form.enabled`（新建默认 `true`，编辑保留原值）|

## 清理

```powershell
Remove-Item -Recurse -Force $env:TEMP\loadout-test-aggregate
```
