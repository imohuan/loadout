package toolcallrepair

import (
	"bytes"
	"encoding/json"

	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// HandleProxyBeforeAttempt 每次渠道尝试前的 hook（proxy:before-attempt）：
// 按「模型 + 当前真实渠道」匹配 toolcall_repair 路由，命中 proxy 时对
// chat/completions 请求做「剥残缺声明 + 补全序列」；未命中则还原原始 body。
//
// 态度说明（哪些请求会跳过我、为什么）：
//   - 为什么不在入口（before-upstream）：那里 metadata 里没有真实渠道，
//     channel_ids 级规则永远匹配不上（key-rate-limit 踩过同一个坑）。本能力
//     需要按渠道开关，所以必须在 __current_channel 已写入的 before-attempt 阶段判断。
//   - 子请求豁免：__sub_request（视觉识别、MCP 识别等内部调用）跳过。它们是网关自己
//     拼的对话，历史由代码生成，天然成对；改写只会让子请求的日志与模型输入对不上。
//   - 非 chat/completions：OpenAI 工具调用协议只在这条路径上，/responses 等不适用。
//   - 未配置能力路由 / native：用户要求原样透传（有的上游自己能消化残缺历史）。
//   - 非 JSON / 无 messages：没东西可补，不改字节。
// 读表失败 fail-open，宁可透传也不凭空改写用户请求。
func (s *Service) HandleProxyBeforeAttempt(payload any) (any, error) {
	pipe, ok := payload.(*modelgateway.ProxyPipeline)
	if !ok || pipe == nil || pipe.Request == nil || len(pipe.Request.Body) == 0 {
		return payload, nil
	}
	if pipe.Request.Path != supportedPath {
		return payload, nil
	}
	if isSubRequest(pipe.Metadata) {
		return payload, nil
	}
	if !json.Valid(pipe.Request.Body) {
		return payload, nil
	}

	if pipe.Metadata == nil {
		pipe.Metadata = map[string]any{}
	}
	// body 归位：若上一次是本插件改写的、且之后没有别人再改，先撤销回输入快照，
	// 保证本轮判断始终基于「本插件接手时的原文」。若当前 body 已被别的插件改写
	// （当前 != 上次输出），说明那是本轮的新内容，原样作为输入，绝不覆盖。
	current := pipe.Request.Body
	inBody, _ := pipe.Metadata[inBodyMetaKey].([]byte)
	lastOut, _ := pipe.Metadata[outBodyMetaKey].([]byte)
	if bytes.Equal(current, lastOut) && len(inBody) > 0 {
		current = inBody
	}

	routes, err := s.decideRoutes(pipe, types.VirtualModelFromMetadata(pipe.Metadata))
	if err != nil || len(routes) == 0 || routes[0].Route != types.RouteProxy {
		// 未命中 / native：撤销本插件上一次的改写，按原生方式透传。
		delete(pipe.Metadata, inBodyMetaKey)
		delete(pipe.Metadata, outBodyMetaKey)
		pipe.Request.Body = current
		return pipe, nil
	}

	fixed, changed := apply(current)
	if !changed {
		delete(pipe.Metadata, inBodyMetaKey)
		delete(pipe.Metadata, outBodyMetaKey)
		pipe.Request.Body = current
		return pipe, nil
	}
	pipe.Request.Body = fixed
	pipe.Metadata[inBodyMetaKey] = append([]byte(nil), current...)
	pipe.Metadata[outBodyMetaKey] = append([]byte(nil), fixed...)
	channelID, _ := pipe.Metadata["__current_channel"].(string)
	s.lg.Info("toolcall-repair: 命中路由，已补全工具调用序列",
		"model", pipe.Request.Model, "path", pipe.Request.Path, "channel_id", channelID)
	return pipe, nil
}

// isSubRequest 判断是否为网关内部的子请求（与 model-gateway 同语义的 metadata 标记）。
func isSubRequest(md map[string]any) bool {
	if md == nil {
		return false
	}
	v, _ := md["__sub_request"].(bool)
	return v
}
