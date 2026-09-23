import assert from 'node:assert/strict'
import test from 'node:test'
import { computeColumnWidths, DEFAULT_MIN_WIDTH, parseCssLength } from '../src/lib/tableWidth.ts'

test('上限以内的列收缩到内容自然宽度（上限生效，不被撑大）', () => {
  // 真实 Chrome 实测：auto + width:100% 下 max-width 只是偏好，320 的列被撑到 696。
  // 这里断言引擎给出的是 320，而不是被富余空间撑大。
  const natural = [155, 93, 107, 159, 59, 566, 185, 73]
  const caps = [null, null, null, null, null, 320, null, null]
  const widths = computeColumnWidths(
    natural.map((w, i) => ({ natural: w, max: caps[i] ?? undefined })),
    { containerWidth: 1200 },
  )
  assert.equal(widths[5], 320, '封顶列必须等于上限')
})

test('没有上限的列吸收富余空间，有上限的列不被撑过上限', () => {
  const widths = computeColumnWidths(
    [{ natural: 100, max: 150 }, { natural: 120, max: 150 }, { natural: 400 }],
    { containerWidth: 1000 },
  )
  assert.equal(widths[0], 100, '未触顶的封顶列保持自然宽度（上限不是目标）')
  assert.equal(widths[1], 120, '未触顶的封顶列保持自然宽度')
  assert.equal(widths[2], 780, '无上限列吸收全部富余')
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    1000,
    '总和铺满容器',
  )
})

test('总和恰好铺满容器，避免 fixed 布局二次放大列宽', () => {
  const columns = [{ natural: 155.4 }, { natural: 93.6 }, { natural: 107.2, max: 320 }]
  const widths = computeColumnWidths(columns, { containerWidth: 900 })
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    900,
  )
})

test('压缩时绝不低于调用方声明的下限', () => {
  // 真实踩过的两个坑：① 按比例平摊把所有列都挤窄；② 无上限的列完全不压，
  // 于是一列 495 字的长文本把整张表撑到 5703px。现在的策略是只压最宽的列。
  const widths = computeColumnWidths(
    [
      { natural: 200, min: 150 },
      { natural: 300, min: 250 },
      { natural: 120, min: 56 },
    ],
    { containerWidth: 400 },
  )
  assert.ok(widths[0] >= 150, `第 1 列不得低于声明的下限，实际=${widths[0]}`)
  assert.ok(widths[1] >= 250, `第 2 列不得低于声明的下限，实际=${widths[1]}`)
  assert.ok(widths[2] >= 56, `第 3 列不得低于兜底下限，实际=${widths[2]}`)
})

test('小幅超出时温和收缩可换行列，避免难看的滚动条', () => {
  // 差 33px 的小幅超出（真实页面踩到）：可换行列最多压掉 25%，应能吸掉滚动条。
  const widths = computeColumnWidths([{ natural: 600, max: 600, min: 100 }, { natural: 584 }], {
    containerWidth: 1151,
  })
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    1151,
    '温和收缩后应恰好铺满，无滚动条',
  )
  assert.ok(widths[0] >= 450, '可换行列最多压掉自身宽度的 25%')
})

test('所有列都触底仍放不下时，保持现状交给外层横向滚动', () => {
  // 每列都有很高下限，压到下限总和仍大于容器 → 不再硬压，滚动。
  const widths = computeColumnWidths(
    [
      { natural: 600, min: 500 },
      { natural: 900, min: 800 },
    ],
    { containerWidth: 600 },
  )
  assert.equal(widths[0], 500, '压到下限为止')
  assert.equal(widths[1], 800, '压到下限为止')
  assert.ok(widths.reduce((a, b) => a + b, 0) > 600, '超出部分交给外层滚动')
})

test('宽表格保持横向滚动，绝不为了塞进容器把内容压没', () => {
  const columns = Array.from({ length: 10 }, () => ({ natural: 400, min: 100, max: 320 }))
  const widths = computeColumnWidths(columns, { containerWidth: 500 })
  assert.ok(
    widths.every((w) => w >= 100),
    `任何列都不得低于下限，实际=${JSON.stringify(widths)}`,
  )
})

