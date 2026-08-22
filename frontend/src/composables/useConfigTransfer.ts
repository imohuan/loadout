import { emitter } from '@/lib/emitter'

// 配置导入导出（设置页「导出配置 / 导入配置」）。
// 后端：POST /api/config/export | /api/config/import/preview | /api/config/import

export type TransferSectionKey =
  'channels' | 'aggregates' | 'capability_routes' | 'mcp' | 'skills' | 'other'

export type ImportMode = 'overwrite' | 'append'

// 导出可选配置项（与后端 transferSections 顺序一致）。
export const transferSectionOptions: Array<{
  key: TransferSectionKey
  label: string
  description: string
}> = [
  { key: 'channels', label: '渠道配置', description: '上游渠道、模型清单与 API 密钥' },
  { key: 'aggregates', label: '聚合模型配置', description: '聚合模型与路由目标' },
  { key: 'capability_routes', label: '能力路由配置', description: '能力 → 模型 → 渠道路由规则' },
  { key: 'mcp', label: 'MCP 配置', description: 'MCP 服务器、分组与工具开关' },
  {
    key: 'skills',
    label: 'Skills 配置',
    description: '技能实际文件、预设与运行时设置',
  },
  {
    key: 'other',
    label: '其他',
    description: '火山引擎免费额度等杂项配置',
  },
]

export interface ImportPreviewSection {
  key: string
  name: string
  file: string
  count: number
}

export interface ImportPreview {
  valid: boolean
  format?: string
  version?: number
  exported_at?: string
  sections: ImportPreviewSection[]
  unknown?: string[]
}

export interface ImportSectionResult {
  key: string
  name: string
  mode: ImportMode
  count: number
  imported: number
  skipped?: string[]
  errors?: string[]
}

export interface ImportResult {
  format?: string
  version?: number
  results: ImportSectionResult[]
}

/** 下载导出 zip：后端返回二进制流，浏览器触发下载。 */
export async function exportConfig(sections: TransferSectionKey[]): Promise<void> {
  const response = await fetch('/api/config/export', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sections }),
  })
  if (response.status === 401) emitter.emit('unauthorized')
  if (!response.ok) {
    let message = `导出失败（${response.status}）`
    try {
      const body = await response.json()
      message = body.error?.message || body.message || message
    } catch {
      // 保持默认消息
    }
    throw new Error(message)
  }
  const blob = await response.blob()
  const disposition = response.headers.get('Content-Disposition') || ''
  const match = /filename="?([^"]+)"?/.exec(disposition)
  const filename = match?.[1] || `loadout-config-${timestamp()}.zip`
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

/** 解析导入 zip，返回包内配置摘要。 */
export async function previewImport(file: File): Promise<ImportPreview> {
  const body = new FormData()
  body.set('file', file)
  const response = await fetch('/api/config/import/preview', {
    method: 'POST',
    credentials: 'same-origin',
    body,
  })
  const data = await response.json().catch(() => ({}))
  if (response.status === 401) emitter.emit('unauthorized')
  if (!response.ok) {
    throw new Error(data.error?.message || data.message || `解析失败（${response.status}）`)
  }
  return data as ImportPreview
}

/** 执行导入：每个配置可独立选择 overwrite（覆盖）/ append（追加）。 */
export async function importConfig(
  file: File,
  modes: Record<string, ImportMode>,
): Promise<ImportResult> {
  const body = new FormData()
  body.set('file', file)
  body.set('modes', JSON.stringify(modes))
  const response = await fetch('/api/config/import', {
    method: 'POST',
    credentials: 'same-origin',
    body,
  })
  const data = await response.json().catch(() => ({}))
  if (response.status === 401) emitter.emit('unauthorized')
  if (!response.ok) {
    throw new Error(data.error?.message || data.message || `导入失败（${response.status}）`)
  }
  return data as ImportResult
}

function timestamp(): string {
  const now = new Date()
  const pad = (v: number) => String(v).padStart(2, '0')
  return (
    `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-` +
    `${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`
  )
}
