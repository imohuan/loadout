package multimodalmcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
)

// newDiscardLogger 返回丢弃所有日志的 logger（测试用）。
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTransport 是一个纯内存 RoundTripper：不走真实网络，直接把请求交给 handler
// 逻辑构造响应返回。用于在 Windows 上可靠地 mock 方舟 Files API（规避 httptest
// server + 重定向 transport 的连接/死锁问题）。
type fakeTransport struct {
	mu      sync.Mutex
	handler func(r *http.Request, reqBody []byte) *fakeResponse
	// 记录每次请求的方法、Authorization、body（供断言）。
	Methods []string
	Auths   []string
	Bodies  []string
	// 触发后返回 true 时，将该请求视为"不可达"（模拟连接失败），可让测试覆盖网络错误。
	fail bool
}

type fakeResponse struct {
	status int
	body   string
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, io.EOF
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.Methods = append(f.Methods, r.Method)
	f.Auths = append(f.Auths, r.Header.Get("Authorization"))
	f.Bodies = append(f.Bodies, string(body))
	if f.handler == nil {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	}
	resp := f.handler(r, body)
	return &http.Response{
		StatusCode: resp.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(resp.body)),
		Request:    r,
	}, nil
}

// installFake 把包级 arkHTTPClient 替换为用指定 handler 的 fake transport，返回可断言的 transport。
func installFake(t *testing.T, handler func(r *http.Request, reqBody []byte) *fakeResponse) *fakeTransport {
	t.Helper()
	ft := &fakeTransport{handler: handler}
	old := arkHTTPClient
	arkHTTPClient = &http.Client{Transport: ft}
	t.Cleanup(func() { arkHTTPClient = old })
	return ft
}

// newTestService 构造带真实 Store（临时目录）+ Repository（内存 sqlite）的 Service，
// 并插入一个方舟渠道（BaseURL=ark 域名，key 经 Encrypt 加密）。
func newTestService(t *testing.T, key string) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	cipher, err := st.Encrypt(key)
	if err != nil {
		t.Fatalf("st.Encrypt: %v", err)
	}
	// 真实方舟域名（非 mock host），host 以 .volces.com 结尾以通过识别。
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{{
		ID:            "ark-1",
		Name:          "ArkKey1",
		BaseURL:       "https://ark.cn-beijing.volces.com/api/v3",
		APIKeyCipher:  cipher,
		ManualEnabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}
	return NewService(st, repo, newDiscardLogger())
}

func TestArkAPIBase(t *testing.T) {
	cases := []struct {
		base    string
		apiBase string
		ok      bool
	}{
		{"https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v3", true},
		{"https://ark.cn-beijing.volces.com", "https://ark.cn-beijing.volces.com/api/v3", true},
		{"http://ark.volces.com/api/v3/", "http://ark.volces.com/api/v3", true},
		{"https://openai.com/api/v1", "", false},
		{"", "", false},
		{"not a url", "", false},
	}
	for _, c := range cases {
		apiBase, ok := arkAPIBase(c.base)
		if ok != c.ok || apiBase != c.apiBase {
			t.Errorf("arkAPIBase(%q) = (%q,%v), want (%q,%v)", c.base, apiBase, ok, c.apiBase, c.ok)
		}
	}
}

// TestUploadAndGetIDSuccess 验证：识别方舟渠道取 key → multipart 上传拿 file_id → 轮询 active。
func TestUploadAndGetIDSuccess(t *testing.T) {
	ft := installFake(t, func(r *http.Request, reqBody []byte) *fakeResponse {
		if r.Method == http.MethodPost {
			return &fakeResponse{status: 200, body: `{"id":"file-abc123","status":"processing"}`}
		}
		return &fakeResponse{status: 200, body: `{"id":"file-abc123","status":"active"}`}
	})

	svc := newTestService(t, "secret-key")
	fileID, err := svc.uploadAndGetID(context.Background(), "video/mp4", []byte("fake-mp4-bytes"), "clip.mp4")
	if err != nil {
		t.Fatalf("uploadAndGetID: %v", err)
	}
	if fileID != "file-abc123" {
		t.Fatalf("fileID = %q, want file-abc123", fileID)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.Methods) != 2 || ft.Methods[0] != http.MethodPost || ft.Methods[1] != http.MethodGet {
		t.Fatalf("methods = %v, want [POST GET]", ft.Methods)
	}
	for _, a := range ft.Auths {
		if a != "Bearer secret-key" {
			t.Errorf("Authorization = %q, want Bearer secret-key", a)
		}
	}
	if len(ft.Bodies) < 1 || ft.Bodies[0] == "" {
		t.Fatalf("expected an upload body, got %d bodies: %v", len(ft.Bodies), ft.Bodies)
	}
	if !strings.Contains(ft.Bodies[0], `name="purpose"`) || !strings.Contains(ft.Bodies[0], "user_data") {
		t.Errorf("upload body missing purpose=user_data: %s", ft.Bodies[0])
	}
	if !strings.Contains(ft.Bodies[0], `filename="clip.mp4"`) {
		t.Errorf("upload body missing filename clip.mp4: %s", ft.Bodies[0])
	}
	if !strings.Contains(ft.Bodies[0], "fake-mp4-bytes") {
		t.Errorf("upload body missing file bytes: %s", ft.Bodies[0])
	}
}

