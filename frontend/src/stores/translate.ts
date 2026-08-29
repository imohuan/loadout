// 翻译 pinia store：集中管理翻译的全局配置、内存缓存、SSE 批量进度。
// 页面（TranslateView）与可复用组件（TranslateText）都从这里取状态/发起翻译，
// 避免各处自建 SSE 连接与重复请求。
import { ref } from 'vue'
import { defineStore } from 'pinia'
import {
  startTranslateBatch,
  getTranslateBatchStatus,
  cancelTranslateBatch,
  translateLookup,
  translateText,
  type TranslateRequest,
} from '@/lib/api'

export type TranslateDisplayMode = 'translated' | 'original' | 'both'

export const useTranslateStore = defineStore('translate', () => {
  // ---- 配置（设置页可改）----
  const targetLang = ref('zh-CN')
  const model = ref('')
  const prompt = ref(
    '你是一个专业翻译。请把下面的文本翻译成{lang}。要求：\n' +
      '- 保持原意，语气自然，符合目标语言习惯\n' +
      '- 保留代码、URL、占位符、变量名、标点等不变\n' +
      '- 只输出译文，不要加任何解释、注释或多余内容\n' +
      '- 如果原文已经是目标语言或无需翻译（纯代码/数字/URL），原样返回',
  )
  const displayMode = ref<TranslateDisplayMode>('translated')
  // 批量翻译并发数（后端 worker 池大小）。
  const batchConcurrency = ref(5)

  // ---- 内存缓存：contentHash -> { lang, text } ----
  const cache = ref<Map<string, { lang: string; text: string }>>(new Map())
  // ---- 翻译结果 map：key(textKey) -> 译文。批量 + 单条 + lookup 都写入，
  //      供 TranslateText 组件按 key 读取展示（组件本身不触发翻译）。----
  const translatedMap = ref<Map<string, string>>(new Map())

  // ---- SSE 批量进度（全局共享，进度条读这个）----
  const batchRunning = ref(false)
  const batchTotal = ref(0)
  const batchDone = ref(0)
  const batchError = ref('')
  const batchFinished = ref(false)
  const batchResults = ref<Map<string, string>>(new Map())

  function setConfig(cfg: Partial<{ targetLang: string; model: string; prompt: string; displayMode: TranslateDisplayMode }>) {
    if (cfg.targetLang !== undefined) targetLang.value = cfg.targetLang
    if (cfg.model !== undefined) model.value = cfg.model
    if (cfg.prompt !== undefined) prompt.value = cfg.prompt
    if (cfg.displayMode !== undefined) displayMode.value = cfg.displayMode
  }

  function cacheKey(text: string) {
    let h = 2166136261
    for (let i = 0; i < text.length; i++) {
      h ^= text.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
    return (h >>> 0).toString(16)
  }

  /** 取文本译文：先查内存缓存，未命中返回 null。 */
  function getCached(text: string, lang = targetLang.value): string | null {
    const h = cacheKey(text)
    const entry = cache.value.get(h)
    if (entry && entry.lang === lang) return entry.text
    return null
  }

  /** 按 textKey 读翻译结果（只读）。 */
  function getTranslated(key: string): string | null {
    return translatedMap.value.get(key) ?? null
  }

  // ---- 缓存读写辅助：把"查缓存/写缓存 + 同步 translatedMap"收敛到一处，避免各查询函数重复 ----

  /** 读缓存命中时把结果同步到 translatedMap，并返回译文；未命中返回 null。 */
  function readCached(source: string, lang: string, textKey?: string): string | null {
    if (textKey && translatedMap.value.has(textKey)) return translatedMap.value.get(textKey)!
    const cached = getCached(source, lang)
    if (cached !== null && textKey) setTranslated(textKey, cached)
    return cached
  }

  /** 把一次翻译结果同时写入内存缓存与 translatedMap。text 为空/null 时不写入。 */
  function writeResult(source: string, lang: string, text: string | null, textKey?: string) {
    if (!text) return
    cache.value.set(cacheKey(source), { lang, text })
    if (textKey) setTranslated(textKey, text)
  }

  /** 翻译一段文本（单条，会真实调用模型）。textKey 用于记录结果供组件读取。 */
  async function translateOne(source: string, opts: Partial<TranslateRequest> & { textKey?: string } = {}): Promise<string> {
    const lang = opts.target_lang ?? targetLang.value
    const cached = readCached(source, lang, opts.textKey)
    if (cached !== null) return cached
    const req: TranslateRequest = {
      source_text: source,
      target_lang: lang,
      model: opts.model ?? model.value,
      prompt: opts.prompt ?? prompt.value,
      source_type: opts.source_type,
      source_id: opts.source_id,
      key: opts.key,
      type: opts.type,
    }
    const resp = await translateText(req)
    const text = resp.text ?? resp.texts?.[0] ?? source
    writeResult(source, lang, text, opts.textKey)
    return text
  }

  /** 只读查询已翻译结果（不触发翻译）。textKey 记录结果供组件读取。 */
  async function lookupText(source: string, lang = targetLang.value, textKey?: string): Promise<string | null> {
    if (!source) return null
    const cached = readCached(source, lang, textKey)
    if (cached !== null) return cached
    try {
      const resp = await translateLookup({ source_text: source, target_lang: lang })
      const text = resp.texts?.[0] ?? null
      writeResult(source, lang, text, textKey)
      return text
    } catch {
      return null
    }
  }

  /** 批量只读查询（不触发翻译）：一次请求查多条文本，把已有译文写入 translatedMap 与内存缓存。
   *  用于页面加载时集中灌入数据库已有译文，避免每个 TranslateText 单独发请求。 */
  async function lookupBatch(
    items: { text: string; textKey?: string }[],
    lang = targetLang.value,
  ): Promise<void> {
    const toQuery: { text: string; textKey?: string }[] = []
    for (const it of items) {
      if (!it.text) continue
      if (readCached(it.text, lang, it.textKey) !== null) continue
      toQuery.push(it)
    }
    if (!toQuery.length) return
    try {
      const resp = await translateLookup({ items: toQuery, target_lang: lang })
      const texts = resp.texts || []
      for (let i = 0; i < toQuery.length && i < texts.length; i++) {
        const t = texts[i]
        if (!t) continue
        writeResult(toQuery[i].text, lang, t, toQuery[i].textKey)
      }
    } catch {
      // 忽略批量查询失败
    }
  }

  function setTranslated(key: string, text: string) {
    if (text) translatedMap.value.set(key, text)
  }

  // ---- 批量翻译：后台任务 + 轮询进度（刷新后可恢复） ----

  type BatchItemLike = {
    source_type: string
    source_id: string
    description: string
    textKey?: string
  }

  // localStorage 里持久化的批量任务信息，供刷新后恢复进度
  interface PersistedBatch {
    task_id: string
    items: BatchItemLike[]
    target_lang: string
    model: string
    prompt: string
  }
  const BATCH_STORAGE_KEY = 'translate_batch_task'
  let pollTimer: ReturnType<typeof setInterval> | null = null

  function saveBatchTask(p: PersistedBatch) {
    try {
      localStorage.setItem(BATCH_STORAGE_KEY, JSON.stringify(p))
    } catch {
      /* 忽略存储失败 */
    }
  }
  function clearBatchTask() {
    try {
      localStorage.removeItem(BATCH_STORAGE_KEY)
    } catch {
      /* 忽略 */
    }
  }
  function readBatchTask(): PersistedBatch | null {
    try {
      const raw = localStorage.getItem(BATCH_STORAGE_KEY)
      if (!raw) return null
      const p = JSON.parse(raw) as PersistedBatch
      return p && p.task_id ? p : null
    } catch {
      return null
    }
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  // 轮询任务进度：每 1s 查询，更新 batchDone/batchTotal；finished/cancelled 时停止并触发回调。
  // onDone 在任务真正结束（完成或取消）时调用一次，用于刷新译文/提示。
  async function pollBatch(taskId: string, items: BatchItemLike[]): Promise<void> {
    return new Promise((resolve) => {
      stopPolling()
      let settled = false
      const finish = (cancelled: boolean) => {
        if (settled) return
        settled = true
        stopPolling()
        clearBatchTask()
        batchRunning.value = false
        batchFinished.value = !cancelled
        resolve()
      }
      const tick = async () => {
        let status
        try {
          status = await getTranslateBatchStatus(taskId)
        } catch {
          return // 网络抖动等，下轮再试
        }
        if (!status) return
        batchTotal.value = status.total
        batchDone.value = status.done
        if (status.error) batchError.value = status.error
        if (status.cancelled) {
          finish(true)
        } else if (status.finished) {
          // 完成后刷新译文：把已翻条目灌入 translatedMap
          void refreshFromItems(items)
          finish(false)
        }
      }
      void tick()
      pollTimer = setInterval(tick, 1000)
    })
  }

  // 用批量条目文本批量 lookup，把已有译文写入内存，供组件刷新显示
  async function refreshFromItems(items: BatchItemLike[]) {
    const texts: { text: string; textKey?: string }[] = []
    for (const it of items) {
      if (it.description) texts.push({ text: it.description, textKey: it.textKey })
    }
    if (texts.length) await lookupBatch(texts)
  }

  /** 启动后台批量翻译并轮询进度（刷新页面后调用 resumeBatch 可恢复进度条）。 */
  async function translateBatch(
    items: BatchItemLike[],
    opts: Partial<TranslateRequest> = {},
  ): Promise<void> {
    batchRunning.value = true
    batchTotal.value = items.length
    batchDone.value = 0
    batchError.value = ''
    batchFinished.value = false
    batchResults.value = new Map()

    const targetLangNow = opts.target_lang ?? targetLang.value
    const modelNow = opts.model ?? model.value
    const promptNow = opts.prompt ?? prompt.value
    const started = await startTranslateBatch({
      items: items.map((it) => ({
        source_type: it.source_type,
        source_id: it.source_id,
        description: it.description,
        key: it.textKey,
      })),
      target_lang: targetLangNow,
      model: modelNow,
      prompt: promptNow,
      type: opts.type,
      concurrency: batchConcurrency.value,
    })
    saveBatchTask({
      task_id: started.task_id,
      items,
      target_lang: targetLangNow,
      model: modelNow,
      prompt: promptNow,
    })
    await pollBatch(started.task_id, items)
  }

  /** 刷新后恢复：若 localStorage 里存在未完成任务，恢复进度条并启动轮询到结束。 */
  async function resumeBatch(): Promise<boolean> {
    const p = readBatchTask()
    if (!p) return false
    batchRunning.value = true
    batchTotal.value = p.items.length
    batchDone.value = 0
    batchFinished.value = false
    // 不阻塞调用方：后台轮询，任务结束后自动收起进度条
    void pollBatch(p.task_id, p.items)
    return true
  }

  /** 取消当前批量翻译（后台任务）。 */
  async function cancelBatch() {
    const p = readBatchTask()
    if (p?.task_id) {
      try {
        await cancelTranslateBatch(p.task_id)
      } catch {
        /* 任务可能已结束，忽略 */
      }
    }
    stopPolling()
    clearBatchTask()
    batchRunning.value = false
  }

  return {
    targetLang,
    model,
    prompt,
    displayMode,
    batchConcurrency,
    batchRunning,
    batchTotal,
    batchDone,
    batchError,
    batchFinished,
    batchResults,
    setConfig,
    getCached,
    getTranslated,
    translateOne,
    lookupText,
    lookupBatch,
    translateBatch,
    resumeBatch,
    cancelBatch,
  }
})
