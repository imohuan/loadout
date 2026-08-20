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
