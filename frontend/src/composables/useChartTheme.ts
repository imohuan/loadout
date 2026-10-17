import { computed } from 'vue'
import { useDark } from '@vueuse/core'

/**
 * 图表的明暗配色。
 *
 * ECharts 画在 canvas 上，拿不到 Tailwind 的 dark: 变体，也不读 CSS 变量；
 * 所以每个图表都要自己按当前主题取色。之前这些颜色被写死成浅色（如
 * splitLine #e2e8f0、饼图白描边），暗色主题下就是一条条刺眼的白线。
 */
const LIGHT = {
  /** 坐标轴数字 / 图例文字 */
  text: '#71717a',
  /** 坐标轴线 */
  axis: '#d4d4d8',
  /** 网格分割线：浅灰，不能抢过数据本身 */
  split: '#e4e4e7',
  /** 图表所在卡片背景（饼图描边用它，形成视觉间隙） */
  surface: '#ffffff',
  /** 悬浮提示框 */
  tooltipBg: '#ffffff',
  tooltipBorder: '#e4e4e7',
  tooltipText: '#3f3f46',
  /** 饼图标签文字 */
  label: '#3f3f46',
  labelLine: '#a1a1aa',
  /** 空态/占位 */
  empty: '#e4e4e7',
}

const DARK = {
  text: '#a1a1aa',
  axis: '#3f3f46',
  // 暗色下的分割线只比背景亮一点，避免整张图被白线切碎
  split: '#323236',
  surface: '#1c1c1f',
  tooltipBg: '#27272a',
  tooltipBorder: '#3f3f46',
  tooltipText: '#e4e4e7',
  label: '#d4d4d8',
  labelLine: '#71717a',
  empty: '#3f3f46',
}

export function useChartTheme() {
  const isDark = useDark()
  const colors = computed(() => (isDark.value ? DARK : LIGHT))
  return { isDark, colors }
}

/** 悬浮提示框的通用样式：默认是白底黑字，暗色下必须改成深色 */
export function tooltipStyle(colors: (typeof LIGHT) | (typeof DARK)) {
  return {
    backgroundColor: colors.tooltipBg,
    borderColor: colors.tooltipBorder,
    borderWidth: 1,
    textStyle: { color: colors.tooltipText, fontSize: 12 },
  }
}
