package workbuddypanel

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// bytesReader 包装 bytes.NewReader。
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// strconvParseFloat 从 "x1.62 credits" 解析倍率数字。
func strconvParseFloat(s string) (float64, error) {
	digits := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' {
			return r
		}
		return -1
	}, s)
	return strconv.ParseFloat(strings.TrimSpace(digits), 64)
}
