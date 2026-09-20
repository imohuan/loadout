export function formatDate(value?: string) {
  return value
    ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(
        new Date(value),
      )
    : '-'
}

/**
 * formatDateTimeCN 把 ISO 时间串格式化成北京时间（Asia/Shanghai）的
 * 「YYYY-MM-DD HH:mm:ss」。
 *
 * 后端统一以 UTC 存/传时间（RFC3339，如 2026-09-19T19:13:13Z），直接对字符串
 * 做 slice 会显示成 UTC 时刻，比北京时间早 8 小时；这里显式指定时区格式化，
 * 保证用户在任何机器时区下看到的都是北京时间。
 */
export function formatDateTimeCN(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).formatToParts(date)
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  // zh-CN 的 hour 在 hour12:false 下可能出现 '24'（表示午夜），归一成 '00'。
  const hour = get('hour') === '24' ? '00' : get('hour')
  return `${get('year')}-${get('month')}-${get('day')} ${hour}:${get('minute')}:${get('second')}`
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
