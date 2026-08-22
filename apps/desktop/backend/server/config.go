package server

import (
	"encoding/json"
	"os"
	"path/filepath"

	"proxyui/backend"
)

// Config 运行时配置（用户可在 UI 中修改）。
type Config struct {
	Port int `json:"port"` // HTTP 服务端口
}

// DefaultConfig 返回默认运行时配置，端口从 wails.json 读取。
func DefaultConfig() Config {
	return Config{Port: config.App.Custom.Server.Port}
}

// configPath 返回运行时配置文件路径（%APPDATA%/<AppName>/config.json）。
func configPath() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// LoadConfig 从磁盘加载运行时配置，文件不存在时返回默认配置。
func LoadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return DefaultConfig(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultConfig(), nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), err
	}
	if cfg.Port == 0 {
		cfg.Port = config.App.Custom.Server.Port
	}
	return cfg, nil
}

// SaveConfig 将运行时配置写入磁盘。
func SaveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, data, 0644)
}
