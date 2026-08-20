package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/billing"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/credentials"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"
)

// 推荐从环境变量读取 AK/SK，不要硬编码到代码里
var (
	AccessKeyID     = getenv("VOLC_ACCESSKEY", "***")
	SecretAccessKey = getenv("VOLC_SECRETKEY", "***")
	Region          = "cn-beijing"
	MaxPages        = 50 // 翻页死循环防护上限
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// PackageEntry 包装：给 SDK 返回的资源包补上查询来源（类型/状态），便于 JSON 里区分
type PackageEntry struct {
	ResourceType string `json:"resource_type"`
	*billing.ListForListResourcePackagesOutput
}

func maskAK(ak string) string {
	if len(ak) <= 8 {
		return "***"
	}
	return ak[:4] + "****" + ak[len(ak)-4:]
}

// newHTTPClient 带各阶段超时，避免网络问题导致无限挂起
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}

func main() {
	// 子命令分发：go run . cache list|probe|delete|add
	if len(os.Args) > 1 && os.Args[1] == "cache" {
		if err := runCacheCommand(os.Args[2:]); err != nil {
			log.Fatalf("[CACHE] %v", err)
		}
		return
	}

	log.Printf("[CONFIG] AK=%s (掩码) Region=%s", maskAK(AccessKeyID), Region)
	if p := os.Getenv("HTTPS_PROXY"); p != "" {
		log.Printf("[CONFIG] 检测到 HTTPS_PROXY=%q", p)
	}

	httpClient := newHTTPClient()

	// 连通性预检（网络不可达时直接提示，不用等 API 超时）
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	preq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.volcengineapi.com/", nil)
	if err != nil {
		log.Printf("[PREFLIGHT] 构造请求失败: %v", err)
		os.Exit(1)
	}
	resp, err := httpClient.Do(preq)
	cancel()
	if err != nil {
		log.Printf("[PREFLIGHT] 连通性失败: %v", err)
		os.Exit(1)
	}
	resp.Body.Close()
	log.Printf("[PREFLIGHT] 连通性 OK (HTTP %d)", resp.StatusCode)

	// 初始化 session
	config := volcengine.NewConfig().
		WithRegion(Region).
		WithCredentials(credentials.NewStaticCredentials(AccessKeyID, SecretAccessKey, "")).
		WithHTTPClient(httpClient).
		WithMaxRetries(0) // 关闭 SDK 重试，失败即报错

	sess, err := session.NewSession(config)
	if err != nil {
		log.Printf("[SDK] new session failed: %v", err)
		os.Exit(1)
	}
	client := billing.New(sess)

	// 翻页拉取全部资源包：3 种资源类型 × 全部状态（官方单页上限 20，只能翻页）
	resourceTypes := []string{"Package", "RI", "RSC"} // Package=资源包；RI=预留实例券；RSC=预留存储容量包
	maxResults := "20"
	all := []PackageEntry{}
	seen := map[string]bool{} // InstanceNo 全局去重，防游标循环

	pages := 0
	for _, rt := range resourceTypes {
		log.Printf("[API] 查询资源类型 %s（全部状态）...", rt)
		var nextToken *string
		for page := 1; page <= MaxPages; page++ {
			pages++
			req := &billing.ListResourcePackagesInput{
				ResourceType: volcengine.String(rt),
				MaxResults:   &maxResults,
				NextToken:    nextToken,
			}
			resp, err := client.ListResourcePackages(req)
			if err != nil {
				log.Printf("[API] %s 第 %d 页调用失败: %v", rt, page, err)
				os.Exit(1)
			}
			pageLen := 0
			if resp.List != nil {
				for _, p := range resp.List {
					key := str(p.InstanceNo)
					if key != "" {
						if seen[key] {
							continue // 游标循环重复，跳过
						}
						seen[key] = true
					}
					all = append(all, PackageEntry{ResourceType: rt, ListForListResourcePackagesOutput: p})
					pageLen++
				}
			}
			// 终止条件：本页 0 条（含去重后 0 条）或 NextToken 为空
			if pageLen == 0 || resp.NextToken == nil || *resp.NextToken == "" {
				break
			}
			nextToken = resp.NextToken
		}
	}
	log.Printf("[API] 翻页完成: %d 次请求, 去重后 %d 个资源包", pages, len(all))

	// 4. 输出完整数据到 JSON 文件（每个资源包的全部字段）
	jsonData, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		log.Printf("[OUT] JSON 序列化失败: %v", err)
		os.Exit(1)
	}
	outPath := "resource_packages.json"
	if err := os.WriteFile(outPath, jsonData, 0644); err != nil {
		log.Printf("[OUT] 写入文件失败: %v", err)
		os.Exit(1)
	}
	absPath, _ := filepath.Abs(outPath)
	log.Printf("[OUT] 完整数据已写入 %s (%d 条, %.1f KB)", absPath, len(all), float64(len(jsonData))/1024)

	// 5. 控制台统计与简表
	fmt.Printf("\n资源包总数: %d 个（查询范围: 3 种资源类型 × 全部状态）\n", len(all))

	typeCount := map[string]int{}
	statusCount := map[string]int{}
	for _, e := range all {
		typeCount[e.ResourceType]++
		statusCount[str(e.Status)]++
	}
	fmt.Printf("按类型: Package=%d, RI=%d, RSC=%d\n", typeCount["Package"], typeCount["RI"], typeCount["RSC"])
	fmt.Print("按状态:")
	for _, s := range []string{"Effective", "NotEffective", "UsedUp", "Expired", "Refunded", "FailedToCreate"} {
		if n := statusCount[s]; n > 0 {
			fmt.Printf(" %s=%d", s, n)
		}
	}
	fmt.Println()

	fmt.Printf("\n%-4s %-6s %-26s %-14s %-14s %-10s %-10s\n", "#", "类型", "产品名称", "商品编码", "总额度", "剩余量", "状态")
	fmt.Println(strings.Repeat("-", 100))

	var arkCount int
	for i, e := range all {
		p := e.ListForListResourcePackagesOutput
		product := str(p.Product)
		name := str(p.ProductName)
		isArk := strings.Contains(strings.ToLower(product), "ark") ||
			strings.Contains(name, "方舟") ||
			strings.Contains(strings.ToLower(name), "doubao") ||
			strings.Contains(strings.ToLower(name), "大模型")
		if isArk {
			arkCount++
		}
		mark := " "
		if isArk {
			mark = "*"
		}
		fmt.Printf("%-4s %-6s %-26s %-14s %-14s %-10s %-10s\n",
			fmt.Sprintf("%s%d", mark, i+1),
			e.ResourceType,
			truncate(name, 26),
			truncate(product, 14),
			str(p.TotalAmount),
			str(p.AvailableAmount),
			truncate(str(p.Status), 10))
	}

	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("其中疑似方舟(Ark)相关资源包: %d 个\n", arkCount)
	fmt.Printf("完整字段 JSON 见: %s\n", absPath)
	fmt.Println("\n提示: 星号(*)为按 Product/名称匹配到 ark/doubao/大模型/方舟 的条目；")
	fmt.Println("     ListResourcePackages 是资源包维度（单页上限 20，只能翻页），不含按模型的 token 剩余明细；")
	fmt.Println("     失效超过 18 个月的资源包接口不返回；接口 QPS 限制 10。")
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
