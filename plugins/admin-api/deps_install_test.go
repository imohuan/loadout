package adminapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loadout/core/procreg"
)

// TestDepsInstallSSEProcess 验证 /api/deps/install 的异步安装进程：
// 用传入的 task id 作为 procreg 进程 id，并在结束后经 SSE 广播 done，
// 前端按 id 匹配即可触发 onDone 收尾。用 SetRunFn mock 安装命令，避免真实 npm 安装。
func TestDepsInstallSSEProcess(t *testing.T) {
	// Mock procreg 命令执行：注册进程并异步广播 done（模拟安装瞬间成功）。
	old := procreg.SetRunFn(func(r *procreg.Registry, o procreg.Options) (*procreg.Handle, error) {
		h := r.RegisterExisting(o.ID, o.Name, o.Kind, nil)
		go func() {
			time.Sleep(50 * time.Millisecond) // 模拟短暂安装耗时
			r.Unregister(o.ID, nil)           // 广播 done
		}()
		return h, nil
	})
	defer func() {
		procreg.SetRunFn(old)
	}()

	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	taskID := "dep-install:skills"
	// 先建立 SSE 订阅，再触发安装，确保收到该进程的事件。
	req, _ := http.NewRequest("GET", ts.URL+"/api/processes/stream", nil)
	req.AddCookie(cookie)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE 请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 触发安装。
	resp2, body := apiReq(t, ts, http.MethodPost, "/api/deps/install",
		map[string]string{"name": "skills", "id": taskID}, cookie)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("install status = %d: %s", resp2.StatusCode, body)
	}

	// 从 SSE 读事件，找 id=taskID 且 status=done 的进程。
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !found {
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type string         `json:"type"`
			Data []procreg.Proc `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		for _, p := range ev.Data {
			if p.ID == taskID && p.Status == procreg.StatusDone {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("SSE 未收到 id=%s 的 done 进程事件", taskID)
	}
}
