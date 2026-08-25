# 10 - 部署与排错

> 本文讲如何构建、运行、部署 Loadout，以及常见排错。整合自 `README.md` 与 `docs/archive/desktop/`。
> 配套：[01-架构总览](./01-architecture.md)、[08-数据存储](./08-data-storage.md)。

## 1. 构建

```bash
# 服务端（Linux / 跨平台）
go build -o loadout ./apps/server

# 桌面端（Windows，Wails 壳，内嵌服务端）
# 见 scripts/pack-desktop.ps1 / pack.ps1（Windows）
```

## 2. 运行

```bash
./loadout
# 首次启动在 ~/.loadout/admin-password 生成随机管理员密码
```

- 管理后台：浏览器打开 `http://127.0.0.1:3000`（桌面版仅监听 127.0.0.1）。
- 账号 `admin`，密码见 `~/.loadout/admin-password`；**改密后该文件自动删除**。
- 对外三入口见 [01-架构总览](./01-architecture.md)。

## 3. Linux 部署（GitHub Actions 产出）

Linux 产物由 GitHub Actions 在打 tag 发版时自动构建（见 `.github/workflows/release.yml`），
产出 `loadout-server-linux-amd64.tar.gz`（单二进制 + systemd 单元）。手动部署步骤：

```bash
# 下载对应版本的 Release 产物并解压
curl -sL https://github.com/imohuan/loadout/releases/download/v0.1.0/loadout-server-linux-amd64.tar.gz -o loadout.tar.gz
tar -xzf loadout.tar.gz && rm loadout.tar.gz
sudo cp loadout-server/loadout /usr/local/bin/loadout
sudo chmod +x /usr/local/bin/loadout

# systemd 服务
sudo cp loadout-server/loadout.service /etc/systemd/system/
# 改端口/数据目录：vim /etc/systemd/system/loadout.service
sudo systemctl daemon-reload && sudo systemctl enable --now loadout
```

## 4. Release 产物

打 tag 发版（`git tag v0.1.0 && git push origin v0.1.0`）后，GitHub Actions 自动产出：

| 产物 | 用途 |
|---|---|
| `loadout-server-linux-amd64.tar.gz` | Linux 无 UI 纯服务版（单二进制 + systemd） |
| `loadout-desktop-windows-amd64.exe` | Windows 桌面版（内嵌服务端，双击即用） |

## 5. 桌面版（Windows / Wails）

- 源码：`apps/desktop/`，打包脚本：`scripts/pack-desktop.ps1`、`scripts/pack.ps1`。
- 运行模式 `LOADOUT_RUN_MODE=desktop`：仅监听 `127.0.0.1`，避免暴露到局域网。
- SSO 登录见 `docs/archive/desktop/sso-login.md`（桌面端用系统浏览器/嵌入 WebView 完成 SSO）。

## 6. 配置（环境变量）

所有配置在 `core/config`，`LOADOUT_*` 环境变量优先（详见 [01-架构总览](./01-architecture.md) 第 5 节）：

- `LOADOUT_RUN_MODE`（server/desktop）、`LOADOUT_SERVER_ADDR`（默认 `:3000`）
- `LOADOUT_UPSTREAM_TIMEOUT`（默认 300s）、`LOADOUT_SMART_GROUP_HEADER`（默认 `X-Loadout-Group`）

## 7. 排错

常见症状与排查（整合 `docs/archive/desktop/troubleshooting-README.md`）：

| 症状 | 排查方向 |
|---|---|
| 启动失败 / 端口占用 | 检查 `LOADOUT_SERVER_ADDR`；`netstat` 看端口；换端口重启 |
| 插件装配中止 | 看启动日志——`Apply` 返回 error 会打印插件名与原因（如缺 `db`/`store` 服务、Provide 未声明） |
| 首启无法登录 | 读 `~/.loadout/admin-password`；确认未被改密删除；清 Cookie 重试 |
| 某个能力不生效 | 检查能力路由表 `capability_routes.json` 的 model/渠道匹配；注意 **native 短路会屏蔽 proxy**（见 [03](./03-plugin-dev-guide.md) 常见坑） |
| 视觉不触发 | 确认 vision_v2 挂的是 `ProxyBeforeAttempt`（不是 `ProxyBeforeUpstream`），且渠道上下文已就绪 |
| 上游频繁失败 | 看 `/api/model-health` / `/api/model-status`；失败策略见 [05-聚合与健康检查](./05-aggregate-health.md) |
| 数据异常/迁移 | 看 `core/db/migrate.go` 迁移版本；JSON 旧数据由 `ImportJSON` 迁到 SQLite（见 [08](./08-data-storage.md)） |
| 日志 | `~/.loadout/logs/loadout.log`（轮转），请求级日志带 `request_id`（`X-Request-Id`） |

## 下一步

- 回总导航 → [00-INDEX](./00-INDEX.md)
- 看历史演进文档索引 → [appendix/00-evolution](./appendix/00-evolution.md)
