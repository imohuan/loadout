package adminapi

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLoadoutBaseURL(t *testing.T) {
	tests := []struct{
		name string
		raw  string
		want bool
	}{
		{"空串", "", false},
		{"标准 localhost", "http://localhost:3000/v1", true},
		{"127.0.0.1", "http://127.0.0.1:3000/v1", true},
		{"无路径", "http://localhost:3000", true},
		// 不带端口等于 80；Loadout 从不监听 80，不算接入。
		{"无端口", "http://localhost", false},
		{"纯主机名", "localhost:3000", true},
		{"远程地址", "https://api.example.com/v1", false},
		{"不同端口", "http://localhost:8000/v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoadoutBaseURL(tt.raw); got != tt.want {
				t.Errorf("isLoadoutBaseURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		});
	}
}

func TestOcxLookPath(t *testing.T) {
	path, err := exec.LookPath("ocx")
	if err != nil {
		t.Skipf("ocx 未安装，跳过: %v", err)
	}
	t.Logf("ocx resolved to: %s", path)
	// 确认是可执行文件（.cmd / .exe / .bat，Windows 上 ps1 不能直接 exec）
	ext := strings.ToLower(filepath.Ext(path))
	t.Logf("extension: %s", ext)
	if ext == ".ps1" {
		t.Errorf("ocx 解析到 .ps1（Windows exec.Command 无法直接执行 ps1），实际路径: %s", path)
	}
}
