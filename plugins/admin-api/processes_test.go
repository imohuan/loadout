package adminapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"loadout/core/procreg"
)

// TestProcessesStreamSnapshot 验证进程 SSE 连接时推送全量快照（含运行中的进程）。
func TestProcessesStreamSnapshot(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	// 用全局 procreg 启动一个常驻命令（handler 依赖全局单例）。
	h, err := startLongProc("联调进程")
	if err != nil {
		t.Fatalf("启动进程失败: %v", err)
	}
	defer procreg.Get().Unregister(h.ID(), nil)

	// 连接 SSE，读取首帧 snapshot。
	req, _ := http.NewRequest("GET", ts.URL+"/api/processes/stream", nil)
	req.AddCookie(cookie)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// 读 data 行，找 snapshot 中的联调进程。
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
			Type string          `json:"type"`
			Data []procreg.Proc  `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if ev.Type == "snapshot" {
			for _, p := range ev.Data {
				if p.ID == h.ID() && p.Name == "联调进程" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Fatalf("snapshot 未包含联调进程 %s", h.ID())
	}
}

// TestProcessKill 验证 kill 接口终止进程并移入历史。
func TestProcessKill(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)

	h, err := startLongProc("待杀进程")
	if err != nil {
		t.Fatalf("启动进程失败: %v", err)
	}

	// kill
	resp, body := apiReq(t, ts, http.MethodPost, "/api/processes/"+h.ID()+"/kill", nil, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("kill status = %d: %s", resp.StatusCode, body)
	}

	// 等待进程被终止并从 running 移除。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if procreg.Get().Running(h.ID()) == nil {
			// 已移入历史
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("kill 后进程未从 running 移除")
}

// TestProcessKillNotFound 验证 kill 不存在的进程返回 404。
func TestProcessKillNotFound(t *testing.T) {
	ts, _, pw := newTestServer(t)
	cookie := login(t, ts, pw)
	resp, _ := apiReq(t, ts, http.MethodPost, "/api/processes/nonexistent/kill", nil, cookie)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("kill 不存在进程 status = %d, want 404", resp.StatusCode)
	}
}

// startLongProc 启动一个长命命令（不同平台命令不同）。
func startLongProc(name string) (*procreg.Handle, error) {
	if runtime.GOOS == "windows" {
		return procreg.Get().Run(procreg.Options{
			Name: name, Kind: "test", Cmd: "cmd", Args: []string{"/c", "ping -n 30 127.0.0.1 >nul"},
		})
	}
	return procreg.Get().Run(procreg.Options{
		Name: name, Kind: "test", Cmd: "sleep", Args: []string{"30"},
	})
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || strings.Contains(s, sub))
}
