# P5 CI 提速优化计划

## 任务概述
- 目标：给 GitHub Actions CI/Release 提速。
- 手段：1) 加缓存（Go modules、pnpm store）；2) 消除两个 workflow 中重复的前端构建逻辑。
- 改动范围严格限定在 `.github/workflows/` 下。

## 现状分析
- `ci.yml`：test job（ubuntu/windows 矩阵），含 build frontend + go test/vet。
- `release.yml`：linux-server、windows-desktop 两个 job，都含 build frontend。
- 前端构建块（setup-node + pnpm/action-setup + install + build）在两个文件共出现 **3 次**，完全一致。
- setup-go@v5 自带缓存（默认开）；pnpm/action-setup@v4 支持 `cache` + `cache_dependency_path`，但当前未开。

## 改动清单
1. **新建 `.github/workflows/build-frontend.yml`**
   - `on: workflow_call`（可复用 workflow）。
   - 封装：setup-node@v4(node 20) + pnpm/action-setup@v4(version 9, cache: true, cache_dependency_path: frontend/pnpm-lock.yaml) + `pnpm install --frozen-lockfile` + `pnpm run build`（working-directory: frontend）。
   - 说明：本地可复用 workflow 与调用方在同一 runner/job 执行，共享 workspace，`frontend/dist/` 产物可被后续步骤使用，行为与内联完全一致。

2. **ci.yml**
   - 用 `uses: ./.github/workflows/build-frontend.yml` 替换内联前端构建块。
   - setup-go 显式加 `cache: true`（注释说明 setup-go 缓存默认开启，显式声明使意图清晰）。

3. **release.yml**
   - linux-server、windows-desktop 两个 job 同样替换前端构建块。
   - setup-go 显式加 `cache: true`。

## 关键参数核对（已查官方 README）
- `actions/setup-go@v5`：`cache` 默认 true，缓存 go modules + build outputs，go.sum 在根目录。
- `pnpm/action-setup@v4`：`cache`(bool) 缓存 pnpm store；`cache_dependency_path` 默认 `pnpm-lock.yaml`，因 lockfile 在 `frontend/` 需指定 `frontend/pnpm-lock.yaml`。

## 验证方式
- 本地无法触发 Actions，通过 YAML 语法解析 + 参数与官方 README 逐项核对保证正确性。
