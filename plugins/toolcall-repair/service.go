// Package toolcallrepair 实现「工具调用序列补全」能力适配器（plugins/toolcall-repair）。
//
// 订阅 model-gateway 的 proxy:before-attempt（每次渠道尝试前）waterfall 事件，
// 按「模型 + 当前真实渠道」匹配能力路由表（capability_routes，capability="toolcall_repair"）
// 决定是否改写请求：
//   - 命中且 route=proxy：清理残缺 assistant 声明 + 补全不完整的工具调用序列；
//   - 未命中 / route=native：请求体零改动，字节级透传。
//
// 为什么做成能力路由而不是网关内置：不同上游对残缺历史的容忍度不同。有的上游
// （实测 step-5-preview / 火山方舟）会直接 400；也有的上游能自己消化「只有 tool 结果、
// 没有对应 assistant 声明」的历史（实测 step-5-preview 原样请求返回 200）。默认改写会
// 改变客户端语义、也让日志难以解释，所以交给用户按模型/渠道显式开启。
package toolcallrepair

import (
	"bytes"
	"encoding/json"
	"strings"
)

// capabilityName 能力路由表中本能力的固定名称。
const capabilityName = "toolcall_repair"

// supportedPath 只对 OpenAI chat/completions（工具调用协议）生效。
const supportedPath = "chat/completions"

// 两个 metadata 键配对记录我们自己的改写，用于「撤销上一次改写」：
//   - inBodyMetaKey：上次本插件看到的输入 body；
//   - outBodyMetaKey：上次本插件写出的 body。
// 下次执行时只有「当前 body 仍等于 out」才回滚到 in——若期间别的插件（force_stream /
// message_inject 等）重写过 body，说明当前 body 已是本轮的权威内容，绝不能拿旧快照
// 覆盖它（否则会把别的插件本轮的改写冲掉）。
const (
	inBodyMetaKey  = "__toolcall_repair_in_body"
	outBodyMetaKey = "__toolcall_repair_out_body"
)

// apply 对一份 chat/completions body 执行「先剥残缺声明、再补全序列」。
// 返回 (改写后 body, 是否修改)。非 JSON 对象 / 无 messages / 无需要修复之处时原样返回。
func apply(body []byte) ([]byte, bool) {
	content := body
	if bytes.HasPrefix(content, []byte("\xEF\xBB\xBF")) {
		content = content[3:]
	}
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return body, false
	}
	messagesRaw, ok := root["messages"]
	if !ok {
		return body, false
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return body, false
	}

	cleaned, changed := stripBrokenAssistantMessagesSlice(messages)
	repaired, repairChanged := repairToolCallSequenceMessages(cleaned)
	if !changed && !repairChanged {
		return body, false
	}
	repairedJSON, err := json.Marshal(repaired)
	if err != nil {
		return body, false
	}
	root["messages"] = repairedJSON
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

// isEmptyContent 判断消息 content 是否为空：null / 空字符串 / 纯空白都算空。
// 分段数组只要有元素就视为非空（保留消息）。
func isEmptyContent(raw json.RawMessage) bool {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return true
	}
	if s[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return false // 解析失败按非空处理
		}
		return strings.TrimSpace(text) == ""
	}
	return false // 数组/对象一律视为非空
}
