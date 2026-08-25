package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"loadout/core/procreg"
)

// handleProcessesStream 全局 SSE：连接即推送当前全部进程快照，之后实时推送进程变化。
// 载荷沿用 {type, data} 约定：type=snapshot（全量）/ update（单进程变化）。
// 不写 event: 行，保证 onmessage 必触发。
func (s *Service) handleProcessesStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	reg := procreg.Get()
	ch := reg.Subscribe()
	defer reg.Unsubscribe(ch)

	// 连接即推全量快照。
	writeSSE(w, flusher, "snapshot", reg.Snapshot())

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, flusher, ev.Type, ev.Data)
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleProcessKill 终止指定进程（杀整棵进程树）。
func (s *Service) handleProcessKill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing process id"})
		return
	}
	reg := procreg.Get()
	if reg.Running(id) == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "process not found"})
		return
	}
	if err := reg.Kill(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeSSE 把事件序列化为 data: JSON 并推送。
func writeSSE(w http.ResponseWriter, flusher http.Flusher, typ string, data any) {
	b, err := json.Marshal(map[string]any{"type": typ, "data": data})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	flusher.Flush()
}
