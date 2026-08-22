package visionv2

import "strings"

// parseDataURI 解析 data:image/<fmt>;base64,<payload>；非 base64 data URI、
// 非图片类型或无法解析时返回 ok=false。
// 复制自 plugins/vision/compress.go。
func parseDataURI(uri string) (mime, payload string, ok bool) {
	if !strings.HasPrefix(uri, "data:") {
		return "", "", false
	}
	rest := uri[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", false
	}
	meta := rest[:comma]
	payload = rest[comma+1:]
	if payload == "" || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	mime = strings.TrimSuffix(meta, ";base64")
	if !strings.HasPrefix(mime, "image/") {
		return "", "", false
	}
	return mime, payload, true
}
