// ANSI 颜色码 → HTML 工具（从 SkillsView 抽出，供技能更新日志 / UnifyAI 执行日志等共用）。
// 支持 16 色 + 256 色 + 重置/加粗/斜体/下划线；非 SGR 的 CSI 序列（光标/擦除/私有模式）剥掉。

const FG16: Record<number, string> = {
  30: '#000',
  31: '#c00',
  32: '#0a0',
  33: '#aa0',
  34: '#00c',
  35: '#c0c',
  36: '#0aa',
  37: '#bbb',
  90: '#555',
  91: '#f55',
  92: '#5f5',
  93: '#ff5',
  94: '#55f',
  95: '#f5f',
  96: '#5ff',
  97: '#fff',
}

function ansi256(n: number): string {
  if (n < 16) {
    return FG16[n < 8 ? 30 + n : 90 + (n - 8)] || '#fff'
  }
  if (n >= 232) {
    const g = (n - 232) * 10 + 8
    return `rgb(${g},${g},${g})`
  }
  const c = n - 16
  const r = Math.floor(c / 36)
  const g = Math.floor((c % 36) / 6)
  const b = c % 6
  const v = (lv: number) => (lv === 0 ? 0 : 55 * lv + 40)
  return `rgb(${v(r)},${v(g)},${v(b)})`
}

/** ANSI 转义文本 → 安全 HTML（已 HTML escape + SGR → span）。 */
export function ansiToHtml(s: string): string {
  // 1) 去掉 OSC 序列（标题/颜色等，...\u0007 结尾）。
  let out = s.replace(/\u001b\][\s\S]*?(?:\u0007|\u001b\\)/g, '')
  // 2) 去掉行内 \r（终端用 CR 覆盖行，进度条等场景）。
  out = out.replace(/\r+/g, '')
  // 3) HTML escape。
  out = out.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  // 4) 统一处理 CSI 序列：SGR (m) → span；其他（光标移动/擦除/私有模式等）→ 剥掉。
  out = out.replace(/\u001b\[([\d;?]*)([\x40-\x7e])/g, (_, params: string, final: string) => {
    if (final !== 'm') {
      return '' // 非 SGR 全部剥掉（光标/清除/私有模式）
    }
    const tokens = params
      .split(';')
      .filter((c) => c !== '')
      .map(Number)
    if (tokens.includes(0)) {
      return '</span>'
    }
    const styles: string[] = []
    for (let i = 0; i < tokens.length; i++) {
      const c = tokens[i]
      if (c === 1) styles.push('font-weight:bold')
      else if (c === 2) styles.push('font-weight:normal')
      else if (c === 3) styles.push('font-style:italic')
      else if (c === 4) styles.push('text-decoration:underline')
      else if (c === 38 && tokens[i + 1] === 5 && i + 2 < tokens.length) {
        styles.push(`color:${ansi256(tokens[i + 2])}`)
        i += 2
      } else if (c === 48 && tokens[i + 1] === 5 && i + 2 < tokens.length) {
        styles.push(`background-color:${ansi256(tokens[i + 2])}`)
        i += 2
      } else if (FG16[c]) styles.push(`color:${FG16[c]}`)
    }
    if (!styles.length) return ''
    return `<span style="${styles.join(';')}">`
  })
  return out
}
