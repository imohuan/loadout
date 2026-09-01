package multimodalmcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loadout/core/mcpkit"
)

// MCPServer 构建多模态的 MCP server（暴露 3 个识别工具），并返回挂 HTTP 传输的 handler。
// 端点路径 POST /mcp/multimodal，精确路由优先于 servercore 的 /mcp/ 前缀分发器，
// 多模态独立于 mcp-hub。AuthMCPHeader 由 servercore 统一包装。
func (s *Service) MCPServer() http.Handler {
	srv := mcpkit.NewServer("multimodal", tools(s))
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return srv
	}, nil)
	return handler
}
