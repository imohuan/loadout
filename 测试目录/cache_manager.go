package main

// 火山方舟上下文缓存管理模块
//
// 背景：官方没有"列出我的全部缓存"的 API（Context API 只有 Create/Chat；
// Responses API 只有 GetResponses 单查 / DeleteResponse 删除）。
// 因此"查询"依赖本地注册表：创建缓存时把 response_id 记下来，
// 之后用 GetResponses 逐个探测存活状态；"删除"用官方 DeleteResponse。
//
// 子命令（挂在 main 的 `cache` 下）：
//   go run . cache list                  # 列出本地登记的缓存 + 探测存活状态
//   go run . cache probe <response_id>   # 探测单个缓存是否还存活
//   go run . cache delete <response_id>  # 删除缓存（官方 API），并移除本地登记
//   go run . cache add <response_id> [model] [note]  # 手动登记一个缓存（可选）
//
// 鉴权：缓存管理走 arkruntime（API Key），不是 billing SDK（AK/SK）。
// 请设置环境变量 ARK_API_KEY。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/arkruntime"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
)

const (
	cacheRegistryFile = "cache_registry.json"
	arkBaseURL        = "https://ark.cn-beijing.volces.com/api/v3"
)

// CacheEntry 本地登记的一条上下文缓存
type CacheEntry struct {
	ResponseID string    `json:"response_id"`  // Responses API 返回的 id（缓存挂在此 ID 上）
	Model      string    `json:"model"`        // 模型 / 接入点 ID
	CreatedAt  time.Time `json:"created_at"`   // 本地登记时间（非服务端创建时间）
	TTLSeconds int64     `json:"ttl_seconds"`  // 创建时配置的 TTL（0=未配置）
	Note       string    `json:"note,omitempty"` // 备注（可选）
}

// CacheRegistry 本地缓存注册表（官方无 List API，只能自建索引）
type CacheRegistry struct {
	Entries []CacheEntry `json:"entries"`
}

// ---------- 注册表读写 ----------

// envOr 读取环境变量，为空时返回默认值（本文件自洽，不依赖 main.go 的 getenv）
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadCacheRegistry() (*CacheRegistry, error) {
	data, err := os.ReadFile(cacheRegistryFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &CacheRegistry{}, nil
		}
		return nil, err
	}
	reg := &CacheRegistry{}
	if err := json.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", cacheRegistryFile, err)
	}
	return reg, nil
}

func (r *CacheRegistry) save() error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cacheRegistryFile, data, 0644)
}

// ---------- arkruntime 客户端 ----------

func newArkClient() (*arkruntime.Client, error) {
	apiKey := envOr("ARK_API_KEY", "")
	if apiKey == "" {
		return nil, errors.New("环境变量 ARK_API_KEY 未设置（缓存管理走 API Key 鉴权，非 AK/SK）")
	}
	// 注意：SDK 注释明确 Responses API 暂不支持 AK/SK，只能 API Key
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return arkruntime.NewClientWithApiKey(apiKey, arkruntime.WithBaseUrl(arkBaseURL), arkruntime.WithHTTPClient(httpClient)), nil
}

// ---------- 探测 / 删除 ----------

// isNotFound 判断错误是否为 404（缓存已过期/已删除）
func isNotFound(err error) bool {
	var reqErr *model.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode == http.StatusNotFound
	}
	return false
}

// probeResponse 探测单个 response 是否仍存活。
// 返回 (存活, 错误)；404 视为"已不存在"而非错误。
func probeResponse(ctx context.Context, client *arkruntime.Client, responseID string) (bool, error) {
	resp, err := client.GetResponses(ctx, responseID, nil)
	if err != nil {
		if isNotFound(err) {
			return false, nil // 已删除或已过期
		}
		return false, err
	}
	// 拿到对象即说明服务端仍保留该 response（缓存挂在其上）
	return resp != nil && resp.Id != "", nil
}

// deleteResponse 删除缓存（官方 DeleteResponse API）
func deleteResponse(ctx context.Context, client *arkruntime.Client, responseID string) error {
	return client.DeleteResponse(ctx, responseID)
}

// ---------- 子命令实现 ----------

// runCacheCommand 入口：args = os.Args[2:]
func runCacheCommand(args []string) error {
	if len(args) == 0 {
		printCacheUsage()
		return nil
	}

	client, err := newArkClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch args[0] {
	case "list":
		return cmdCacheList(ctx, client)
	case "probe":
		if len(args) < 2 {
			return errors.New("用法: cache probe <response_id>")
		}
		return cmdCacheProbe(ctx, client, args[1])
	case "delete":
		if len(args) < 2 {
			return errors.New("用法: cache delete <response_id>")
		}
		return cmdCacheDelete(ctx, client, args[1])
	case "add":
		return cmdCacheAdd(args[1:])
	case "help", "-h", "--help":
		printCacheUsage()
		return nil
	default:
		return fmt.Errorf("未知子命令: %s\n\n%s", args[0], cacheUsageText())
	}
}

func printCacheUsage() {
	fmt.Print(cacheUsageText())
}

