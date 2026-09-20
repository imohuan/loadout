<script setup lang="ts">
// 模型/渠道健康状态徽标：把后端 health_status + failure_class + disabled_until
// 翻译成「人话」+ 语义色，避免用户只看到一个「已关闭」分不清原因。
//
// 状态优先级（从强到弱）：
//   手动关闭 > 规则禁用（永久） > 规则禁用（次日恢复） > 免费额度耗尽 >
//   鉴权失效 > 余额/额度 > 冷却中（含剩余时间） > 限速冷却 > 可用
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  RiForbidLine,
  RiTimeLine,
  RiErrorWarningLine,
  RiCheckboxCircleLine,
  RiKey2Line,
} from '@remixicon/vue'
import { HoverCard, HoverCardContent, HoverCardTrigger } from 'shadcn-vue-cdn'
import ErrorJsonPreview from '@/components/route-logs/ErrorJsonPreview.vue'

const router = useRouter()

const props = withDefaults(
  defineProps<{
    status?: string
    available?: boolean
    /** 手动开关（false = 用户主动关掉，与自动熔断区分） */
    manualEnabled?: boolean
    /** 失败分类：rate_limit / auth / model_quota / free_quota_exhausted / rule_<verdict>_<recover> … */
    failureClass?: string
    /** 冷却/禁用截止时间（ISO） */
    disabledUntil?: string
    /** 最近错误（tooltip 详情） */
    lastError?: string
    /** 命中并禁用该对象的失败规则（展示用名称 + 跳转用 id） */
    ruleId?: string
    ruleName?: string
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

// 失败规则引擎写入的分类名形如 rule_<verdict>_<recover>（如 rule_disable_key_daily）。
// 早期版本写的是裸 verdict（disable_key / cooldown），历史数据仍要能识别，
// 因此这里两种都解析；无法识别时返回 null，交回给下方的通用状态分支。
const RULE_VERDICTS = ["disable_provider", "disable_model", "disable_key", "cooldown", "disable"]

function parseRuleClass(cls: string): { verdict: string; recover: string } | null {
  if (!cls) return null
  let body = cls
  if (body.indexOf("rule_") === 0) {
    body = body.slice(5)
  } else if (!/^(disable_key|disable_model|disable_provider|cooldown)$/.test(body)) {
    // 裸值只接受规则引擎早期写过的 verdict，避免把 legacy classify 的分类误判为规则裁决。
    return null
  }
  for (const v of RULE_VERDICTS) {
    if (body === v) return { verdict: v, recover: "" }
    if (body.indexOf(v + "_") === 0) {
      const rest = body.slice(v.length + 1)
      return { verdict: v, recover: rest === "never" || rest === "daily" || rest === "fixed" ? rest : "" }
    }
  }
  return null
}

// legacyRecover 推断早期裸值分类（disable_key/cooldown）对应的恢复策略：
// 错误文案命中「额度用尽」类关键词时按日恢复，否则按有无截止时间区分定时/永久。
function legacyRecover(cls: string, lastError?: string, disabledUntil?: string): string {
  const msg = (lastError || "").toLowerCase()
  const dailyHint =
    msg.indexOf("14018") >= 0 ||
    msg.indexOf("额度已用尽") >= 0 ||
    msg.indexOf("额度用尽") >= 0 ||
    msg.indexOf("quota exhausted") >= 0 ||
    msg.indexOf("insufficient quota") >= 0
  if (dailyHint) return "daily"
  if (cls === "cooldown") return "fixed"
  return disabledUntil ? "fixed" : "never"
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

  // 3) 免费额度耗尽（独立额度池，按日恢复）
  if (cls === "free_quota_exhausted") {
    return {
      label: remain ? "额度用尽 · " + remain + "恢复" : "额度用尽 · 次日恢复",
      tone: "amber",
      icon: RiTimeLine,
      hint: "该账号今天的免费额度已用完，明天自动恢复；期间不会再被选中。",
    }
  }

  // 4) 失败规则引擎裁决写入的分类（rule_<verdict>_<recover>）。
  //    必须能区分「永久禁用 / 次日恢复的额度用尽 / 定时冷却」，否则不同成因
  //    会被一律渲染成「冷却中」，用户无法判断该等还是该换 Key。
  const rule = parseRuleClass(cls)
  if (rule) {
    // 早期落库的裸值（disable_key / cooldown）没有 recover 段：有 until 视为可自动
    // 恢复，否则需手动恢复。额度用尽类文案可进一步判定为「按日恢复」。
    const recover = rule.recover || legacyRecover(cls, props.lastError, props.disabledUntil)
    if (rule.verdict === "disable_provider") {
      return {
        label: "平台已禁用（需手动恢复）",
        tone: "red",
        icon: RiForbidLine,
        hint: "失败规则判定整个平台（同 base_url 的全部 Key）不可用，停止路由，需在「失败规则」页或此处手动恢复。",
      }
    }
    if (rule.verdict === "disable_model") {
      return {
        label:
          recover === "never"
            ? "模型不可用（需手动恢复）"
            : recover === "daily"
              ? remain
                ? "模型额度用尽 · " + remain + "恢复"
                : "模型额度用尽 · 次日恢复"
              : remain
                ? "该模型冷却 · " + remain + "恢复"
                : "该模型冷却（即将恢复）",
        tone: recover === "never" ? "red" : "amber",
        icon: recover === "never" ? RiForbidLine : RiTimeLine,
        hint:
          recover === "never"
            ? "失败规则判定该模型在当前 Key 上不可用（如模型不存在），需手动恢复。"
            : "失败规则判定该模型在当前 Key 上暂时不可用，到期自动恢复。",
      }
    }
    // disable_key + never：整条 Key（账号）停止路由。
    if (recover === "never") {
      return {
        label: "账号已禁用（需手动恢复）",
        tone: "red",
        icon: RiKey2Line,
        hint: "失败规则判定该 Key 不可用（如密钥无效、余额不足），已停止路由，需更换或手动恢复。",
      }
    }
    if (recover === "daily") {
      return {
        label: remain ? "额度用尽 · " + remain + "恢复" : "额度用尽 · 次日恢复",
        tone: "amber",
        icon: RiTimeLine,
        hint: "失败规则判定该账号今日额度已用完，到次个刷新点自动恢复；期间不参与路由。",
      }
    }
    // cooldown / disable_* + fixed：定时恢复
    return {
      label: remain ? "临时禁用 · " + remain + "恢复" : "临时禁用（即将恢复）",
      tone: "amber",
      icon: RiTimeLine,
      hint: "失败规则判定为临时禁用，到期自动恢复；期间不参与路由。",
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

// 后端的 last_error 常是「摘要 + 上游响应体」拼接的一行，例如：
//   上游返回错误(429) {"error":{"data":{"code":14018,"msg":"额度已用尽"}}}
// 直接塞进 tooltip 会挤成一坨。这里把前导摘要与 JSON 正文拆开，正文交给
// ErrorJsonPreview 做彩色高亮（与转发日志的错误悬浮卡一致）。
const errorParts = computed<{ summary: string; body: string }>(() => {
  const raw = (props.lastError || "").trim()
  if (!raw) return { summary: "", body: "" }
  const at = raw.search(/[[{]/)
  if (at < 0) return { summary: raw, body: "" }
  const head = raw.slice(0, at).trim()
  const tail = raw.slice(at).trim()
  // 仅当尾部确实是可解析的 JSON 时才当正文，否则整段按纯文本展示。
  try {
    JSON.parse(tail)
  } catch {
    return { summary: raw, body: "" }
  }
  return { summary: head, body: tail }
})

// 卡片里「复制」按钮复制的内容：优先完整错误原文（摘要 + 响应体）。
const copyText = computed(() => (props.lastError || "").trim())
const copied = ref(false)
async function copyError() {
  if (!copyText.value) return
  try {
    await navigator.clipboard.writeText(copyText.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    /* 非安全上下文等场景静默忽略 */
  }
}

// goToRule 跳到「失败规则」页并直接打开这条规则的编辑器，用户可当场改阈值/
// 恢复策略。规则页读 query.rule 自动展开（见 RulesView 的 openRuleFromQuery）。
function goToRule() {
  if (!props.ruleId) return
  router.push({ name: 'failure-rules', query: { rule: props.ruleId } })
}

// 规则名已知但 id 缺失（历史数据只落了分类，没落规则 id）：至少把用户送到规则页，
// 让他能按名称搜索并修改，而不是完全没有入口。
function goToRulesPage() {
  router.push({ name: 'failure-rules' })
}

// hasRuleRef 卡片是否要显示「命中规则」行：有 id 或名称都算。
const hasRuleRef = computed(() => Boolean(props.ruleId || props.ruleName))
</script>

<template>
  <!-- 状态徽标 + 悬停详情卡。
       卡片与「转发日志」的错误悬浮卡（RouteLogErrorCell）保持同一套观感：
       标题行 + 复制按钮 + 彩色 JSON 正文。Tooltip 只能显示一行小字，
       遇到 429/14018 这种带完整响应体的错误会挤成一坨，看不清也复制不了。 -->
  <HoverCard v-if="show" :open-delay="150" :close-delay="100">
    <HoverCardTrigger as-child>
      <Badge
        variant="outline"
        class="cursor-default gap-1 border text-[11px] font-medium"
        :class="TONE_CLASS[view.tone]"
      >
        <component :is="view.icon" size="12" class="shrink-0" />
        {{ view.label }}
      </Badge>
    </HoverCardTrigger>
    <HoverCardContent align="start" :side-offset="6" class="w-[min(420px,calc(100vw-2rem))] p-0">
      <div class="space-y-2 rounded-md border border-border/60 bg-muted/40 px-3 py-2">
        <div class="flex items-center justify-between gap-2">
          <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
            {{ view.label }}
          </span>
          <button
            v-if="copyText"
            type="button"
            class="rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
            @click.stop="copyError"
          >
            {{ copied ? '已复制' : '复制' }}
          </button>
        </div>
        <!-- 成因说明：告诉用户「为什么不可用 / 什么时候恢复」 -->
        <p class="text-xs leading-relaxed text-foreground/90">{{ view.hint }}</p>
        <!-- 命中规则：让用户知道「是哪条规则判的」，一键跳到规则页改它 -->
        <div
          v-if="hasRuleRef"
          class="flex items-center justify-between gap-2 rounded border border-border/60 bg-background/60 px-2 py-1"
        >
          <span class="min-w-0 flex-1 truncate text-[11px] text-foreground/80">
            命中规则：{{ ruleName || ruleId }}
          </span>
          <button
            type="button"
            class="shrink-0 rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
            @click.stop="ruleId ? goToRule() : goToRulesPage()"
          >
            查看规则
          </button>
        </div>
        <!-- 最近错误：摘要一行 + 响应体彩色预览 -->
        <div v-if="errorParts.summary || errorParts.body" class="border-t border-border/60 pt-2">
          <p
            v-if="errorParts.summary"
            class="mb-1 font-mono text-[11px] leading-snug text-muted-foreground break-all"
          >
            {{ errorParts.summary }}
          </p>
          <ErrorJsonPreview
            v-if="errorParts.body"
            :body="errorParts.body"
            :compact="true"
            max-height-class="max-h-64"
          />
        </div>
      </div>
    </HoverCardContent>
  </HoverCard>
</template>
