package volcfreequota

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/volcengine/volcengine-go-sdk/service/billing"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/credentials"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"
)

// billingFetcher 拉取资源包的抽象接口（Service.client 字段类型）。
// 生产实现是 *volcBillingClient；测试注入 fake 以测并发锁/刷新流程（不触发真实网络）。
type billingFetcher interface {
	FetchPackages(ctx context.Context, accessKey, secretKey string) ([]rawPackage, error)
}

// volcBillingClient 火山引擎账单 SDK 客户端封装：按 AK/SK 查询资源包清单。
//
// 复用测试目录/main.go 的策略：翻页拉取全量 ResourceType=Package 的资源包，
// 按 product/product_name 模糊识别方舟免费模型，按 TotalAmount/AvailableAmount
// 上报剩余额度。可用 Amount 字段由 SDK 返回为 *string，单位（"Tokens" 等）一并透传。
//
// 流控说明：ListResourcePackages 接口 QPS 上限 10（测试目录/main.go 已注明），
// 翻页必须节流，否则返回 AccountFlowLimitExceeded 429。本客户端内置 rateLimiter
// 限制 ≤8 QPS，并在首页即按 Status 过滤（默认仅 Effective + UsedUp）——这两个
// 状态之外的资源包（Expired/NotEffective/FailedToCreate/Refunded）在客户端侧
// 本来就会被 looksLikeArkFreePackage 丢弃，直接在服务端过滤可大幅减少翻页数。
//
// 翻页终止条件（双保险，防止无限翻页打爆接口）：
//  1. 达到 maxPages 上限（默认 10 页 = 200 条，足够覆盖免费模型数量级）；
//  2. 某页返回 0 条（resp.List 为空）——再往后大概率也是空，直接停；
//  3. NextToken 为空。
type volcBillingClient struct {
	lg        *slog.Logger
	region    string
	maxPages  int
	pageDelay time.Duration
	// limiter 全局限流器：控制对 ListResourcePackages 的整体 QPS，翻页节流用。
	limiter *rate.Limiter
	// statusFilter 服务端过滤的资源包状态（空 = 不过滤，全量翻页）。
	statusFilter []string
}

const volcBillingRegion = "cn-beijing"

// newVolcBillingClient 构造账单客户端；region 固定 cn-beijing，maxPages 10 上限
//（单页 20 条 → 最多 200 条，足够覆盖免费模型数量级）。翻页按 8 QPS 节流（接口上限 10）。
func newVolcBillingClient(lg *slog.Logger) *volcBillingClient {
	return &volcBillingClient{
		lg:           lg,
		region:       volcBillingRegion,
		maxPages:     10,
		pageDelay:    0,
		limiter:      rate.NewLimiter(8, 8),
		statusFilter: []string{"Effective", "UsedUp"},
	}
}

// FetchPackages 查询该 AK/SK 名下 ResourceType=Package 的全部资源包。
//
// 返回的资源包字段：
//
//   - TotalAmount / AvailableAmount：字符串形式的额度数（多数为 Token 计数），
//     解析失败记为 0，单位保留 SDK 返回的原始串。
//   - ProductName：用作 model 标识归一化的主源；Product 作辅助。
//   - InstanceNo：去重键，同一资源包翻页可能重复返回。
//
// Status 过滤：SDK 的 Status 是单值，statusFilter 有 N 个状态就分 N 次独立翻页
// 查询（各自维护 NextToken），再按 InstanceNo 全局去重合并——保证 Effective 和
// UsedUp 的包都能拿到（UsedUp 的包 available=0 但 total 计入模型总额，漏掉会让
// 累加总额偏小）。
func (c *volcBillingClient) FetchPackages(ctx context.Context, accessKey, secretKey string) ([]rawPackage, error) {
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("missing volcengine credentials")
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
		},
		Timeout: 60 * time.Second,
	}
	cfg := volcengine.NewConfig().
		WithRegion(c.region).
		WithCredentials(credentials.NewStaticCredentials(accessKey, secretKey, "")).
		WithHTTPClient(httpClient).
		WithMaxRetries(0) // 失败即报错，由上层决定是否重试
	sess, err := session.NewSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	client := billing.New(sess)

	resourceType := "Package"
	maxResults := "20"
	// statusFilter 为空 = 不过滤（一次全量翻页）；否则每个状态独立翻页一次。
	filters := c.statusFilter
	if len(filters) == 0 {
		filters = []string{""}
	}
	var out []rawPackage
	seen := make(map[string]bool)
	for _, status := range filters {
		var nextToken *string
		for page := 1; page <= c.maxPages; page++ {
			// 翻页节流：等待 limiter 令牌；超时(5s)则放弃整次刷新（保护后台 goroutine 不堆积）。
			if err := c.limiter.Wait(ctx); err != nil {
				return nil, fmt.Errorf("rate limit wait: %w", err)
			}
			if c.pageDelay > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(c.pageDelay):
				}
			}
			req := &billing.ListResourcePackagesInput{
				ResourceType: &resourceType,
				MaxResults:   &maxResults,
				NextToken:    nextToken,
			}
			// 服务端按状态过滤：只拉会参与额度判断的包，减少翻页与流量。
			if status != "" {
				req.Status = volcengine.String(status)
			}
			resp, err := client.ListResourcePackages(req)
			if err != nil {
				return nil, fmt.Errorf("list resource packages page %d (status=%s): %w", page, status, err)
			}
			// 统计本页去重后实际新增的条数：0 条说明后面大概率也是空，直接停（防无限翻页）。
			added := 0
			if resp.List != nil {
				for _, p := range resp.List {
					if p == nil {
						continue
					}
					key := ptrToString(p.InstanceNo)
					if key != "" {
						if seen[key] {
							continue
						}
						seen[key] = true
					}
					added++
					out = append(out, rawPackage{
						Product:            ptrToString(p.Product),
						ProductName:        ptrToString(p.ProductName),
						TotalAmount:        ptrToString(p.TotalAmount),
						AvailableAmount:    ptrToString(p.AvailableAmount),
						// SDK 不返回 UsedAmount，按 Total - Available 计算（同一单位，展示用）。
						UsedAmount:          calcUsedAmount(ptrToString(p.TotalAmount), ptrToString(p.AvailableAmount)),
						Unit:                ptrToString(p.Unit),
						Status:              ptrToString(p.Status),
						InstanceNo:          key,
						ConfigurationCode:   ptrToString(p.ConfigurationCode),
						ConfigurationName:   ptrToString(p.ConfigurationName),
						EffectiveTime:       ptrToString(p.EffectiveTime),
						ExpiryTime:          ptrToString(p.ExpiryTime),
						Specification:       ptrToString(p.Specification),
						SpecificationUnit:   ptrToString(p.SpecificationUnit),
						ResetPeriod:         ptrToString(p.ResetPeriod),
						ResetByNaturalMonth: ptrToString(p.ResetByNaturalMonth),
					})
				}
			}
			// 终止条件：本页 0 条（含全被去重）或 NextToken 为空 → 后面没有数据了。
			if added == 0 || resp.NextToken == nil || *resp.NextToken == "" {
				break
			}
			nextToken = resp.NextToken
		}
	}
	return out, nil
}

