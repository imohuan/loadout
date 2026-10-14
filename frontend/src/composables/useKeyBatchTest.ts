import { computed, ref } from 'vue'
import { useModelTest } from './useModelTest'
import type { ChannelStatus } from '@/lib/types'
import type { KeyTestState } from '@/lib/modelStatus'

/**
 * 「模型状态」页的批量测试：把每个 API Key 挨个探一遍。
 *
 * 判断口径是「这个 Key 现在能不能出结果」—— 发一条最短的流式请求，
 * 上游吐出第一条内容就算通过，其余把上游原文带回来显示在左栏列表里。
 *
 * 请求走本机 /v1 网关（via_gateway + X-Loadout-Pin-Channel），而不是直连上游：
 *   - 只有经网关的请求才会写转发日志，测试才有历史记录可查；
 *   - 也只有经网关的请求才会真实更新渠道健康状态 —— 直连上游的旁路探针
 *     测完右侧表格还是旧数据，用户会以为「测了没反应」。
 *
 * 为什么要自己写一个并发池而不是 Promise.all：17 个 Key 一起打过去，上游多半
 * 直接按限流回一批 429，测出来的「失败」是并发带来的假象，而不是 Key 本身的问题。
 * 池子固定住同时在跑的请求数，测出来的结果才可信。
 */

/** 可选并发数：从「一个一个来」到「快点跑完」，够覆盖 17 个 Key 的量级。 */
export const CONCURRENCY_OPTIONS = [1, 2, 4, 8, 16]

/** 探测用的最小请求：一句 ping，不关心回答内容，只关心有没有正常返回。 */
const PROBE_MESSAGES = [{ role: 'user', content: 'ping' }]

export function useKeyBatchTest() {
  const { probe } = useModelTest()

  /** channel.id → 测试状态。没测过的 Key 不在表里（左栏就不显示状态行）。 */
  const states = ref<Record<string, KeyTestState>>({})
  const running = ref(false)
  const concurrency = ref(4)
  const finished = ref(0)
  const total = ref(0)
  /** 中止标记：点「停止」后，池子里还没开跑的 Key 直接跳过。 */
  let cancelled = false

  const progress = computed(() => `${finished.value}/${total.value}`)

  /**
   * 这个 Key 用哪个模型测。
   *
   * 优先第一个可用模型：测的是「这个 Key 现在能不能用」，拿一个本来就不可用的
   * 模型去测，失败了也说明不了 Key 的问题。全不可用时退回第一个模型 —— 至少
   * 能带回来一个真实的上游错误，而不是我们自己编一句「无可用模型」。
   */
  function pickModel(key: ChannelStatus): string {
    const available = key.models.find((m) => m.effective_available)
    return (available || key.models[0])?.model || ''
  }

  function setState(id: string, state: KeyTestState | undefined) {
    const next = { ...states.value }
    if (state) next[id] = state
    else delete next[id]
    states.value = next
  }

  /**
   * 正在跑的单次测试，键为 `${channelId}:${model}`。
   *
   * 不复用上面的 states：左栏一行对应一个 Key，右栏一行对应一个模型。拿 Key 级
   * 状态驱动模型级按钮，点一个就会让整列按钮一起转。用户要的是「每个操作单独
   * 占一行、各自有自己的进度」，所以两套状态分开维护。
   */
  const activeTests = ref<Record<string, true>>({})

  /** 单次测试的键：同一个 Key 下不同模型互不影响。 */
  function testKey(channelId: string, model: string) {
    return `${channelId}:${model}`
  }

  function setActive(key: string, on: boolean) {
    const next = { ...activeTests.value }
    if (on) next[key] = true
    else delete next[key]
    activeTests.value = next
  }

  /** 这个「Key + 模型」是否正在测：右栏每行按钮的独立 loading。 */
  function isTesting(channelId: string, model: string) {
    return activeTests.value[testKey(channelId, model)] === true
  }

  /** 测一个 Key（指定模型，缺省用 pickModel 挑一个）。 */
  async function testOne(
    key: ChannelStatus,
    model?: string,
  ): Promise<{ ok: boolean; error?: string }> {
    const id = key.channel.id
    const target = model || pickModel(key)
    if (!target) {
      setState(id, { phase: 'failed', model: '-', error: '该 Key 下没有可测的模型' })
      return { ok: false, error: '该 Key 下没有可测的模型' }
    }
    const ticket = testKey(id, target)
    setState(id, { phase: 'running', model: target })
    setActive(ticket, true)
    let result
    try {
      // via_gateway 走本机 /v1：写转发日志 + 刷新健康状态；channel_id 同时
      // 被服务端转成 X-Loadout-Pin-Channel，把请求钉死在这个 Key 上。
      result = await probe(
        { channel_id: id, suffix_mode: 'chat', via_gateway: true },
        target,
        PROBE_MESSAGES,
      )
    } finally {
      setActive(ticket, false)
    }
    if (result.ok) {
      // 成功也留下标记：同一批里哪些Key 通过、哪些没过，测完还能对着列表核账。
      // 之前成功就清空，用户只能看到失败的那几条，剩下的分不清是「通过」还是
      // 「压根没测」。
      setState(id, { phase: 'ok', model: target })
      return { ok: true }
    }
    const error = result.error || `请求失败（${result.status}）`
    setState(id, { phase: 'failed', model: target, error })
    return { ok: false, error }
  }

  /**
   * 批量测试：按并发数跑完这一批 Key，逐个回填状态。
   *
   * 每个 Key 独立成败，一个失败不影响其余 —— 批量场景里失败是常态。
   */
  async function testKeys(keys: ChannelStatus[]) {
    if (running.value || !keys.length) return
    running.value = true
    cancelled = false
    finished.value = 0
    total.value = keys.length
    // 只清这一批的状态：用户可能在别的平台刚测完一轮，那些标记不该被顺手抹掉。
    const next = { ...states.value }
    for (const key of keys) delete next[key.channel.id]
    states.value = next

    let cursor = 0
    const worker = async () => {
      for (;;) {
        if (cancelled) return
        const index = cursor++
        const key = keys[index]
        if (!key) return
        try {
          await testOne(key)
        } finally {
          finished.value += 1
        }
      }
    }
    const lanes = Math.max(1, Math.min(concurrency.value, keys.length))
    await Promise.all(Array.from({ length: lanes }, worker))
    running.value = false
  }

  /** 中止：正在跑的那几个请求会自然结束，没开跑的 Key 不再发出。 */
  function stop() {
    cancelled = true
  }

  return {
    states,
    isTesting,
    running,
    concurrency,
    finished,
    total,
    progress,
    testOne,
    testKeys,
    stop,
  }
}
