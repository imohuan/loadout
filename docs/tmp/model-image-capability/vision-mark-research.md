# 调查：catalog 里 29 个模型 input_modalities 全标 ['text','image']，图片能力到底哪来的？

日期：2026-09-06
结论性质：机制基本查清 + 一处运行时残留待定

## 一、铁的事实（已确认）

1. **Loadout 的上游 `/v1/models` 完全不报 modality。**
   代码 `D:\Code\Git\loadout\plugins\model-gateway\service.go`
   `HandleModelsV2`（约 395-425 行）每条 item 只写：
   `{"id": m, "object": "model"}`，外加 `ctx>0` 时的 `context_length`。
   **没有任何 input_modalities / modalities / vision 字段。**
   => 这些 image 标记 100% 不是 Loadout 上报的，跟本次加的 context_length 也无关。

2. **目录中 29 条都是 opencodex 自建的路由行**（slug 带 `Loadout/`、`IMOHUAN/` 前缀），
   provenance（`opencodex_capability_provenance`）里**只写了 `{provider, model_id, context_window}`**，
   **没写 input_modalities**——说明在 stamp 那一刻，模型自身 modality 是空、内置元数据表也没匹配到。

3. 但同一行的 `entry.input_modalities` 却是 `['text','image']`。
   => image 是**在 stamped provenance 之后、写入最终 catalog 文件之前**被某个环节补进去的，
   且补得**非常一致（全 29 行）**，跟模型是否真支持看图无关（git-commit/hy4/纯文本 deepseek-v4-flash 也标了）。

## 二、opencodex 里"标 image"的机制（已确认）

opencodex 源码（运行版 = `%APPDATA%\npm\node_modules\@bitkyc08\opencodex@2.43.0`，
与本地 /tmp/ocx/repo HEAD 06ec553 逐字一致）有两条把 model 标成 image 的路径，本质都是
**"视觉 sidecar（先用一个能看图的 describer 模型把图转成文字描述）"**：

- `src/codex/catalog/provider-fetch.ts:783-793`（applyProviderConfigHints）：
  若 `isModelVisionSidecarConsumer(prov, model.id)` 为真，就给该模型补 image。
- 语义（`src/vision/eligibility.ts` 头部注释 + registry.ts 大量注释）：
  一个模型在 provider 的 `noVisionModels` 里（= 它本身不能看图，是文本模型）
  **或** 它声明的 modality 是 text-without-image，都会被判定为"需要 vision sidecar 帮它看图"，
  于是 catalog 必须对客户端标 image，否则 Codex app 在 sidecar 跑起来之前就把图片附件拦了。

- 各官方 provider 在 `src/providers/registry.ts` 里都维护了各自的 noVisionModels：
  deepseek 官方把 `deepseek-v4-pro/flash` 列进去、zhipu/ollama/alibaba-token-plan 等把
  `glm-5.2/5.3`、`minimax-m2.5` 等列进去（这些确实是文本-only 代码模型）。

- 还有一张巨型内置元数据表（`src/generated/model-metadata.ts`，OpenRouter 抓的）+ registry
  的 `modelInputModalities` 表，按"裸模型 id"记录真能看图的那批（kimi-k3、minimax-m3、
  claude-opus-5、gpt-5.6 等）。这批是真·视觉模型或按 sidecar 补 image。

## 三、关键点：为什么"Loadout 这个自定义 provider"的 28 条也全被标 image？

**静态读码却说不通的地方就在这里。**
- Loadout 在 config 里只有 adapter/baseUrl/authMode/apiKey，没有 noVisionModels / modelInputModalities。
- `enrichProviderFromRegistry` 对非注册表名字（"Loadout" 不在 registry）只走
  destination 兜底分支，**不会**给它填 noVisionModels/modelInputModalities。
- 因此对 Loadout 的模型，`isModelVisionSidecarConsumer` 应返回 false，上述 783 行补 image 不该触发；
  发现路径默认回退应是 `["text"]`（ensureStrictCatalogFields）而非 image。
- git-commit / hy4 / doubao / deepseek-auto 这几个在任何内置表里都查不到（0 命中），更不该有 image。

=> 现实结果（全 29 行一致 image）与"干净重建应从这些路径得出"矛盾。
   最可能解释：这些行是**从磁盘持久化的缓存/catalog 里被 preserve/merge 回来的**，
   image 标记是在更早的一次 opencodex 运行状态（配置/版本不同，或 sidecar 规则覆盖到整个默认
   openai-chat provider）里被写进去并缓存住的；本次只刷新了 context_length（新字段），
   没重算 modality。证据：provenance 无 modality + entry 有 image 的组合只有"跨版本/跨状态缓存"才成立。

## 四、给用户的答复要点（大白话）

- 这些 `['text','image']` **不代表这些模型原生支持看图**。
- 它俩来源是两层叠加：
  1. 真能看图的那批（claude-opus/sonnet-5、gpt-5.6、kimi-k3、minimax-m3、glm-5.2……）
     按 opencodex 内置元数据标 image，是真的。
  2. 纯文本代码模型（deepseek-v4-flash/pro、glm-5.x、git-commit、hy4……）被 opencodex 用
     **视觉 sidecar** 机制补成 image：客户端允许发图 → opencodex 先用一个能看图的模型把图
     描述成文字 → 再喂给这些纯文本模型。所以"能发图"≠"模型自己能看图"，是网关兜底。
- Loadout 自己一分钱 modality 都没报，这些标记全是 opencodex 侧的决定，跟本次 context_length 改动无关。

## 五、尚待定的一个问题

"为什么 Loadout 这 28 条被**无差别**补 image（含任何表都查不到的 git-commit/hy4）"，
静态读当前源码推不出，疑似陈旧缓存/持久化 catalog 保留所致。
**若要钉死**：在 opencodex 里强制清 catalog 重建缓存后重看
（或抓一次 opencodex 真正跑 deriveEntry 前后的 model.inputModalities），确认是缓存残留还是
有个 provider 级 image 兜底规则。该问题不影响上面"图片能力≠原生看图"的结论。
