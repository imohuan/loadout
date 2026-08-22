package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 注意：本包使用包级变量保存配置，测试直接操纵这些变量并在结束时恢复默认，
// 以保证不污染其他测试。Load() 本身也会覆盖这些值。

func reset() { Load() }

func TestStrEnv(t *testing.T) {
	t.Run("未设置返回默认", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_UNSET", "")
		if got := strEnv("LOADOUT_TEST_UNSET", "def"); got != "def" {
			t.Fatalf("strEnv 未设置时应返回默认值, got %q", got)
		}
	})
	t.Run("设置返回环境值", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_SET", "hello")
		if got := strEnv("LOADOUT_TEST_SET", "def"); got != "hello" {
			t.Fatalf("strEnv 设置时应返回环境值, got %q", got)
		}
	})
	t.Run("空值回落默认", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_EMPTY", "")
		if got := strEnv("LOADOUT_TEST_EMPTY", "def"); got != "def" {
			t.Fatalf("空环境变量应回落默认, got %q", got)
		}
	})
}

func TestIntEnv(t *testing.T) {
	if got := intEnv("LOADOUT_TEST_INT_MISSING", 42); got != 42 {
		t.Fatalf("未设置应返回默认, got %d", got)
	}
	t.Setenv("LOADOUT_TEST_INT_OK", "99")
	if got := intEnv("LOADOUT_TEST_INT_OK", 42); got != 99 {
		t.Fatalf("解析成功应返回 99, got %d", got)
	}
	t.Setenv("LOADOUT_TEST_INT_BAD", "abc")
	if got := intEnv("LOADOUT_TEST_INT_BAD", 42); got != 42 {
		t.Fatalf("解析失败应回落默认, got %d", got)
	}
}

func TestBoolEnv(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "TRUE": true, "yes": true, "on": true,
		"0": false, "false": false, "no": false, "off": false,
	}
	for v, want := range cases {
		t.Setenv("LOADOUT_TEST_BOOL", v)
		if got := boolEnv("LOADOUT_TEST_BOOL", !want); got != want {
			t.Fatalf("boolEnv(%q) = %v, want %v", v, got, want)
		}
	}
	t.Setenv("LOADOUT_TEST_BOOL_BAD", "xyz")
	if got := boolEnv("LOADOUT_TEST_BOOL_BAD", true); !got {
		t.Fatalf("非法值应回落默认 true, got %v", got)
	}
}

func TestDurEnv(t *testing.T) {
	t.Run("Go 时长格式", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_DUR", "2m")
		if got := durEnv("LOADOUT_TEST_DUR", time.Second); got != 2*time.Minute {
			t.Fatalf("2m 应解析为 2m, got %v", got)
		}
	})
	t.Run("纯整数秒", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_DUR_INT", "300")
		if got := durEnv("LOADOUT_TEST_DUR_INT", time.Second); got != 300*time.Second {
			t.Fatalf("300 应解析为 300s, got %v", got)
		}
	})
	t.Run("未设置回落默认", func(t *testing.T) {
		if got := durEnv("LOADOUT_TEST_DUR_MISSING", 5*time.Second); got != 5*time.Second {
			t.Fatalf("未设置应回落默认, got %v", got)
		}
	})
	t.Run("非法回落默认", func(t *testing.T) {
		t.Setenv("LOADOUT_TEST_DUR_BAD", "not-a-duration")
		if got := durEnv("LOADOUT_TEST_DUR_BAD", 5*time.Second); got != 5*time.Second {
			t.Fatalf("非法值应回落默认, got %v", got)
		}
	})
}

func TestExpandHome(t *testing.T) {
	home := mustUserHome(t)
	t.Run("单独波浪号", func(t *testing.T) {
		if got := expandHome("~"); got != home {
			t.Fatalf("expandHome(~) = %q, want %q", got, home)
		}
	})
	t.Run("前缀波浪号", func(t *testing.T) {
		if got := expandHome("~/.loadout"); got != filepath.Join(home, ".loadout") {
			t.Fatalf("expandHome(~/.loadout) = %q, want %q", got, filepath.Join(home, ".loadout"))
		}
	})
	t.Run("无波浪号原样返回", func(t *testing.T) {
		if got := expandHome("/tmp/foo"); got != "/tmp/foo" {
			t.Fatalf("expandHome(/tmp/foo) = %q", got)
		}
	})
}

func TestLoadEnvOverride(t *testing.T) {
	defer reset()
	t.Setenv("LOADOUT_SERVER_ADDR", ":9999")
	t.Setenv("LOADOUT_LOG_LEVEL", "debug")
	t.Setenv("LOADOUT_HOME_DIR", "~/.loadout-test")
	Load()
	if ServerAddr != ":9999" {
		t.Fatalf("ServerAddr 环境变量未生效, got %q", ServerAddr)
	}
	if LogLevel != "debug" {
		t.Fatalf("LogLevel 环境变量未生效, got %q", LogLevel)
	}
	wantData := filepath.Join(mustUserHome(t), ".loadout-test", "data")
	if DataDir != wantData {
		t.Fatalf("DataDir 派生错误, got %q want %q", DataDir, wantData)
	}
}

func TestLoadDefaults(t *testing.T) {
	defer reset()
	// 清除相关环境变量后 Load，验证回落默认值。
	for _, k := range []string{
		"LOADOUT_SERVER_ADDR", "LOADOUT_LOG_LEVEL", "LOADOUT_HOME_DIR",
		"LOADOUT_UPSTREAM_BASE_URL", "LOADOUT_DEFAULT_VISION_MODEL",
	} {
		t.Setenv(k, "")
	}
	Load()
	if ServerAddr != ":3000" {
		t.Fatalf("默认 ServerAddr = %q, want :3000", ServerAddr)
	}
	if LogLevel != "info" {
		t.Fatalf("默认 LogLevel = %q, want info", LogLevel)
	}
	if DefaultVisionModel != "qwen-vl-max" {
		t.Fatalf("默认 DefaultVisionModel = %q, want qwen-vl-max", DefaultVisionModel)
	}
}

func TestDerivedDirs(t *testing.T) {
	defer reset()
	home := mustUserHome(t)
	if DataDir != filepath.Join(home, ".loadout", "data") {
		t.Fatalf("DataDir = %q", DataDir)
	}
	if SkillsDir != filepath.Join(home, ".loadout", "skills") {
		t.Fatalf("SkillsDir = %q", SkillsDir)
	}
	if SecretFile != filepath.Join(home, ".loadout", "data", ".secret") {
		t.Fatalf("SecretFile = %q", SecretFile)
	}
}

func mustUserHome(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法获取用户主目录: %v", err)
	}
	return h
}
