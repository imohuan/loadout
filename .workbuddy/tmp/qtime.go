package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// 按时间范围打印 codex 会话中的命令与输出摘要。
func main() {
	path := os.Args[1]
	from := os.Args[2]
	to := os.Args[3]
	maxLen := 1200
	if len(os.Args) > 4 {
		fmt.Sscanf(os.Args[4], "%d", &maxLen)
	}
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 128*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		ts, _ := m["timestamp"].(string)
		if ts < from || ts > to {
			continue
		}
		payload, _ := m["payload"].(map[string]interface{})
		ptype, _ := payload["type"].(string)
		if ptype == "item_completed" {
			item, _ := payload["item"].(map[string]interface{})
			it, _ := item["type"].(string)
			switch it {
			case "CommandExecution":
				cmd := fmt.Sprint(item["command"])
				out := fmt.Sprint(item["stdout"])
				if len(out) > maxLen {
					out = out[:maxLen] + "…"
				}
				fmt.Printf("[%s] CMD %s\n   OUT: %s\n", ts, trunc(cmd, 600), out)
			case "FileChange":
				ch, _ := item["changes"].(map[string]interface{})
				for k, v := range ch {
					vm, _ := v.(map[string]interface{})
					fmt.Printf("[%s] FILE %s %v\n", ts, k, vm["type"])
				}
			}
			continue
		}
		if ptype == "reasoning" || ptype == "message" {
			s := fmt.Sprint(payload["content"])
			fmt.Printf("[%s] %s %s\n", ts, ptype, trunc(s, maxLen))
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

var _ = strings.TrimSpace
