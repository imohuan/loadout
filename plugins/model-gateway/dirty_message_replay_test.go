package modelgateway

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 回放 79ed5ee492c3d825 的真实失败请求：原始体里有孤立 tool 结果，旧版 repair 会
// 给它补一条 name:"" 的声明，转发后上游返回 400 code 11133。按 proxy.go 的顺序
// （先剥残缺声明，再补全序列）处理后，body 里不允许再出现空工具名。
//
// 设置 LOADOUT_REPLAY_RAW 指向复现脚本即可运行；文件缺失时跳过。
func TestRepairPipeline_RealReplayNeverEmitsEmptyName(t *testing.T) {
	path := os.Getenv("LOADOUT_REPLAY_RAW")
	if path == "" {
		t.Skip("未设置 LOADOUT_REPLAY_RAW，跳过真实请求回放")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip("读取复现脚本失败，跳过: " + err.Error())
	}
	var body []byte
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "const REQUEST_BODY = ") {
			literal := strings.TrimSuffix(strings.TrimPrefix(line, "const REQUEST_BODY = "), ";")
			var unquoted string
			if err := json.Unmarshal([]byte(literal), &unquoted); err != nil {
				t.Fatalf("解析 REQUEST_BODY 失败: %v", err)
			}
			body = []byte(unquoted)
		}
	}
	if len(body) == 0 {
		t.Skip("复现脚本里没有 REQUEST_BODY")
	}

	stripped, _ := stripBrokenAssistantMessages(body)
	fixed, changed := repairToolCallSequence(stripped)
	if !changed {
		t.Fatal("真实请求含孤立 tool 结果，应触发补全")
	}
	if bytes.Contains(fixed, []byte(`"name":""`)) || bytes.Contains(fixed, []byte(`"name": ""`)) {
		t.Fatalf("补全后仍含空工具名，上游会 400")
	}
	var root struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(fixed, &root); err != nil {
		t.Fatalf("补全结果不是合法 JSON: %v", err)
	}
	for i, m := range root.Messages {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				t.Fatalf("message[%d] tool_call %s 名字为空", i, tc.ID)
			}
		}
	}
}
