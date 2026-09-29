package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/model"
	"pansou/stats"
)

// 统计库与记录器；为 nil 时所有记录都是空操作，搜索照常进行。
var (
	statsStore    *stats.Store
	statsRecorder *stats.Recorder
)

// SetStats 注入统计库与记录器。
func SetStats(s *stats.Store, r *stats.Recorder) {
	statsStore, statsRecorder = s, r
}

// searchSessionHeader 由前端在一次搜索的所有请求（预热 + 多轮补齐）上携带同一个值，
// 使这些请求在统计中合并为一次搜索。
const searchSessionHeader = "X-Search-Session"

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// searchSessionID 返回请求携带的合法会话 ID；缺失或非法时生成一个新的，
// 使直接调用接口的客户端每个请求各算一次搜索。
func searchSessionID(c *gin.Context) string {
	if id := c.GetHeader(searchSessionHeader); sessionIDPattern.MatchString(id) {
		return id
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf)
}

// sessionKey 把会话 ID 与关键词绑定：即使调用方复用会话 ID 搜不同的词，也不会被合并。
func sessionKey(sessionID, keyword string) string {
	sum := sha256.Sum256([]byte(keyword))
	return sessionID + "/" + hex.EncodeToString(sum[:6])
}

// recordSearch 记录一次搜索请求（异步，不阻塞调用方）。
func recordSearch(c *gin.Context, req model.SearchRequest, total, status int, errMsg string, started time.Time) {
	if statsRecorder == nil {
		return
	}
	keyword := strings.TrimSpace(req.Keyword)
	statsRecorder.RecordSearch(stats.SearchEvent{
		SessionID:   sessionKey(searchSessionID(c), keyword),
		Username:    c.GetString(ctxUsername),
		Role:        string(currentRole(c)),
		IP:          c.ClientIP(),
		UserAgent:   c.GetHeader("User-Agent"),
		Keyword:     keyword,
		SourceType:  req.SourceType,
		CloudTypes:  strings.Join(req.CloudTypes, ","),
		Refresh:     req.ForceRefresh,
		ResultTotal: total,
		LatencyMs:   time.Since(started).Milliseconds(),
		Status:      status,
		Error:       errMsg,
		At:          started,
	})
}

// 登录记录的失败原因
const (
	loginReasonBadRequest  = "bad_request"
	loginReasonRateLimited = "rate_limited"
	loginReasonInvalid     = "invalid_credentials"
	loginReasonDisabled    = "disabled"
	loginReasonNoStore     = "not_configured"
)

func recordLogin(c *gin.Context, username string, success bool, reason string) {
	if statsRecorder == nil {
		return
	}
	statsRecorder.RecordLogin(stats.LoginEvent{
		Username:  username,
		IP:        c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
		Success:   success,
		Reason:    reason,
		At:        time.Now(),
	})
}

// 插件账号管理页的路由前缀
var pluginPagePrefixes = []string{"/qqpd/", "/gying/", "/panlian/", "/weibo/", "/woniu/"}

// routeGroup 把请求路径归入统计分组。
func routeGroup(path string) string {
	switch {
	case path == "/api/search":
		return "search"
	case path == "/api/search/options":
		return "search-options"
	case strings.HasPrefix(path, "/api/check/"):
		return "check"
	case strings.HasPrefix(path, "/api/auth/"):
		return "auth"
	case strings.HasPrefix(path, "/api/admin/"):
		return "admin"
	case path == "/api/health":
		return "health"
	case strings.HasPrefix(path, "/api/"):
		return "api-other"
	}
	for _, prefix := range pluginPagePrefixes {
		if strings.HasPrefix(path, prefix) {
			return "plugin-page"
		}
	}
	return "other"
}

// StatsMiddleware 按分组累计接口访问量、错误数与耗时。
func StatsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if statsRecorder == nil {
			c.Next()
			return
		}
		started := time.Now()
		c.Next()
		statsRecorder.RecordAPI(routeGroup(c.Request.URL.Path), c.Writer.Status(), time.Since(started), started)
	}
}

func statsDBSize() int64 {
	if statsStore == nil {
		return 0
	}
	return statsStore.SizeBytes()
}
