# P5 CI 改动自我审查（code review）

审查对象：a2798d6 / f1e887b / fb30981 / f150503 四笔 P5 CI 改动。
审查方式：逐项核查 workflow 语法、GitHub Actions 官方语义、缓存参数、行为等价性。

## 结论：发现严重问题，已修复

**原始实现有致命错误，会直接导致 workflow 失败。已回退消重重构，保留缓存优化。**

---

## 逐项审查结果

### 1. 可复用 workflow 的 runner 语义 —— ❌ 严重错误（已修复）

**原始实现把可复用 workflow 当作 step 引用：**
```yaml
steps:
  - uses: actions/checkout@v4
  - uses: ./.github/workflows/build-frontend.yml   # 错误！
```

**错误依据（GitHub 官方文档，reusing-workflows）**：
> "Unlike when you are using actions within a workflow, you call reusable workflows **directly within a job, and not from within job steps**."
> `jobs.<job_id>.uses` 才是引用可复用 workflow 的位置（作为独立 job）。

即：
- **action** → 放在 `steps[].uses`（合法）。
- **可复用 workflow** → 必须用 `jobs.<job_id>.uses` 作为独立 job（合法）。

`steps[].uses` 只接受 action（同仓库 `./.github/actions/...` 目录或远程 action）。把 `.github/workflows/build-frontend.yml` 写进 `steps`，GitHub 会尝试把它当本地 action 加载，但该路径不是含 `action.yml` 的 action 目录 → **报错，job 直接失败**。

**此外，原始设计依赖的"共享 workspace"特性不成立**：可复用 workflow 是独立 job，job 之间不共享 workspace 和文件系统。即使把引用改到 job 级，`frontend/dist/` 产物也无法被调用方 job 访问（需要 `upload-artifact`/`download-artifact` 传递）。原 release 的打包/embed/copy 步骤依赖 `frontend/dist/`，产物丢失会直接挂。

**修复**：
- 删除错误的 `.github/workflows/build-frontend.yml`。
- 回退 ci.yml、release.yml 为原始内联前端构建，**保留缓存参数**（行为与改造前完全一致）。

### 2. 缓存正确性 —— ✅ 通过（保留）

- **pnpm store 缓存**：`cache_dependency_path: frontend/pnpm-lock.yaml`
  - `frontend/` 是独立 pnpm 工作区（有 `pnpm-workspace.yaml`），lockfile 确实在 `frontend/pnpm-lock.yaml`，路径准确（已 ls 确认）。
  - 缓存 key 基于该 lockfile 内容 hash，lockfile 不变即命中。
  - pnpm/action-setup@v4 参数名 `cache` / `cache_dependency_path` 已对照官方 README 核实无误。
- **setup-go 缓存**：`cache: true`，`go.sum` 位于仓库根（已确认），cache 默认开启，显式声明无害且意图清晰。

### 3. 行为一致性 —— ✅ 通过（修复后）

修复后三个构建点均为：node 20 + pnpm 9 + `pnpm install --frozen-lockfile` + `pnpm run build`，与改造前**完全等价**。仅新增缓存参数，不改变任何命令或产物。

### 4. YAML 语法与 action 参数 —— ✅ 通过（修复后）

- 全部 workflow YAML 用 `yaml.safe_load` 校验合法。
- 已确认无任何 step 级 `.github/workflows/` 引用残留。
- action 版本、`with`/`uses` 字段与官方用法一致。

### 5. 潜在坑 —— ✅ 通过

- windows-desktop job 的 `copy dist to desktop embed dir`（pwsh）依赖 `frontend/dist/`，因前端构建在**同一 job 内联执行**（共享 workspace），产物正常可访问。✓
- setup-go `cache: true` 在 windows/ubuntu 矩阵下通用，无平台差异报错。✓

---

## 修复记录

| 文件 | 修复动作 |
|---|---|
| `.github/workflows/build-frontend.yml` | 删除（step 级引用不合法，整体设计错误） |
| `.github/workflows/ci.yml` | 前端构建回退为内联，保留 pnpm + setup-go 缓存 |
| `.github/workflows/release.yml` | 两 job 前端构建回退为内联，保留 pnpm + setup-go 缓存 |

提交：`ci: 修正 P5 消重重构——回退为内联并保留缓存（step 级不可引用可复用 workflow）`

---

## 最终状态

- **缓存提速**：达成。pnpm store + go modules/build outputs 均加了缓存。
- **消重**：回退。在"严格限定 .github/workflows/ 下 + 行为完全一致"约束下，消重不可行：
  - composite action 需放 `.github/actions/`（超出约束）；
  - reusable workflow 作为独立 job 不共享 workspace，需 artifact 传递产物（改变行为、引入复杂度）。
  - 若后续愿意放开 `.github/actions/` 约束，可改用 composite action 实现消重，行为可保持完全一致。

## 建议（真实触发验证）

本地无法触发 GitHub Actions。建议下次真实 push/PR/tag 时观察：
1. ci.yml 矩阵在 ubuntu + windows 都跑通；
2. release.yml 前端产物正常被打包/embed，Release 页出现 tar.gz 与 exe；
3. 缓存命中日志出现 "cache hit" 或第二次运行明显变快。
