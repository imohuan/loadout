package translate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ---- 只读查询（组件获取已翻译结果，绝不触发翻译）----

// LookupRequest 只读查询请求。
type LookupRequest struct {
	SourceText string           `json:"source_text"`
	TargetLang string           `json:"target_lang"`
	Type       TranslationType  `json:"type"`
	Items      []LookupItem     `json:"items"`
}

// LookupItem 批量只读查询的一项。
type LookupItem struct {
	Text string `json:"text"`
}

// LookupResponse 只读查询响应：texts 与请求项一一对应，未命中为 null。
type LookupResponse struct {
	Texts []*string `json:"texts"`
}

// handleLookup 处理 POST /api/translate/lookup：按 hash 查库返回已有译文，不翻译。
func (s *Service) handleLookup(w http.ResponseWriter, r *http.Request) {
	var req LookupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "请求体解析失败: " + err.Error()}})
		return
	}
	if req.Type == "" {
		req.Type = TypeTranslate
	}
	if req.TargetLang == "" {
		req.TargetLang = "zh-CN"
	}
	texts := req.Items
	if req.SourceText != "" {
		texts = []LookupItem{{Text: req.SourceText}}
	}
	resp := LookupResponse{Texts: make([]*string, len(texts))}
	for i, it := range texts {
		t, ok, err := s.getCached(r.Context(), hashText(it.Text), req.TargetLang, req.Type)
		if err == nil && ok {
			v := t
			resp.Texts[i] = &v
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- 单条/多条翻译 ----

// handleTranslate 处理 POST /api/translate。
func (s *Service) handleTranslate(w http.ResponseWriter, r *http.Request) {
	var req TranslateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "请求体解析失败: " + err.Error()}})
		return
	}
	results, err := s.Translate(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	resp := TranslateResponse{Texts: results}
	if req.SourceText != "" && len(results) > 0 {
		resp.Text = results[0]
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- 批量翻译（后台任务） ----

// BatchItem 批量翻译的一个条目。
type BatchItem struct {
	SourceType  SourceType `json:"source_type"`
	SourceID    string     `json:"source_id"`
	Description string     `json:"description"`
	// Key 文本键（如 param/xxx），落库时写入 key 字段，供前端按 textKey 读回译文。
	Key string `json:"key"`
}

// BatchRequest 批量翻译请求。
type BatchRequest struct {
	Items      []BatchItem    `json:"items"`
	TargetLang string         `json:"target_lang"`
	Model      string         `json:"model"`
	Prompt     string         `json:"prompt"`
	Type       TranslationType `json:"type"`
	// Concurrency 并发翻译数量；<=0 时取默认值 5。
	Concurrency int `json:"concurrency"`
}

// defaultBatchConcurrency 批量翻译默认并发数。
const defaultBatchConcurrency = 5

// BatchStartResponse 启动批量任务后立即返回的响应。
type BatchStartResponse struct {
	TaskID string `json:"task_id"`
	Total  int    `json:"total"`
}

// BatchStatusResponse 批量任务进度/状态。
type BatchStatusResponse struct {
	TaskID   string `json:"task_id"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Running  bool   `json:"running"`
	Finished bool   `json:"finished"`
	Cancelled bool  `json:"cancelled"`
	Error    string `json:"error,omitempty"`
}

// batchTask 一个后台批量翻译任务：脱离 HTTP 请求生命周期独立运行。
type batchTask struct {
	id       string
	total    int
	done     int
	mu       sync.Mutex
	cancel   context.CancelFunc
	running  bool
	finished bool
	cancelled bool
	err      string
	// results 按索引记录已完成的译文（供进度 SSE 订阅实时下发；done 完成后清理）
	results map[int]ProgressEvent
}

// handleBatch 处理 POST /api/translate/batch：启动一个后台批量翻译任务，立即返回 task_id。
// 任务用独立的 context（不随本请求断开而取消），翻译在后台 goroutine 池中执行；
// 前端可通过 GET /batch/status 轮询进度、GET /batch/progress 订阅实时进度、POST /batch/cancel 取消。
func (s *Service) handleBatch(w http.ResponseWriter, r *http.Request) {
	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "请求体解析失败: " + err.Error()}})
		return
	}
	if req.Type == "" {
		req.Type = TypeTranslate
	}
	if req.TargetLang == "" {
		req.TargetLang = "zh-CN"
	}
	workers := req.Concurrency
	if workers <= 0 {
		workers = defaultBatchConcurrency
	}
	total := len(req.Items)
	if total == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "没有待翻译条目"}})
		return
	}

	// 分配任务 id
	s.batchMu.Lock()
	s.batchSeq++
	id := fmt.Sprintf("batch-%d-%d", time.Now().Unix(), s.batchSeq)
	task := &batchTask{id: id, total: total, results: make(map[int]ProgressEvent)}
	s.batchJobs[id] = task
	s.batchMu.Unlock()

	task.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	task.cancel = cancel
	task.running = true
	task.mu.Unlock()

	// 后台跑翻译（不阻塞本请求）
	go s.runBatchTask(ctx, task, req, workers)

	writeJSON(w, http.StatusOK, BatchStartResponse{TaskID: id, Total: total})
}

// runBatchTask 在后台执行批量翻译：worker 池并发消费条目，结果写入 task.results 并推进 done。
// 用传入的 ctx（任务的独立 context），取消任务即 cancel 它，worker 全部退出。
func (s *Service) runBatchTask(ctx context.Context, task *batchTask, req BatchRequest, workers int) {
	items := req.Items
	total := len(items)
	jobs := make(chan int)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				item := items[i]
				evt := ProgressEvent{Done: 0, Total: total, Index: i}
				tres, err := s.Translate(ctx, TranslateRequest{
					SourceText: item.Description,
					TargetLang: req.TargetLang,
					Model:      req.Model,
					Prompt:     req.Prompt,
					SourceType: item.SourceType,
					SourceID:   item.SourceID,
					Key:        item.Key,
					Type:       req.Type,
				})
				if err != nil {
					if ctx.Err() != nil {
						return // 任务被取消，不再推进
					}
					evt.Error = err.Error()
				} else if len(tres) > 0 {
					evt.Text = tres[0]
				}
				task.mu.Lock()
				if ctx.Err() != nil {
					task.mu.Unlock()
					return
				}
				task.done++
				evt.Done = task.done
				evt.Finished = task.done == total
				task.results[i] = evt
				task.mu.Unlock()
			}
		}()
	}
	// 投递任务，遇到取消就停
	go func() {
		defer close(jobs)
		for i := range items {
			select {
			case <-ctx.Done():
				return
			default:
			}
			jobs <- i
		}
	}()
	wg.Wait()

	task.mu.Lock()
	task.running = false
	if ctx.Err() != nil {
		task.cancelled = true
	} else {
		task.finished = true
	}
	task.results = nil // 任务结束，释放结果（进度靠 status 轮询即可）
	task.mu.Unlock()

	// 延迟清理任务记录，避免内存泄漏（完成/取消 60s 后移除）
	go func() {
		time.Sleep(60 * time.Second)
		s.batchMu.Lock()
		if t, ok := s.batchJobs[task.id]; ok && !t.running {
			delete(s.batchJobs, task.id)
		}
		s.batchMu.Unlock()
	}()
}

// handleBatchStatus 处理 GET /api/translate/batch/status?task_id=xxx。
func (s *Service) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	tid := r.URL.Query().Get("task_id")
	if tid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "缺少 task_id"}})
		return
	}
	s.batchMu.Lock()
	task, ok := s.batchJobs[tid]
	s.batchMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "任务不存在或已过期"}})
		return
	}
	task.mu.Lock()
	resp := BatchStatusResponse{TaskID: task.id, Done: task.done, Total: task.total, Running: task.running, Finished: task.finished, Cancelled: task.cancelled, Error: task.err}
	task.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// handleBatchCancel 处理 POST /api/translate/batch/cancel?task_id=xxx。
func (s *Service) handleBatchCancel(w http.ResponseWriter, r *http.Request) {
	tid := r.URL.Query().Get("task_id")
	if tid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "缺少 task_id"}})
		return
	}
	s.batchMu.Lock()
	task, ok := s.batchJobs[tid]
	s.batchMu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "任务不存在或已过期"}})
		return
	}
	task.mu.Lock()
	if task.cancel != nil {
		task.cancel()
	}
	task.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"task_id": tid, "cancelled": true})
}

// ---- 来源清单 ----

// handleSources 处理 GET /api/translate/sources。
func (s *Service) handleSources(w http.ResponseWriter, r *http.Request) {
	items, err := s.collectSources(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
