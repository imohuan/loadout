package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// 从 codex 会话 jsonl 中按关键字抽取条目，输出精简信息。
func main() {
	path := os.Args[1]
	keyword := os.Args[2]
	maxLen := 1500
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &maxLen)
	}
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, keyword) {
			continue
		}
		n++
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			fmt.Println("[unparsed]", trunc(line, maxLen))
			continue
		}
		ts, _ := m["timestamp"].(string)
		tp, _ := m["type"].(string)
		fmt.Printf("=== #%d %s %s\n", n, ts, tp)
		emit(m["payload"], maxLen, 0)
	}
	if err := sc.Err(); err != nil {
		fmt.Println("scan err:", err)
	}
}

func emit(v interface{}, maxLen, depth int) {
	if depth > 6 {
		return
	}
	switch t := v.(type) {
	case map[string]interface{}:
		// 只打印 key 集合中感兴趣的部分
		for k, vv := range t {
			switch k {
			case "text", "input", "output", "command", "content", "arguments", "result":
				fmt.Printf("  %s: %s\n", k, trunc(fmt.Sprint(vv), maxLen))
			case "type", "name", "server", "tool", "model", "status":
				fmt.Printf("  %s=%v\n", k, vv)
			}
		}
	case []interface{}:
		for i, vv := range t {
			if i > 6 {
				fmt.Println("  ...")
				break
			}
			emit(vv, maxLen, depth+1)
		}
	}
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if len(s) > n {
		return s[:n] + "…[+" + fmt.Sprint(len(s)-n) + "]"
	}
	return s
}
