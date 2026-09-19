package modelhealth

import (
	"strings"

	"loadout/core/config"
)

// internalBaseURL 本机内部调用地址（AI 兜底请求走网关自身 /v1）。
func internalBaseURL() string {
	addr := config.ServerAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		if p := strings.TrimPrefix(addr[i:], ":"); p != "" {
			return "http://127.0.0.1:" + p
		}
	}
	return "http://127.0.0.1"
}
