// 模型测试「快速模板」的 IndexedDB 封装：纯前端本地存储，零依赖。
// 存 draft 文本 + 附件（图片以 Blob 原样存储），回填时由调用方还原成 File。
// 浏览器原生支持在 IndexedDB 中存 Blob，无需转 base64（体积更小）。

const DB_NAME = 'loadout-model-test'
const STORE = 'templates'

export type TemplateAttachment = {
  name: string
  kind: 'image' | 'file'
  blob: Blob
}

export type TestTemplate = {
  id: string
  name: string
  text: string
  attachments: TemplateAttachment[]
  savedAt: number
}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1)
    request.onupgradeneeded = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: 'id' })
      }
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

/** 列出全部模板（按保存时间倒序） */
export async function listTemplates(): Promise<TestTemplate[]> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly')
    const req = tx.objectStore(STORE).getAll()
    req.onsuccess = () =>
      resolve((req.result as TestTemplate[]).sort((a, b) => b.savedAt - a.savedAt))
    req.onerror = () => reject(req.error)
  })
}

/** 保存 / 覆盖一个模板 */
export async function saveTemplate(template: TestTemplate): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).put(template)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

/** 按 id 删除模板 */
export async function removeTemplate(id: string): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).delete(id)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}
