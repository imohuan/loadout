package main

import (
	"fmt"
	"os"
	"os/signal"

	fakemcp "loadout/testkit/fake-mcp"
)

func main() {
	f := fakemcp.New("fake-mcp", []fakemcp.Tool{
		{Name: "echo", Description: "回声工具", InputSchema: map[string]any{"type": "object"}},
		{Name: "noop", Description: "空操作工具", InputSchema: map[string]any{"type": "object"}},
		{Name: "read_file", Description: "读取文件", InputSchema: map[string]any{"type": "object"}},
		{Name: "web_search", Description: "网页搜索", InputSchema: map[string]any{"type": "object"}},
	})
	defer f.Close()
	fmt.Printf("FAKE_MCP_URL=%s\n", f.URL())
	fmt.Println("fake-mcp ready, waiting for signal...")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch
}