func cacheUsageText() string {
	return `火山方舟上下文缓存管理

用法:
  cache list                   列出本地登记的缓存 + 探测每个是否还存活
  cache probe <response_id>    探测单个缓存是否存活（不依赖本地登记）
  cache delete <response_id>   删除缓存（官方 DELETE /responses/{id}），并移除本地登记
  cache add <response_id> [model] [note]  手动登记一个缓存（可选，用于溯源）

说明:
  官方没有"列出全部缓存"的 API，本地登记表是唯一查询途径。
  删除走官方接口；Context API 创建的缓存无法手动删除，只能等 TTL 过期。
  需要环境变量 ARK_API_KEY。
`
}

// cmdCacheList 列出本地登记的缓存并逐个探测
func cmdCacheList(ctx context.Context, client *arkruntime.Client) error {
	reg, err := loadCacheRegistry()
	if err != nil {
		return err
	}
	if len(reg.Entries) == 0 {
		fmt.Println("本地缓存登记表为空。")
		fmt.Println("提示: 官方没有 List 缓存 API，请用 `cache add` 手动登记，或接入时自动登记 response_id。")
		return nil
	}

	fmt.Printf("%-28s %-28s %-19s %-10s %-8s %s\n", "RESPONSE_ID", "MODEL", "登记时间", "TTL(s)", "状态", "备注")
	fmt.Println(strings.Repeat("-", 120))

	aliveCount := 0
	for _, e := range reg.Entries {
		alive, err := probeResponse(ctx, client, e.ResponseID)
		status := "存活"
		switch {
		case err != nil:
			status = "探测失败"
		case !alive:
			status = "已过期/已删"
		default:
			aliveCount++
		}
		ttl := fmt.Sprintf("%d", e.TTLSeconds)
		if e.TTLSeconds == 0 {
			ttl = "-"
		}
		fmt.Printf("%-28s %-28s %-19s %-10s %-8s %s\n",
			truncateRune(e.ResponseID, 28),
			truncateRune(e.Model, 28),
			e.CreatedAt.Format("2006-01-02 15:04"),
			ttl, status, truncateRune(e.Note, 30))
		if err != nil {
			fmt.Printf("  └─ 探测错误: %v\n", err)
		}
	}
	fmt.Println(strings.Repeat("-", 120))
	fmt.Printf("共 %d 条登记，其中 %d 条仍存活。\n", len(reg.Entries), aliveCount)
	return nil
}

// cmdCacheProbe 探测单个缓存（不依赖本地登记）
func cmdCacheProbe(ctx context.Context, client *arkruntime.Client, responseID string) error {
	alive, err := probeResponse(ctx, client, responseID)
	if err != nil {
		return fmt.Errorf("探测 %s 失败: %w", responseID, err)
	}
	if alive {
		fmt.Printf("%s 存活 ✓（服务端仍保留该 response 及其缓存）\n", responseID)
	} else {
		fmt.Printf("%s 已不存在（已删除或已超过保留期，无存储费用）\n", responseID)
	}
	return nil
}

// cmdCacheDelete 删除缓存并移除本地登记
func cmdCacheDelete(ctx context.Context, client *arkruntime.Client, responseID string) error {
	if err := deleteResponse(ctx, client, responseID); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", responseID, err)
	}
	fmt.Printf("已删除缓存 %s ✓（不再产生存储费用）\n", responseID)

	// 同步移除本地登记
	reg, err := loadCacheRegistry()
	if err != nil {
		return err
	}
	removed := false
	filtered := reg.Entries[:0]
	for _, e := range reg.Entries {
		if e.ResponseID == responseID {
			removed = true
			continue
		}
		filtered = append(filtered, e)
	}
	if removed {
		reg.Entries = filtered
		if err := reg.save(); err != nil {
			log.Printf("[CACHE] 本地登记移除失败: %v", err)
		} else {
			fmt.Println("已同步移除本地登记。")
		}
	}
	return nil
}

// cmdCacheAdd 手动登记一个缓存
func cmdCacheAdd(args []string) error {
	if len(args) < 1 {
		return errors.New("用法: cache add <response_id> [model] [note]")
	}
	entry := CacheEntry{
		ResponseID: args[0],
		CreatedAt:  time.Now(),
	}
	if len(args) >= 2 {
		entry.Model = args[1]
	}
	if len(args) >= 3 {
		entry.Note = strings.Join(args[2:], " ")
	}

	reg, err := loadCacheRegistry()
	if err != nil {
		return err
	}
	// 防重复登记
	for _, e := range reg.Entries {
		if e.ResponseID == entry.ResponseID {
			return fmt.Errorf("该 response_id 已登记过: %s", entry.ResponseID)
		}
	}
	reg.Entries = append(reg.Entries, entry)
	if err := reg.save(); err != nil {
		return fmt.Errorf("保存登记表失败: %w", err)
	}
	fmt.Printf("已登记缓存 %s (model=%s)\n", entry.ResponseID, entry.Model)
	return nil
}

// truncateRune 按字符截断（与 main.go 的 truncate 逻辑一致，避免命名冲突）
func truncateRune(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:1])
	}
	return string(runes[:n-1]) + "…"
}
