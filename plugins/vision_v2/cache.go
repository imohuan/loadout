package visionv2

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loadout/core/config"
)

const visionCacheVersion = "v3"

// visionCacheKey 缓存 key：md5(id|prompt|v3)。
// 含方向（prompt）：同图同方向命中，换方向重识别（方向由模型在工具参数显式给出，可入 key）。
// 不含模型名：换视觉模型/换提供商也能复用。
func visionCacheKey(id, prompt string) string {
	return md5Hex(id + "|" + strings.TrimSpace(prompt) + "|" + visionCacheVersion)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// readCache 读取缓存；未命中或已过期返回 ok=false。
func (s *Service) readCache(key string) (string, bool) {
	path := filepath.Join(s.cacheDir, key+".txt")
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	ttl := time.Duration(config.VisionCacheTTLHours) * time.Hour
	if time.Since(info.ModTime()) > ttl {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// writeCache 写入缓存文件（先建目录，0600 权限）。
func (s *Service) writeCache(key, text string) error {
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.cacheDir, key+".txt"), []byte(text), 0o600)
}
