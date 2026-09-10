//go:build ignore

// verify_run.go：端到端验证「同步取数」路径返回的 OpenCodex 模型数。
// 打的是本机正在运行的 loadout 服务（默认 127.0.0.1:3000），
// 用 store 的 SecretKey 自签一个管理员会话 token 走 AuthSession。
//
// 用法：go run docs/tmp/unifyai-opencodex-zero/verify_run.go [baseURL]
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"loadout/core/auth"
	"loadout/core/config"
	"loadout/core/store"
)

func main() {
	base := "http://127.0.0.1:3000"
	if len(os.Args) > 1 {
		base = os.Args[1]
	}
	// 打开与运行中服务同一个数据目录（~/.loadout），拿到同一把 secret。
	st, err := store.New(config.DataDir)
	if err != nil {
		fmt.Println("open store:", err)
		os.Exit(1)
	}
	token, err := auth.SignToken(st.SecretKey(), "admin", time.Hour)
	if err != nil {
		fmt.Println("sign token:", err)
		os.Exit(1)
	}

	get := func(path string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
		if err != nil {
			fmt.Printf("%s -> ERR %v\n", path, err)
			return nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			fmt.Printf("%s -> HTTP %d %s\n", path, resp.StatusCode, string(body))
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			fmt.Printf("%s -> 非法 JSON: %v\n", path, err)
			return nil
		}
		return out
	}

	// 1) /all —— 页面「同步取数」走的就是这条。
	if all := get("/api/unifyai/all"); all != nil {
		m, _ := all["models"].(map[string]any)
		if m == nil {
			fmt.Println("/api/unifyai/all -> models 缺失")
		} else {
			list, _ := m["models"].([]any)
			fmt.Printf("/api/unifyai/all      -> count=%v models=%d degraded=%v reason=%v\n",
				m["count"], len(list), m["degraded"], m["degradedReason"])
			if len(list) > 0 {
				first, _ := list[0].(map[string]any)
				fmt.Printf("   首个模型: %v (contextWindow=%v)\n", first["modelId"], first["contextWindow"])
			}
			raw, _ := json.Marshal(m)
			fmt.Printf("   原始 models 对象: %s\n", raw)
		}
	}
	// 2) /opencodex-models —— 「刷新元数据」后重拉的那条。
	if om := get("/api/unifyai/opencodex-models"); om != nil {
		list, _ := om["models"].([]any)
		fmt.Printf("/api/unifyai/opencodex-models -> count=%v models=%d\n", om["count"], len(list))
	}
	// 3) /opencodex-models-live —— 原样透传 CLI 输出，看 CLI 到底报了什么。
	if lv := get("/api/unifyai/opencodex-models-live"); lv != nil {
		raw, _ := json.Marshal(lv)
		fmt.Printf("/api/unifyai/opencodex-models-live -> %s\n", raw)
	}
}
