import { reactive } from 'vue'

/**
 * 全局折叠状态表：key -> boolean
 * key 规则：
 *   - tool-group 用 "tg-" + 第一个 tool 的 id
 *   - 单个 shell/edit 工具用 "tool-" + tool.id
 * 来源：backup/codex-base-ui/web/src/composables/useCollapse.js（转为 TS）
 */
const state = reactive<Record<string, boolean>>({})

export function useCollapse() {
  /** 获取某个 key 的展开状态，默认 false（折叠） */
  function isOpen(key: string): boolean {
    if (!(key in state)) {
      state[key] = false
    }
    return state[key]
  }

  /** 切换 */
  function toggle(key: string) {
    state[key] = !isOpen(key)
  }

  /** 显式设置 */
  function setOpen(key: string, value: boolean) {
    state[key] = value
  }

  return { state, isOpen, toggle, setOpen }
}
