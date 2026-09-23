package failurerules

import (
	"regexp"
	"strings"
)

// captureFromConditions 取条件组里**第一个命中正则**的捕获组。
//
// any：第一个命中的正则；all：顺序扫描第一个命中的正则。
// 捕获来自该正则最近一次（也就是这次）求值。
func captureFromConditions(conds []compiledCond, ev Evidence) []string {
	for i := range conds {
		cc := &conds[i]
		if cc.regex == nil {
			continue
		}
		m := cc.regex.FindStringSubmatch(ev.Message)
		if m != nil {
			return m
		}
	}
	return nil
}

// captureGroups 导出用：对给定文本跑正则并返回捕获组（测试/调试用）。
func captureGroups(re *regexp.Regexp, text string) []string {
	return re.FindStringSubmatch(text)
}

// expandTemplate 把模板里的 $0/$1/… 展开成捕获值。
//
// $0 = 整个匹配，$1.. = 第 N 个捕获组；引用不存在的组展开为空串。
// 支持 $$ 转义为字面 $（需要输出美元符号时用）。
func expandTemplate(tpl string, captures []string) string {
	if tpl == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if c != '$' {
			b.WriteByte(c)
			continue
		}
		// $ 后面跟数字 → 组引用；$$ → 字面 $。
		if i+1 < len(tpl) && tpl[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		j := i + 1
		numEnd := j
		for numEnd < len(tpl) && tpl[numEnd] >= '0' && tpl[numEnd] <= '9' {
			numEnd++
		}
		if numEnd == j {
			// 不是 $数字，按字面输出。
			b.WriteByte(c)
			continue
		}
		n := 0
		for k := j; k < numEnd; k++ {
			n = n*10 + int(tpl[k]-'0')
		}
		if n < len(captures) {
			b.WriteString(captures[n])
		}
		i = numEnd - 1
	}
	return b.String()
}
