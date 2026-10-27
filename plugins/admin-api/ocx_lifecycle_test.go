package adminapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loadout/core/config"
)

// tempOcxHome 把 ocx 的三条路径指向临时目录，隔离真实用户配置。
func tempOcxHome(t *testing.T) (home string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	oldCfg, oldCodex, oldState := ocxConfigPath, ocxCodexConfigPath, ocxServiceStatePath
	ocxConfigPath = func() string { return filepath.Join(dir, "config.json") }
	ocxCodexConfigPath = func() string { return filepath.Join(dir, "codex-config.toml") }
	ocxServiceStatePath = func() string { return filepath.Join(dir, "service-state.json") }
	return dir, func() {
		ocxConfigPath, ocxCodexConfigPath, ocxServiceStatePath = oldCfg, oldCodex, oldState
	}
}

// TestOcxStatusDetectsInitAndLoadout 验证「初始化」与「Loadout provider + 模型数」被正确识别。
// 这是用户反馈的核心：卡片必须能表达「有没有 init」「配了没配我的服务」。
func TestOcxStatusDetectsInitAndLoadout(t *testing.T) {
	dir, cleanup := tempOcxHome(t)
	defer cleanup()

	cfg := map[string]any{
		"port":            10100,
		"defaultProvider": "Loadout",
		"providers": map[string]any{
			"Loadout": map[string]any{
				"baseUrl": "http://localhost:3000/v1",
				"models":  []string{"deepseek-auto", "claude-opus-5"},
			},
			"other": map[string]any{"baseUrl": "https://api.example.com/v1"},
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	svc := &Service{}
	info := svc.readOcxConfigFull()
	if !info.initialized {
		t.Fatal("config.json 存在时应判定为已初始化")
	}
	if !info.hasLoadout {
		t.Fatal("应识别出指向 localhost:3000 的 Loadout provider")
	}
	if info.modelCount != 2 {
		t.Fatalf("模型数应为 2，实际 %d", info.modelCount)
	}
	if info.providerCount != 2 {
		t.Fatalf("provider 数应为 2，实际 %d", info.providerCount)
	}
}

// TestOcxConfigMissingMeansNotInitialized 配置缺失时必须报「未初始化」，
// 而不是静默当成已就绪（否则用户永远看不到 ocx init 的提示）。
func TestOcxConfigMissingMeansNotInitialized(t *testing.T) {
	_, cleanup := tempOcxHome(t)
	defer cleanup()

	info := (&Service{}).readOcxConfigFull()
	if info.initialized {
		t.Fatal("config.json 不存在时不应判定为已初始化")
	}
	if info.hasLoadout {
		t.Fatal("配置缺失时不应判定为已配置 Loadout")
	}
	if info.port != ocxDefaultPort {
		t.Fatalf("端口应回落到默认 %d，实际 %d", ocxDefaultPort, info.port)
	}
}

// TestOcxCapabilitiesReflectLifecycle 验证能力清单与三种启动方式被正确派生。
func TestOcxCapabilitiesReflectLifecycle(t *testing.T) {
	st := OcxStatus{
		CLIInstalled:      true,
		ConfigInitialized: true,
		ServiceRunning:    true,
		LoadoutProvider:   true,
		ModelCount:        3,
		CodexInjected:     true,
		ServiceInstalled:  true,
		ServiceBackend:    "scheduler",
		ProxyPID:          1234,
		Port:              10100,
		ProxyURL:          "http://127.0.0.1:10100",
		LoadoutBaseURL:    "http://localhost:3000",
	}
	st.buildDerived()

	if len(st.Capabilities) != 7 {
		t.Fatalf("应有 7 条生命周期能力，实际 %d", len(st.Capabilities))
	}
	for _, c := range st.Capabilities {
		if !c.OK {
			t.Errorf("能力 %s 应就绪，详情=%s", c.Key, c.Detail)
		}
	}
	if st.Protection != "service" {
		t.Errorf("保护等级应为 service，实际 %s", st.Protection)
	}
	if !st.RebootSafe {
		t.Error("服务已装且代理在跑时应判定重启安全")
	}
	if len(st.Methods) != 3 {
		t.Fatalf("应有 3 种启动方式，实际 %d", len(st.Methods))
	}
	if st.Methods[1].Key != "service" || !st.Methods[1].Active {
		t.Errorf("service 方式应标记为当前方式，实际 %+v", st.Methods[1])
	}
}

// TestOcxManualLaunchIsNotRebootSafe 手动启动（无服务无 shim）不能算重启安全。
func TestOcxManualLaunchIsNotRebootSafe(t *testing.T) {
	st := OcxStatus{
		CLIInstalled:      true,
		ConfigInitialized: true,
		ServiceRunning:    true,
		LoadoutProvider:   true,
		CodexInjected:     true,
		ProxyPID:          999,
	}
	st.LaunchMode = detectLaunchMode(false, false, st.ProxyPID)
	st.buildDerived()

	if st.Protection != "none" {
		t.Errorf("无服务无 shim 时保护等级应为 none，实际 %s", st.Protection)
	}
	if st.RebootSafe {
		t.Error("手动启动不应判定为重启安全")
	}
	if st.LaunchMode != OcxModeManual {
		t.Errorf("应判定为 manual，实际 %s", st.LaunchMode)
	}
}

// TestReadServiceState 读 service-state.json 判断服务是否已注册。
func TestReadServiceState(t *testing.T) {
	dir, cleanup := tempOcxHome(t)
	defer cleanup()

	if got := readServiceState(); got.Installed {
		t.Fatal("文件不存在时不应判定为已安装服务")
	}
	if err := os.WriteFile(filepath.Join(dir, "service-state.json"),
		[]byte(`{"backend":"scheduler","version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readServiceState()
	if !got.Installed || got.Backend != "scheduler" {
		t.Fatalf("应识别 scheduler 服务，实际 %+v", got)
	}
}

// TestIsLoadoutBaseURLAcceptsDebugPort 回归：debug.ps1 把后端起在 :5009，
// 而用户 OpenCodex 配置指向 :3000。两个端口都要认，否则「明明配好了」会被误判成没配。
func TestIsLoadoutBaseURLAcceptsDebugPort(t *testing.T) {
	old := config.ServerAddr
	config.ServerAddr = ":5009"
	defer func() { config.ServerAddr = old }()

	for _, u := range []string{
		"http://localhost:3000/v1",
		"http://127.0.0.1:3000/v1",
		"http://localhost:5009/v1",
	} {
		if !isLoadoutBaseURL(u) {
			t.Errorf("%s 应识别为 Loadout（:3000 默认 + 当前进程 :5009 都算）", u)
		}
	}
	if isLoadoutBaseURL("http://localhost:8000/v1") {
		t.Error(":8000 不应识别为 Loadout")
	}
}

// TestModelCountFallsBackByProviderName 回归：baseUrl 端口对不上时，
// 仍要按 provider 名字 + modelDiscovery 缓存把模型数读出来，不能显示 0。
func TestModelCountFallsBackByProviderName(t *testing.T) {
	dir, cleanup := tempOcxHome(t)
	defer cleanup()

	old := config.ServerAddr
	config.ServerAddr = ":5009"
	defer func() { config.ServerAddr = old }()

	cfg := map[string]any{
		"port":            10100,
		"defaultProvider": "Loadout",
		"providers": map[string]any{
			// 故意指向 3000，与当前进程 5009 不同
			"Loadout": map[string]any{"baseUrl": "http://localhost:3000/v1"},
		},
		"modelDiscovery": map[string]any{
			"knownModels": map[string]any{
				"Loadout": map[string]any{
					"ids": []string{"deepseek-auto", "claude-opus-5", "glm-5.2"},
				},
			},
		},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	info := (&Service{}).readOcxConfigFull()
	if !info.hasLoadout {
		t.Fatal("baseUrl 指向 :3000 时应识别为 Loadout（debug 端口兼容）")
	}
	if info.modelCount != 3 {
		t.Fatalf("应从 modelDiscovery 读到 3 个模型，实际 %d", info.modelCount)
	}
}

// TestOcxStatusSlicesNeverNil 回归：切片字段为 nil 会序列化成 JSON null，
// 前端模板里的 .length / v-for 直接抛 TypeError，整张卡片渲染不出来（实测踩到）。
func TestOcxStatusSlicesNeverNil(t *testing.T) {
	_, cleanup := tempOcxHome(t)
	defer cleanup()

	// CLI 未安装时走 early return 分支，最容易留下 nil 切片
	oldLookPath := ocxLookPath
	ocxLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { ocxLookPath = oldLookPath }()

	st := (&Service{}).OcxStatus()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"missing", "capabilities", "methods"} {
		if raw[key] == nil {
			t.Errorf("%s 不能序列化成 null（前端会 render 崩溃）", key)
		}
		if _, ok := raw[key].([]any); !ok {
			t.Errorf("%s 应是数组，实际 %T", key, raw[key])
		}
	}
}
