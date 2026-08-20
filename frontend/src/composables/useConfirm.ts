import { reactive } from 'vue'

export interface ConfirmOptions {
  /** 对话框标题，默认为 "请确认" */
  title?: string
  /** 对话框说明文字，可为空 */
  description?: string
  /** 确认按钮文案，默认 "确认" */
  confirmText?: string
  /** 取消按钮文案，默认 "取消" */
  cancelText?: string
  /** 确认按钮是否使用危险样式（红色），默认 true */
  destructive?: boolean
}

interface ConfirmState {
  open: boolean
  options: Required<ConfirmOptions>
  resolve?: (value: boolean) => void
}

// 模块级单例：所有调用方共享同一个对话框实例
const state = reactive<ConfirmState>({
  open: false,
  options: {
    title: '请确认',
    description: '',
    confirmText: '确认',
    cancelText: '取消',
    destructive: true,
  },
  resolve: undefined,
})

/**
 * 全局确认对话框。
 *
 * 用法（替代原生 confirm()）：
 * ```ts
 * if (!(await confirmDialog('删除渠道「xxx」？'))) return
 * if (!(await confirmDialog({ title: '删除？', description: '操作不可恢复' }))) return
 * ```
 */
export function useConfirm() {
  function confirmDialog(options: string | ConfirmOptions): Promise<boolean> {
    const opts: ConfirmOptions = typeof options === 'string' ? { title: options } : options
    state.options = {
      title: opts.title ?? '请确认',
      description: opts.description ?? '',
      confirmText: opts.confirmText ?? '确认',
      cancelText: opts.cancelText ?? '取消',
      destructive: opts.destructive ?? true,
    }
    state.open = true
    return new Promise((resolve) => {
      state.resolve = resolve
    })
  }

  function resolve(value: boolean) {
    state.open = false
    state.resolve?.(value)
    state.resolve = undefined
  }

  return { state, confirmDialog, resolve }
}
