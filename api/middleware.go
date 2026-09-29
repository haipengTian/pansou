package api

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/config"
)

// CORSMiddleware 跨域中间件
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Search-Session")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// LoggerMiddleware 日志中间件
func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 开始时间
		startTime := time.Now()

		// 处理请求
		c.Next()

		// 结束时间
		endTime := time.Now()

		// 执行时间
		latencyTime := endTime.Sub(startTime)

		// 请求方式
		reqMethod := c.Request.Method

		// 请求路由
		reqURI := c.Request.RequestURI

		// 对于搜索API，尝试解码关键词以便更好地显示
		displayURI := reqURI
		if strings.Contains(reqURI, "/api/search") && strings.Contains(reqURI, "kw=") {
			if parsedURL, err := url.Parse(reqURI); err == nil {
				if keyword := parsedURL.Query().Get("kw"); keyword != "" {
					if decodedKeyword, err := url.QueryUnescape(keyword); err == nil {
						// 替换原始URI中的编码关键词为解码后的关键词
						displayURI = strings.Replace(reqURI, "kw="+keyword, "kw="+decodedKeyword, 1)
					}
				}
			}
		}

		// 状态码
		statusCode := c.Writer.Status()

		// 请求IP
		clientIP := c.ClientIP()

		// 日志格式
		gin.DefaultWriter.Write([]byte(
			fmt.Sprintf("| %s | %s | %s | %d | %s\n",
				clientIP, reqMethod, displayURI, statusCode, latencyTime.String())))
	}
}

// authPublicPaths 在启用认证时也无需令牌的接口。
var authPublicPaths = []string{
	"/api/auth/login",
	"/api/auth/logout",
	"/api/health", // 仅返回存活状态，供容器健康检查使用
}

// AuthMiddleware JWT认证中间件
//
// 无论是否启用认证，只要请求携带有效令牌就解析出身份写入上下文，供 RequireAdmin
// 与搜索参数约束使用；AUTH_ENABLED 只决定"没有有效令牌的请求能否继续"。
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := resolveIdentity(c)
		if id != nil {
			c.Set(ctxUsername, id.Username)
			c.Set(ctxRole, id.Role)
		}

		if !config.AppConfig.AuthEnabled || isAuthPublicPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{
				"error": err.Error(),
				"code":  tokenErrorCode(err),
			})
			return
		}
		c.Next()
	}
}

func isAuthPublicPath(path string) bool {
	for _, p := range authPublicPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
