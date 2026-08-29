# P5 CI 提速优化

## 目标
- 给 GitHub Actions 的 CI / Release 加缓存（Go modules、pnpm store），消除两个 workflow 重复的前端构建逻辑，提速构建。
- 改动范围严格限定在 `.github/workflows/` 下。

## 改动文件
1. **`.github/workflows/build-frontend.yml`（新增）** — 可复用的前端构建 workflow
   - `on: workflow_call`，通过 `inputs.runs-on` 接收运行平台（跟随调用方 runner）。
   - 内含：`actions/setup-node@v4`(node 20) + `pnpm/action-setup@v4`(version 9) + `pnpm install --frozen-lockfile` + `pnpm run build`。
   - **pnpm store 缓存**：`pnpm/action-setup@v4` 内置 `cache: true` + `cache_dependency_path: frontend/pnpm-lock.yaml`。
     - 关键：`frontend/` 是独立 pnpm 工作区（有 `pnpm-workspace.yaml`），lockfile 在 `frontend/pnpm-lock.yaml` 而非仓库根，必须显式指定路径，否则缓存 key 会用错误的 lockfile。
   - 本地可复用 workflow（`uses: ./.github/workflows/...`）与调用方在同一 runner/job 执行，共享 workspace，`frontend/dist/` 产物可被后续步骤使用，行为与内联完全一致。

2. **`.github/workflows/ci.yml`**
   - 用 `uses: ./.github/workflows/build-frontend.yml` 替换 test job 内联的前端构建块（setup-node + pnpm + install + build）。
   - `actions/setup-go@v5` 显式加 `cache: true`（setup-go 自带缓存，默认开启，go.sum 在根目录无需额外配置；显式声明使意图清晰）。

3. **`.github/workflows/release.yml`**
   - linux-server、windows-desktop 两个 job 均用 reusable workflow 替换内联前端构建块。
   - 两个 job 的 `actions/setup-go@v5` 均显式加 `cache: true`。

## 参数核对（已查官方 README）
- `actions/setup-go@v5`：`cache` 默认 true，缓存 go modules + build outputs，依赖文件 `go.sum` 位于仓库根，`cache-dependency-path` 无需指定。
- `pnpm/action-setup@v4`：`cache`(bool) 启用 pnpm store 缓存；`cache_dependency_path` 默认 `pnpm-lock.yaml`，因本仓库 lockfile 在 `frontend/` 下，指定为 `frontend/pnpm-lock.yaml`。

## 验证方式
- 本地无法触发 GitHub Actions，采用：
  1. Python `yaml.safe_load` 校验全部 workflow YAML 语法合法。
  2. 校验 3 处调用点传参（`with.runs-on`）与 reusable workflow 声明的 `inputs.runs-on` 完全匹配。
  3. 参数与官方 action README 逐项核对。

## 提速收益
- **pnpm**：3 个 job 的前端构建从每次全量下载依赖，变为命中 store 缓存（key 基于 `frontend/pnpm-lock.yaml` 内容 hash，lockfile 不变即命中）。
- **Go**：所有 job 缓存 go modules 与 build outputs，重复 build 不再重新下载/编译公共依赖。
- **消重**：前端构建逻辑由重复 3 份收敛为 1 处，后续版本/脚本调整只需改一处。
