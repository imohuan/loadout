// 临时自动化验证：驱动真实浏览器 hover 三种失败形态，断言都能弹出详情卡片。
// 用法：node frontend/__hovercheck__/check.mjs http://localhost:5391
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'

const require = createRequire(import.meta.url)
const playwrightPath = path.join(
  os.tmpdir(),
  'loadout-hovercheck',
  'node_modules',
  'playwright-core',
)
const { chromium } = require(playwrightPath)

const url = process.argv[2] || 'http://localhost:5391/'
const browser = await chromium.launch({
  channel: 'chrome',
  headless: true,
})
const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
const errors = []
page.on('pageerror', (e) => errors.push(String(e)))
page.on('console', (m) => {
  if (m.type() === 'error') errors.push(m.text())
})

await page.goto(url, { waitUntil: 'networkidle' })

// 卡片标题只在悬浮卡里出现，用它当「卡片是否弹出」的信号；
// 正文用每种形态独有的字符串断言「内容确实是这一条的完整详情」。
const LABEL = '上游原始响应'
const cases = [
  { id: 'json', body: '购买加量包' },
  { id: 'message-only', body: '模型免费额度用完冷却中' },
  { id: 'plain', body: 'context deadline exceeded' },
]

// 打开页面后、hover 之前，页面里不应有任何卡片标题
const before = (await page.locator(`text=${LABEL}`).count())
console.log(`初始卡片数（应为 0）: ${before}`)

let failed = before === 0 ? 0 : 1
for (const { id, body } of cases) {
  await page.mouse.move(0, 0)
  await page.waitForTimeout(300)
  const cell = page.locator(`[data-case="${id}"] span.underline`)
  await cell.hover()
  let text = ''
  try {
    const card = page
      .locator(`text=${LABEL}`)
      .first()
      .locator('xpath=ancestor::*[3]')
    await card.waitFor({ state: 'visible', timeout: 3000 })
    text = (await page.locator('body').innerText()).replace(/\s+/g, ' ')
  } catch {
    text = ''
  }
  const hasCard = text.includes(LABEL)
  const hasBody = text.includes(body)
  const ok = hasCard && hasBody
  if (!ok) failed++
  console.log(
    `${ok ? 'PASS' : 'FAIL'} [${id}] 卡片可见=${hasCard} 完整正文=${hasBody}`,
  )
}

if (errors.length) console.log('页面错误:', errors.slice(0, 5))
await browser.close()
process.exit(failed === 0 && errors.length === 0 ? 0 : 1)
