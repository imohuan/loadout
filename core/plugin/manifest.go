package plugin

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadManifest 读取 plugins/*/plugin.yaml 并解析为 Manifest。
// v1 采用编译期装配（方案 A），本函数主要用于生成器与自检/校验场景，
// 运行时装配以 Plugin.Manifest() 为准。
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin: 读取清单失败 %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("plugin: 解析清单失败 %s: %w", path, err)
	}
	if m.Name == "" {
		return Manifest{}, fmt.Errorf("plugin: 清单 %s 缺少 name", path)
	}
	return m, nil
}

// ValidateManifest 校验清单字段的合法性（name 非空、inject/provide 无空项）。
func ValidateManifest(m Manifest) error {
	if m.Name == "" {
		return fmt.Errorf("plugin: name 不能为空")
	}
	for _, s := range m.Inject {
		if s == "" {
			return fmt.Errorf("plugin: %s 的 inject 含空服务名", m.Name)
		}
	}
	for _, s := range m.Provide {
		if s == "" {
			return fmt.Errorf("plugin: %s 的 provide 含空服务名", m.Name)
		}
	}
	return nil
}
