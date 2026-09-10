export function formatDate(value?: string) {
  return value
    ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(
        new Date(value),
      )
    : '-'
}

export function formatDuration(value?: number) {
  if (value === undefined || value === null) return '-'
  return value < 1000 ? `${value} ms` : `${(value / 1000).toFixed(2)} s`
}

/**
 * 字节数转人类可读（B / KB / MB / GB）。
 * 二进制进位（1024）与后端按 MB 限制容量的口径一致。
 * 负值/非法值一律显示 0 B，避免出现 "-1 B" 这种噪音。
 */
export function formatBytes(value?: number) {
  const bytes = value ?? 0
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let size = bytes / 1024
  let index = 0
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024
    index++
  }
  // 小于 10 保留一位小数（1.5 MB），否则取整（128 MB），避免按钮文字抖动。
  return `${size < 10 ? size.toFixed(1) : Math.round(size)} ${units[index]}`
}
