package visionv2

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"loadout/core/config"
)

const idLen = 12

// imageID 图片内容 md5 截断 12 位十六进制。
func imageID(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])[:idLen]
}

// SaveImageDataURI 解码 data URI → 原始字节落盘 files/{id}.bin，返回 id。
func (s *Service) SaveImageDataURI(dataURI string) (string, error) {
	_, payload, ok := parseDataURI(dataURI)
	if !ok {
		return "", errors.New("vision_v2: 非法 data URI")
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	return s.saveBytes(raw)
}

// SaveImageURL 下载远程图片落盘（失败返回 err，调用方保留原块）。
func (s *Service) SaveImageURL(ctx context.Context, url string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vision_v2: 下载图片失败 %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(config.VisionMaxImageBytes)))
	if err != nil {
		return "", err
	}
	return s.saveBytes(raw)
}

func (s *Service) saveBytes(raw []byte) (string, error) {
	id := imageID(raw)
	dir := s.imageFilesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, id+".bin")
	if _, err := os.Stat(dst); err == nil {
		return id, nil // 幂等：同图已存在
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// loadImageBytes 按 id 读回原始字节，并用 DetectContentType 推 mime（重建 data URI 用）。
func (s *Service) loadImageBytes(id string) (raw []byte, mime string, err error) {
	raw, err = os.ReadFile(filepath.Join(s.imageFilesDir(), id+".bin"))
	if err != nil {
		return nil, "", err
	}
	return raw, http.DetectContentType(raw), nil
}

// cleanupStaleFiles 懒清理：files/ 下 mtime 超过 VisionCacheTTLHours 的孤儿文件。
func (s *Service) cleanupStaleFiles() {
	ttl := time.Duration(config.VisionCacheTTLHours) * time.Hour
	entries, err := os.ReadDir(s.imageFilesDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > ttl {
			_ = os.Remove(filepath.Join(s.imageFilesDir(), e.Name()))
		}
	}
}
