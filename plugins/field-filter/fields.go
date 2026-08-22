package fieldfilter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// applyFieldRules 对 JSON body 应用字段规则：keep 非空走白名单（strip 忽略），
// 否则按 strip 剔除。非 JSON（含顶层数组）/空 body 原样返回；无字段命中
// 返回原字节（不重写）。用 map[string]any + UseNumber 保证数字精度无损。
func applyFieldRules(body []byte, keep, strip []string) []byte {
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return body
	}
	var removed int
	if len(keep) > 0 {
		removed = keepTopLevel(obj, keep)
	} else if len(strip) > 0 {
		removed = stripPaths(obj, strip)
	}
	if removed == 0 {
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// stripPaths 按点路径剔除字段（a.b.c 删 obj["a"]["b"]["c"]），返回删除数量。
// 中间节点不是对象时跳过该路径。
func stripPaths(obj map[string]any, paths []string) int {
	removed := 0
	for _, p := range paths {
		parts := strings.Split(p, ".")
		cur := obj
		for i, part := range parts {
			if i == len(parts)-1 {
				if _, ok := cur[part]; ok {
					delete(cur, part)
					removed++
				}
				break
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				break
			}
			cur = next
		}
	}
	return removed
}

// keepTopLevel 白名单：只保留指定的顶层 key，返回删除数量。
func keepTopLevel(obj map[string]any, keep []string) int {
	allowed := make(map[string]bool, len(keep))
	for _, k := range keep {
		allowed[k] = true
	}
	removed := 0
	for k := range obj {
		if !allowed[k] {
			delete(obj, k)
			removed++
		}
	}
	return removed
}
