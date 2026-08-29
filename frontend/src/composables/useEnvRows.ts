// ============================================================================
// 键值行（env / header）增删逻辑 —— 供 MCP 相关面板复用。
// McpPanel 与 UnifyaiPanel 过去各自内联了一份 add/remove，这里统一收口。
// ============================================================================

export interface KeyValueRow {
  key: string
  value: string
}

/** 在行尾追加一个空行。 */
export function addRow(rows: KeyValueRow[]) {
  rows.push({ key: '', value: '' })
}

/** 删除指定下标的一行。 */
export function removeRow(rows: KeyValueRow[], index: number) {
  rows.splice(index, 1)
}
