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
 * Token 数量转人类可读（K / M / B / T）。
 *
 * 模型统计接口返回的是原始 token 数（单日可能上亿），直接展示会出现
 * 「118742.5K」这种越长越难读的数字；这里统一进位到 K/M/B/T，
 * 让「积分消耗月历」「模型消耗分布」与页面顶部的柱状图口径保持一致。
 *
 * 十进制进位（1K = 1000），与模型厂商和账单口径一致；
 * 保留两位有效小数并去掉多余的 0（1K、1.5K、118.74M）。
 * 非法值一律显示 0，避免出现 NaN。
 */
export function formatTokens(value?: number) {
  const n = value ?? 0
  if (!Number.isFinite(n)) return '0'
  const sign = n < 0 ? '-' : ''
  const units = ['', 'K', 'M', 'B', 'T']
  let scaled = Math.abs(n)
  let index = 0
  while (scaled >= 1000 && index < units.length - 1) {
    scaled /= 1000
    index++
  }
  // 小于 1000 直接取整（token 数都是整数，不会出现小数点）。
  if (index === 0) return sign + String(Math.round(scaled))
  let text = scaled.toFixed(2)
  // 四舍五入后越界时再进一位：999999 → 「1000.00K」应为「1M」。
  if (Number(text) >= 1000 && index < units.length - 1) {
    index++
    text = (Number(text) / 1000).toFixed(2)
  }
  return `${sign}${text.replace(/\.?0+$/, '')}${units[index]}`
}

/**
 * 紧凑日期时间（如 09-27 10:04，北京时间）。
 *
 * 列表里给人扫一眼「这条数据有多新」时用这个；要完整时刻用 formatDateTimeCN。
 * 时区显式取 Asia/Shanghai，与 formatDateTimeCN 口径一致 —— 后端统一存 UTC，
 * 直接按运行机器的本地时区渲染，在非中国时区的机器上会差 8 小时。
 */
export function formatShortCN(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  const parts = new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).formatToParts(date)
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  // zh-CN 在 hour12:false 下可能给出 '24' 表示午夜，归一成 '00'（同 formatDateTimeCN）。
  const hour = get('hour') === '24' ? '00' : get('hour')
  return `${get('month')}-${get('day')} ${hour}:${get('minute')}`
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
