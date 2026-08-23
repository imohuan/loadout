package visionv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	modelgateway "loadout/plugins/model-gateway"
)

// mockForwarder 模拟 model-gateway 子请求通道：按 pipe 的 __channel_candidates
// 逐个候选渠道发 HTTP（与真实网关 candidates 循环同语义：失败换下一个），
// 返回成功候选的响应 body；streamWriter 非 nil 时逐行回调（流式）。
// 供 vision_v2 测试注入，替代真实 modelgateway.Service。
type mockForwarder struct {
	// channels: candidate channelID → baseURL
	channels map[string]string
	// lastPipe 记录最近一次 ForwardSubRequest 收到的 pipe（断言用）。
	lastPipe *modelgateway.ProxyPipeline
}

func newMockForwarder(channels map[string]string) *mockForwarder {
	return &mockForwarder{channels: channels}
}

func (m *mockForwarder) ForwardSubRequest(ctx context.Context, pipe *modelgateway.ProxyPipeline, streamWriter func(line []byte) error) (*modelgateway.ProxyPipeline, []byte, error) {
	m.lastPipe = pipe
	var ids []string
	if v, ok := pipe.Metadata["__channel_candidates"].([]string); ok {
		ids = v
	}
	// 续流场景：只有 __current_channel（主渠道），直接用该渠道。
	if len(ids) == 0 {
		if ch, ok := pipe.Metadata["__current_channel"].(string); ok && ch != "" {
			ids = []string{ch}
		}
	}
	if len(ids) == 0 {
		return pipe, nil, errors.New("mockForwarder: 无候选渠道")
	}
	var lastErr error
	for _, id := range ids {
		baseURL, ok := m.channels[id]
		if !ok {
			lastErr = fmt.Errorf("mockForwarder: 渠道 %q 不存在", id)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", strings.NewReader(string(pipe.Request.Body)))
		if err != nil {
			return pipe, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("mockForwarder: 渠道 %q 返回 %d: %s", id, resp.StatusCode, string(b))
			continue
		}
		// 模拟 request-log 在 ProxyBeforeAttempt 的行为：把子请求的完整日志
		// 关联 id 写进 pipe.Metadata，供 vision_v2 回填视觉 attempt 的 request_log_id。
		pipe.Metadata["__request_log_attempt_id"] = "mock-reqlog-" + id
		if streamWriter != nil {
			// 流式：逐行读 SSE 喂回调。
			reader := newSSELineReader(resp.Body)
			for {
				line, err := reader()
				if len(line) > 0 {
					if werr := streamWriter([]byte(line)); werr != nil {
						resp.Body.Close()
						return pipe, nil, werr
					}
				}
				if err != nil {
					break
				}
			}
			resp.Body.Close()
			pipe.Metadata["__last_tried_channel"] = id
			return pipe, nil, nil
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		pipe.Metadata["__last_tried_channel"] = id
		return pipe, body, nil
	}
	return pipe, nil, lastErr
}

// newSSELineReader 返回逐行读取 SSE 的闭包（每行含 \n）。
func newSSELineReader(r io.Reader) func() (string, error) {
	var buf []byte
	return func() (string, error) {
		for {
			i := indexByte(buf, '\n')
			if i >= 0 {
				line := string(buf[:i+1])
				buf = buf[i+1:]
				return line, nil
			}
			chunk := make([]byte, 4096)
			n, err := r.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				// 继续循环切分——即使本次 Read 带 EOF，buf 里的行也要先切出。
				continue
			}
			if err != nil {
				if len(buf) > 0 {
					line := string(buf)
					buf = nil
					return line, nil
				}
				return "", err
			}
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