// TestUploadAndGetIDPollWait 验证：轮询首轮 processing、次轮 active，最终返回 file_id。
func TestUploadAndGetIDPollWait(t *testing.T) {
	var pollCount int
	ft := installFake(t, func(r *http.Request, reqBody []byte) *fakeResponse {
		if r.Method == http.MethodPost {
			return &fakeResponse{status: 200, body: `{"id":"file-p","status":"processing"}`}
		}
		pollCount++
		if pollCount == 1 {
			return &fakeResponse{status: 200, body: `{"id":"file-p","status":"processing"}`}
		}
		return &fakeResponse{status: 200, body: `{"id":"file-p","status":"active"}`}
	})
	_ = ft
	oldInterval := uploadPollInterval
	uploadPollInterval = time.Millisecond
	t.Cleanup(func() { uploadPollInterval = oldInterval })

	svc := newTestService(t, "k")
	fileID, err := svc.uploadAndGetID(context.Background(), "audio/mpeg", []byte("wav"), "a.wav")
	if err != nil {
		t.Fatalf("uploadAndGetID: %v", err)
	}
	if fileID != "file-p" {
		t.Fatalf("fileID = %q, want file-p", fileID)
	}
	if pollCount != 2 {
		t.Fatalf("pollCount = %d, want 2", pollCount)
	}
}

// TestUploadAndGetIDUploadFailure 验证：上传 HTTP 失败返回明确错误。
func TestUploadAndGetIDUploadFailure(t *testing.T) {
	installFake(t, func(r *http.Request, reqBody []byte) *fakeResponse {
		return &fakeResponse{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`}
	})

	svc := newTestService(t, "k")
	_, err := svc.uploadAndGetID(context.Background(), "video/mp4", []byte("data"), "x.mp4")
	if err == nil {
		t.Fatal("expected upload failure error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention HTTP status, got: %v", err)
	}
}

// TestUploadAndGetIDPollTimeout 验证：轮询一直 processing 时超时返回错误。
func TestUploadAndGetIDPollTimeout(t *testing.T) {
	installFake(t, func(r *http.Request, reqBody []byte) *fakeResponse {
		if r.Method == http.MethodPost {
			return &fakeResponse{status: 200, body: `{"id":"file-t","status":"processing"}`}
		}
		return &fakeResponse{status: 200, body: `{"id":"file-t","status":"processing"}`}
	})
	oldTimeout, oldInterval := uploadPollTimeout, uploadPollInterval
	uploadPollTimeout = 20 * time.Millisecond
	uploadPollInterval = time.Millisecond
	t.Cleanup(func() {
		uploadPollTimeout = oldTimeout
		uploadPollInterval = oldInterval
	})

	svc := newTestService(t, "k")
	_, err := svc.uploadAndGetID(context.Background(), "video/mp4", []byte("data"), "x.mp4")
	if err == nil {
		t.Fatal("expected poll timeout error")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("error should mention 超时, got: %v", err)
	}
}

// TestUploadAndGetIDNetworkError 验证：上传请求网络失败（连接不可达）返回明确错误。
func TestUploadAndGetIDNetworkError(t *testing.T) {
	ft := installFake(t, nil)
	ft.mu.Lock()
	ft.fail = true
	ft.mu.Unlock()

	svc := newTestService(t, "k")
	_, err := svc.uploadAndGetID(context.Background(), "video/mp4", []byte("data"), "x.mp4")
	if err == nil {
		t.Fatal("expected network error")
	}
	if !strings.Contains(err.Error(), "上传请求失败") {
		t.Errorf("error should mention upload request failure, got: %v", err)
	}
}

// TestUploadAndGetIDNoArkChannel 验证：无方舟渠道时返回明确错误。
func TestUploadAndGetIDNoArkChannel(t *testing.T) {
	installFake(t, nil)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	// 插入一个非方舟渠道（openai），无 volces.com。
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{{
		ID:            "openai-1",
		Name:          "OpenAI",
		BaseURL:       "https://openai.com/api/v1",
		APIKeyCipher:  "",
		ManualEnabled: true,
	}}); err != nil {
		t.Fatalf("ReplaceChannels: %v", err)
	}
	svc := NewService(st, repo, newDiscardLogger())
	_, err = svc.uploadAndGetID(context.Background(), "video/mp4", []byte("data"), "x.mp4")
	if err == nil {
		t.Fatal("expected no-ark-channel error")
	}
	if !strings.Contains(err.Error(), "未找到启用的方舟") {
		t.Errorf("error should mention no ark channel, got: %v", err)
	}
}
