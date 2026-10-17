package requestlog

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// bytesReader bytes.NewReader 别名（避免 import 别名噪音）。
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// requestManifest 单条日志的清单（纸条）：只存元数据与块编号（内容哈希），
// 不存正文。正文全部在 blobs/ 里按哈希去重共享。
//
// 兼容约定：Detail 组装时把块还原成与旧版完全相同的 request_json /
// response_json 结构（requestSnapshot / responseSnapshot），前端与 API 消费方
// 零改动。
type requestManifest struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	Channel   string `json:"channel,omitempty"`
	Stream    bool   `json:"stream"`
	StartedAt string `json:"started_at"`

	// HTTP 元数据（收尾时回填）
	HTTPStatus int    `json:"http_status,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Result     string `json:"result"`

	// 请求侧块引用：
	//   Messages 逐条消息的块哈希（按对话顺序）；
	//   ReqExtra 请求体去掉 messages 后的剩余部分（参数/工具定义等）；
	//   ReqRaw   消息不可拆（非 JSON / 无 messages）时整个请求体一个块，其余为空。
	Messages []string `json:"messages,omitempty"`
	ReqExtra string   `json:"req_extra,omitempty"`
	ReqRaw   string   `json:"req_raw,omitempty"`

	// 响应块引用（收尾时写入）
	Resp string `json:"response,omitempty"`

	// Envelope 请求包装元数据块（method/path/headers），组装详情时还原。
	Envelope string `json:"envelope,omitempty"`
}

// messageEntry 拆块后的一条消息编号。
type messageEntry struct {
	Role string `json:"role"`
	Hash string `json:"hash"`
}

// referencedHashes 清单引用的全部块哈希（GC 用）。
func (m *requestManifest) referencedHashes() []string {
	var out []string
	out = append(out, m.Messages...)
	if m.ReqExtra != "" {
		out = append(out, m.ReqExtra)
	}
	if m.ReqRaw != "" {
		out = append(out, m.ReqRaw)
	}
	if m.Resp != "" {
		out = append(out, m.Resp)
	}
	if m.Envelope != "" {
		out = append(out, m.Envelope)
	}
	return out
}

// splitRequestMessages 把请求体拆成逐消息块 + 剩余部分。
//
// 返回：
//   - msgs：每条消息的哈希（内容 = 该消息 compact 后的字节）
//   - extra：请求体去掉 "messages" 数组后的剩余 JSON
//   - ok：是否成功拆分。false 时调用方把整个请求体当一个块存（ReqRaw 路径）。
//
// 消息哈希按 compact 后的字节算：去空白后同一条消息在前后轮字节一致，哈希
// 才能命中；客户端在消息内部加无意义空白只会多存一份块，不影响正确性。
func splitRequestMessages(body []byte) (msgs []messageEntry, contents [][]byte, extra string, ok bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, nil, "", false
	}
	rawMsgs, present := top["messages"]
	if !present {
		return nil, nil, "", false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawMsgs, &items); err != nil {
		return nil, nil, "", false
	}
	if len(items) == 0 {
		return nil, nil, "", false
	}
	msgs = make([]messageEntry, 0, len(items))
	contents = make([][]byte, 0, len(items))
	for _, item := range items {
		role := ""
		var one struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(item, &one); err == nil {
			role = one.Role
		}
		compact, err := compactJSON(item)
		if err != nil {
			return nil, nil, "", false
		}
		msgs = append(msgs, messageEntry{Role: role, Hash: contentHash(compact)})
		contents = append(contents, compact)
	}
	// 剩余部分：删掉 messages 键后 compact 重编码（参数/工具定义，体积小）。
	delete(top, "messages")
	rest, err := json.Marshal(top)
	if err != nil {
		return nil, nil, "", false
	}
	return msgs, contents, string(rest), true
}

// compactJSON 压缩 JSON 片段（去空白、数字规范化），保证哈希输入稳定。
func compactJSON(raw json.RawMessage) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// rebuildRequestBody 按 manifest 把块拼回完整请求体（Detail 用）。
//
// 容错语义：个别消息块缺失/损坏不整条报废——用占位消息顶上（标明块哈希与
// 缺失原因），其余消息照常拼回；req_extra 或 req_raw 缺失才是硬错误。
func (bs *blobStore) rebuildRequestBody(m requestManifest) (string, error) {
	if m.ReqRaw != "" {
		data, err := bs.getBlob(m.ReqRaw)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if len(m.Messages) == 0 {
		return "", nil
	}
	items := make([]json.RawMessage, 0, len(m.Messages))
	missing := 0
	for _, h := range m.Messages {
		data, err := bs.getBlob(h)
		if err != nil {
			missing++
			placeholder, _ := json.Marshal(map[string]string{
				"role":      "missing",
				"content":   "[消息块缺失，无法还原内容]",
				"block":     h,
				"error":     err.Error(),
			})
			items = append(items, placeholder)
			continue
		}
		items = append(items, json.RawMessage(data))
	}
	msgsJSON, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	if m.ReqExtra == "" {
		out := "{\"messages\":" + string(msgsJSON) + "}"
		if missing > 0 {
			return out, fmt.Errorf("request-log: %d/%d message blobs missing", missing, len(m.Messages))
		}
		return out, nil
	}
	extraData, err := bs.getBlob(m.ReqExtra)
	if err != nil {
		return "", err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(extraData, &top); err != nil {
		return "", fmt.Errorf("request-log: parse req_extra: %w", err)
	}
	top["messages"] = msgsJSON
	out, err := json.Marshal(top)
	if err != nil {
		return "", err
	}
	if missing > 0 {
		return string(out), fmt.Errorf("request-log: %d/%d message blobs missing", missing, len(m.Messages))
	}
	return string(out), nil
}
