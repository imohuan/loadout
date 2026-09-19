<script setup lang="ts">
// 模型/渠道健康状态徽标：把后端 health_status + failure_class + disabled_until
// 翻译成「人话」+ 语义色，避免用户只看到一个「已关闭」分不清原因。
//
// 状态优先级（从强到弱）：
//   手动关闭 > 规则禁用（永久） > 规则禁用（次日恢复） > 免费额度耗尽 >
//   鉴权失效 > 余额/额度 > 冷却中（含剩余时间） > 限速冷却 > 可用
import { computed } from 'vue'
import {
  RiForbidLine,
  RiTimeLine,
  RiErrorWarningLine,
  RiCheckboxCircleLine,
  RiKey2Line,
} from '@remixicon/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from 'shadcn-vue-cdn'

const props = withDefaults(
  defineProps<{
    status?: string
    available?: boolean
    /** 手动开关（false = 用户主动关掉，与自动熔断区分） */
    manualEnabled?: boolean
    /** 失败分类：rate_limit / auth / model_quota / free_quota_exhausted / rule_disable* … */
    failureClass?: string
    /** 冷却/禁用截止时间（ISO） */
    disabledUntil?: string
    /** 最近错误（tooltip 详情） */
    lastError?: string
    /** available 时是否隐藏（密集列表用） */
    hideWhenAvailable?: boolean
  }>(),
  { available: true, manualEnabled: true, hideWhenAvailable: false },
)

type Tone = 'red' | 'amber' | 'blue' | 'slate' | 'emerald' | 'violet'

const TONE_CLASS: Record<Tone, string> = {
  red: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  amber: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  blue: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  slate: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
  emerald: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  violet: 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20',
}

// untilText 剩余时间：>1h 显示「N 小时后」，否则「N 分钟后」。
function untilText(iso?: string): string {
  if (!iso) return ""
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ""
  const diff = t - Date.now()
  if (diff <= 0) return "即将恢复"
  const mins = Math.round(diff / 60000)
  if (mins < 60) return mins + " 分钟后"
  const hours = Math.round(mins / 60)
  if (hours < 24) return hours + " 小时后"
  return Math.round(hours / 24) + " 天后"
}

const view = computed<{ label: string; tone: Tone; icon: unknown; hint: string }>(() => {
  const cls = props.failureClass || ""

  // 1) 用户主动关闭（最高优先级）
  if (!props.manualEnabled) {
    return {
      label: "手动关闭",
      tone: "slate",
      icon: RiForbidLine,
      hint: "该对象被你手动关闭，不参与路由；打开「手动启用」即可恢复。",
    }
  }

  // 2) 可用
  if (props.available !== false && props.status === "available") {
    return { label: "可用", tone: "emerald", icon: RiCheckboxCircleLine, hint: "当前可正常参与路由。" }
  }

  const remain = untilText(props.disabledUntil)

  // 3) 免费额度耗尽（按日恢复）
  if (cls === "free_quota_exhausted") {
    return {
      label: remain ? "额度用尽 · " + remain + "恢复" : "额度用尽 · 次日恢复",
      tone: "amber",
      icon: RiTimeLine,
      hint: "该账号今天的免费额度已用完，明天自动恢复；期间不会再被选中。",
    }
  }

  // 4) 规则引擎裁决写入的禁用
  if (cls.indexOf("rule_disable") === 0) {
    const permanent = !props.disabledUntil
    return {
      label: permanent ? "规则禁用（需手动恢复）" : "规则禁用 · " + remain,
      tone: permanent ? "red" : "amber",
      icon: permanent ? RiForbidLine : RiTimeLine,
      hint: permanent
        ? "失败规则判定为不可恢复（如密钥无效、余额不足），需在「失败规则」页或此处手动恢复。"
        : "失败规则判定为临时禁用，到期自动恢复；期间不参与路由。",
    }
  }

  // 5) 鉴权失效
  if (cls === "auth") {
    return {
      label: "密钥失效",
      tone: "red",
      icon: RiKey2Line,
      hint: "上游返回鉴权失败（密钥过期/被删/无权限），需要更换该 Key。",
    }
  }

  // 6) 余额 / 额度类
  if (cls === "model_quota" || cls === "channel_billing") {
    return {
      label: "余额/额度不足",
      tone: "red",
      icon: RiErrorWarningLine,
      hint: "上游返回余额或额度不足，需要充值或更换 Key。",
    }
  }

  // 7) 限速冷却
  if (cls === "rate_limit") {
    return {
      label: remain ? "限速冷却 · " + remain : "限速冷却",
      tone: "blue",
      icon: RiTimeLine,
      hint: "上游返回限速，短暂冷却后自动重试。",
    }
  }

  // 8) 其它自动熔断
  if (props.status === "cooling") {
    return {
      label: remain ? "冷却中 · " + remain : "冷却中",
      tone: "blue",
      icon: RiTimeLine,
      hint: "连续失败后进入冷却，到期自动恢复。",
    }
  }
  if (props.status === "disabled") {
    return {
      label: "已禁用",
      tone: "red",
      icon: RiForbidLine,
      hint: "自动健康检查判定不可用，需要在页面内手动恢复。",
    }
  }

  return { label: "未知", tone: "slate", icon: RiErrorWarningLine, hint: "状态未知。" }
})

const show = computed(
  () => !(props.hideWhenAvailable && props.available !== false && props.status === "available"),
)
</script>

<template>
  <Tooltip v-if="show">
    <TooltipTrigger as-child>
      <Badge variant="outline" class="gap-1 border text-[11px] font-medium" :class="TONE_CLASS[view.tone]">
        <component :is="view.icon" size="12" class="shrink-0" />
        {{ view.label }}
      </Badge>
    </TooltipTrigger>
    <TooltipContent class="max-w-sm space-y-1 whitespace-normal">
      <p>{{ view.hint }}</p>
      <p v-if="lastError" class="text-muted-foreground break-words">{{ lastError }}</p>
    </TooltipContent>
  </Tooltip>
</template>
