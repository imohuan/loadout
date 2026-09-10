import type { SkillTreeEntry } from '@/lib/types'

/** 技能文件树节点：目录带 children，文件 children 为空数组 */
export interface SkillTreeNode {
  path: string
  name: string
  dir: boolean
  size: number
  children: SkillTreeNode[]
}

/**
 * 把后端返回的扁平条目（相对路径）还原成树。
 * 父目录缺失时（理论上不会）按路径逐级补出目录节点，保证不漏文件。
 */
export function buildSkillTree(entries: SkillTreeEntry[]): SkillTreeNode[] {
  const nodes = new Map<string, SkillTreeNode>()

  function ensureDir(path: string, name: string): SkillTreeNode {
    const existing = nodes.get(path)
    if (existing) return existing
    const created: SkillTreeNode = { path, name, dir: true, size: 0, children: [] }
    nodes.set(path, created)
    const slash = path.lastIndexOf('/')
    if (slash > 0) {
      const parent = ensureDir(path.slice(0, slash), path.slice(slash + 1))
      parent.children.push(created)
    }
    return created
  }

  for (const entry of entries) {
    if (!entry.path) continue
    if (entry.dir) {
      ensureDir(entry.path, entry.name)
      continue
    }
    const node: SkillTreeNode = {
      path: entry.path,
      name: entry.name,
      dir: false,
      size: entry.size,
      children: [],
    }
    nodes.set(entry.path, node)
    const slash = entry.path.lastIndexOf('/')
    if (slash > 0) {
      ensureDir(entry.path.slice(0, slash), entry.name).children.push(node)
    }
  }

  // 顶层节点 = 路径里不含 "/" 的条目，顺序沿用后端给的排序。
  const roots: SkillTreeNode[] = []
  for (const entry of entries) {
    if (!entry.path || entry.path.includes('/')) continue
    const node = nodes.get(entry.path)
    if (node) roots.push(node)
  }
  return roots
}

/** 默认展开的目录：每个一级目录下的目录链（让第一层子目录立即可见） */
export function defaultExpandedPaths(roots: SkillTreeNode[]): string[] {
  const out: string[] = []
  function walk(node: SkillTreeNode, opened: boolean) {
    if (!node.dir) return
    if (opened) out.push(node.path)
    for (const child of node.children) walk(child, opened)
  }
  for (const root of roots) walk(root, true)
  return out
}