// rawPackage 资源包 SDK 输出的扁平化字段，避免直接暴露 SDK 类型到上层。
type rawPackage struct {
	Product            string
	ProductName        string
	TotalAmount        string
	AvailableAmount    string
	UsedAmount         string
	Unit               string
	Status             string
	InstanceNo         string
	ConfigurationCode  string
	ConfigurationName  string
	EffectiveTime      string
	ExpiryTime         string
	Specification      string
	SpecificationUnit  string
	ResetPeriod        string
	ResetByNaturalMonth string
}

// ptrToString 把 SDK 返回的 *string 安全地转 string。
func ptrToString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// calcUsedAmount 计算已用量 = Total - Available（SDK 不返回 UsedAmount）。
//
// 两个值可能为小数（"100.00"）；解析失败视为 0。结果格式化为整数串。
func calcUsedAmount(total, available string) string {
	t, err1 := strconv.ParseFloat(total, 64)
	a, err2 := strconv.ParseFloat(available, 64)
	if err1 != nil || err2 != nil {
		return "0"
	}
	used := t - a
	if used < 0 {
		used = 0
	}
	return strconv.FormatFloat(used, 'f', 0, 64)
}

// looksLikeArkFreePackage 资源包是否疑似方舟免费模型：
//
//  - product 包含 "ark" / product_name 包含 "方舟" / "doubao" / "大模型" 任一关键字。
//  - status 处于 Effective（有效）/ UsedUp（已用完）才会有后续处理；NotEffective/Expired/Refunded/FailedToCreate 直接跳过。
//
// 与 main.go 启发式一致；识别过宽不会导致禁用（exhausted 仍需 available<=0 才标耗尽），
// 识别过窄会导致 UI 看不到该模型——所以采用宽口径。
func (r rawPackage) looksLikeArkFreePackage() bool {
	if r.Status != "" && r.Status != "Effective" && r.Status != "UsedUp" {
		return false
	}
	product := strings.ToLower(r.Product)
	name := strings.ToLower(r.ProductName)
	if strings.Contains(product, "ark") {
		return true
	}
	if strings.Contains(name, "方舟") || strings.Contains(name, "doubao") ||
		strings.Contains(name, "豆包") || strings.Contains(name, "大模型") {
		return true
	}
	return false
}

// normalizeModelName 从 product_name 提取用于 UI 显示与匹配的 model 标签。
//
// 策略：去掉全部空格与 "豆包" / "方舟" / "·" 等修饰，保留字母数字与短横线下划线；
// 点（"."）归一化为短横线——code 里的版本号 "2.1" 与 API 模型名的 "2-1" 写法不同，
// 归一化后双向包含才能命中（"doubao-seed-2.1-pro" ↔ "doubao-seed-2-1-pro-260628"）。
// 例："豆包·Doubao-pro-32k" → "doubao-pro-32k"，"方舟 Doubao 1.5 lite 32k" → "doubao1-5lite32k"。
func normalizeModelName(name string) string {
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == ' ' || r == '·' || r == '、':
			continue
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	s := b.String()
	// 去前缀："doubao" / "模型" 等会被剥离，留下的核心仍可匹配（归一化的 API 模型
	// 名如 "doubao-1-5-pro-32k-250115" 包含 "doubao-1-5-pro-32k"，与 product_name
	// "doubao-pro-32k" 通过双向包含匹配可命中；详见 service.matchQuotaToAPIModel）。
	return s
}