test('不可换行的列向上取整，避免少 1px 裁掉最后一个字', () => {
  const widths = computeColumnWidths(
    [{ natural: 100.2 }, { natural: 100.2, max: 200 }],
    // fill:false —— 隔离「取整」这一件事，否则无上限的列会吸收容器富余。
    { containerWidth: 400, fill: false },
  )
  assert.equal(widths[0], 101, '不可换行列必须向上取整')
})

test('超宽时压缩最宽的那一列，其余列保持内容宽度', () => {
  const widths = computeColumnWidths([{ natural: 400, min: 80 }, { natural: 200 }], {
    containerWidth: 300,
  })
  // 最宽的 400 那列被压到 100（300-200），另一列不动。
  assert.equal(widths[0], 100, '最宽列被压缩到刚好放得下')
  assert.equal(widths[1], 200, '其余列保持内容宽度')
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    300,
  )
})

test('下限生效：内容很短的列不会被压到看不见', () => {
  const widths = computeColumnWidths([{ natural: 10 }, { natural: 2000 }], {
    containerWidth: 300,
  })
  assert.equal(widths[0], DEFAULT_MIN_WIDTH)
})

test('拖拽钉住的列不被自动计算覆盖，其他列重新分配', () => {
  const widths = computeColumnWidths(
    [{ natural: 100, max: 150 }, { natural: 200, pinned: 520 }, { natural: 150 }],
    { containerWidth: 1000 },
  )
  assert.equal(widths[1], 520, '钉住列必须保持用户拖拽的宽度')
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    1000,
    '其余列吃掉宽度变化后仍铺满',
  )
})

test('钉住宽度同样受下限和拖拽上限约束', () => {
  // 可换行的列（声明了 max）被钉到低于下限时，抬到下限。
  const tiny = computeColumnWidths([{ natural: 100, min: 80, max: 160, pinned: 10 }], {
    containerWidth: 400,
    fill: false,
  })
  assert.equal(tiny[0], 80, '低于下限的钉住值被抬到下限')
  const huge = computeColumnWidths([{ natural: 100, pinned: 99999 }], {
    containerWidth: 400,
    fill: false,
    dragCeiling: 800,
  })
  assert.equal(huge[0], 800, '超过拖拽上限的值被压到上限')
})

test('fill=false 时不做铺满，只按内容给宽', () => {
  const widths = computeColumnWidths([{ natural: 120 }, { natural: 80 }], {
    containerWidth: 1000,
    fill: false,
  })
  assert.deepEqual(widths, [120, 80])
})

test('全列封顶且内容很窄时仍铺满容器（不隐藏内容，只是变宽）', () => {
  const widths = computeColumnWidths(
    [
      { natural: 100, max: 120 },
      { natural: 100, max: 120 },
    ],
    { containerWidth: 600 },
  )
  assert.equal(
    widths.reduce((a, b) => a + b, 0),
    600,
  )
})

test('空列返回空数组', () => {
  assert.deepEqual(computeColumnWidths([], { containerWidth: 500 }), [])
})

test('异常输入（NaN / 负数 / 容器为 0）不产生 NaN 或负宽度', () => {
  const widths = computeColumnWidths(
    [{ natural: Number.NaN }, { natural: -50 }, { natural: 100, max: -10 }],
    { containerWidth: Number.NaN },
  )
  for (const w of widths) {
    assert.ok(Number.isFinite(w) && w >= 0, `宽度必须是非负有限数，实际=${w}`)
  }
})

test('parseCssLength 解析表头上的 Tailwind 宽度类', () => {
  assert.equal(parseCssLength('320px'), 320)
  assert.equal(parseCssLength('240px'), 240)
  assert.equal(parseCssLength('12rem'), 192)
  assert.equal(parseCssLength('none'), undefined)
  assert.equal(parseCssLength('auto'), undefined)
  assert.equal(parseCssLength('50%'), undefined)
  assert.equal(parseCssLength(''), undefined)
  assert.equal(parseCssLength(undefined), undefined)
  assert.equal(parseCssLength('0px'), undefined)
})
