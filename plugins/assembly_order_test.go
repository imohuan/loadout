package plugins

import (
	"database/sql"
	"log/slog"
	"net/http"
	"testing"

	"loadout/core/plugin"
	"loadout/core/store"
)

// TestAssemblyOrderLogPlugins 固化「route-log 必须先于 request-log 装配」。
//
// 原因：route-log 在 Apply 里登记「完整日志存在性查询」安装器，request-log 装配完成
// 后用 ctx.InstallRouteLogPresence 回填。装配顺序由 inject 拓扑排序决定、不看注册顺序——
// request-log 若排在前面，回填时安装器还没登记，入口存在性校验会静默失效
// （列表永远显示已失效的「进入日志」入口）。靠 request-log Manifest 里 Inject "route-log"
// 约束顺序；本测试防止将来有人顺手删掉那个 inject。
func TestAssemblyOrderLogPlugins(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", t.TempDir()+"/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	asm, err := plugin.Load(All(), plugin.Options{
		Logger: slog.New(slog.DiscardHandler),
		Services: map[string]any{
			"store": st, "logger": slog.New(slog.DiscardHandler),
			"http-client": &http.Client{}, "db": database,
		},
	})
	if err != nil {
		t.Fatalf("assembly failed: %v", err)
	}
	defer asm.Unload()

	order := plugin.OrderOf(asm)
	routeIdx, requestIdx := -1, -1
	for i, name := range order {
		switch name {
		case "route-log":
			routeIdx = i
		case "request-log":
			requestIdx = i
		}
	}
	if routeIdx < 0 || requestIdx < 0 {
		t.Fatalf("装配顺序里缺少日志插件: route-log=%d request-log=%d (order=%v)", routeIdx, requestIdx, order)
	}
	if routeIdx > requestIdx {
		t.Fatalf("route-log 必须在 request-log 之前装配（route=%d request=%d, order=%v）", routeIdx, requestIdx, order)
	}
}
