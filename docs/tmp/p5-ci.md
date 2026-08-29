# P5 CI 提速优化

## 目标
- 给 GitHub Actions 的 CI / Release 加缓存（Go modules、pnpm store），提速构建。
- 改动范围严格限定在 `.github/workflows/` 下。

## ⚠️ 重要更正（自我审查后修正）

最初尝试把重复的前端构建抽成可复用 workflow（`build-frontend.yml`），经严格审查确认**该设计错误**：
- GitHub Actions 规定可复用 workflow 必须用 `jobs.<job_id>.uses` 作为**独立 job** 引用，**不能放在 `steps[].uses`**（steps 里只能放 action）。
- 即使作为独立 job，job 之间**不共享 workspace**，`frontend/dist/` 产物无法传给调用方，release 打包/embed 会失败。

因此**已回退消重重构**，恢复为内联写法，**只保留缓存优化**（行为与改造前完全一致）。

## 实际改动

### `.github/workflows/ci.yml`
- 前端构建块（node 20 + pnpm 9 + install + build）保持内联，**新增 pnpm store 缓存**：
  - `pnpm/action-setup@v4` 加 `cache: true` + `cache_dependency_path: frontend/pnpm-lock.yaml`（frontend 是独立 pnpm 工作区，lockfile 在 frontend/ 下，key 基于其内容 hash）。
- `actions/setup-go@v5` 加 `cache: true`（setup-go 自带缓存，go.sum 在仓库根）。

### `.github/workflows/release.yml`
- linux-server、windows-desktop 两个 job 的前端构建均保持内联，同样新增 pnpm store 缓存。
- 两个 job 的 `actions/setup-go@v5` 均加 `cache: true`。

### 删除 `.github/workflows/build-frontend.yml`（错误设计）

## 参数核对（已查官方 README / 文档）
- `actions/setup-go@v5`：`cache` 默认 true，缓存 go modules + build outputs，依赖 `go.sum` 在根目录。
- `pnpm/action-setup@v4`：`cache`(bool) + `cache_dependency_path`（默认 `pnpm-lock.yaml`，本仓库需显式指 `frontend/pnpm-lock.yaml`）。
- 可复用 workflow 引用语义：`steps[].uses` 只接受 action，可复用 workflow 须作独立 job（见更正）。

## 验证方式
- 本地无法触发 Actions：
  1. Python `yaml.safe_load` 校验全部 workflow YAML 合法。
  2. 确认无 step 级 workflow 引用残留。
  3. 参数与官方 README 逐项核对。
  4. 建议真实 push/PR/tag 时观察缓存命中与产物正常。

## 提速收益
- **pnpm**：3 个前端构建点命中 store 缓存，不再每次全量下载依赖（lockfile 不变即命中）。
- **Go**：所有 job 缓存 go modules 与 build outputs，重复构建不再重新下载/编译公共依赖。
