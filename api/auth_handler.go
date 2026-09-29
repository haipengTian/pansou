package api

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	"pansou/util"
)

// LoginRequest 登录请求结构
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse 登录响应结构
type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Username  string `json:"username"`
	Role      string `json:"role"`
}

// LoginHandler 处理用户登录
//
// 不再受 AUTH_ENABLED 限制：客户可以免登录，但管理员必须能登录后台。
func LoginHandler(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		recordLogin(c, req.Username, false, loginReasonBadRequest)
		c.JSON(400, gin.H{"error": "参数错误：用户名和密码不能为空"})
		return
	}

	if adminStore == nil {
		recordLogin(c, req.Username, false, loginReasonNoStore)
		c.JSON(500, gin.H{"error": "认证系统未正确配置"})
		return
	}

	// 只按客户端 IP 计数：按用户名锁定会让任何人都能把管理员锁在门外。
	// ClientIP 仅采信可信代理转发的地址，见 configureTrustedProxies。
	limiterKeys := []string{"ip:" + c.ClientIP()}
	if wait := loginGuard.retryAfter(limiterKeys...); wait > 0 {
		seconds := int(math.Ceil(wait.Seconds()))
		recordLogin(c, req.Username, false, loginReasonRateLimited)
		c.Header("Retry-After", fmt.Sprint(seconds))
		c.JSON(429, gin.H{"error": fmt.Sprintf("登录失败次数过多，请 %d 秒后再试", seconds)})
		return
	}

	user, err := adminStore.Authenticate(req.Username, req.Password)
	switch {
	case errors.Is(err, store.ErrUserDisabled):
		recordLogin(c, req.Username, false, loginReasonDisabled)
		c.JSON(403, gin.H{"error": "账号已被禁用"})
		return
	case err != nil:
		loginGuard.recordFailure(limiterKeys...)
		recordLogin(c, req.Username, false, loginReasonInvalid)
		c.JSON(401, gin.H{"error": "用户名或密码错误"})
		return
	}
	loginGuard.recordSuccess(limiterKeys...)

	token, err := util.GenerateTokenFor(
		user.Username,
		string(user.Role),
		user.TokenVersion,
		config.AppConfig.AuthJWTSecret,
		config.AppConfig.AuthTokenExpiry,
	)
	if err != nil {
		c.JSON(500, gin.H{"error": "生成令牌失败"})
		return
	}

	now := time.Now()
	adminStore.RecordLogin(user.Username, now)
	recordLogin(c, user.Username, true, "")
	c.JSON(200, LoginResponse{
		Token:     token,
		ExpiresAt: now.Add(config.AppConfig.AuthTokenExpiry).Unix(),
		Username:  user.Username,
		Role:      string(user.Role),
	})
}

// VerifyHandler 验证token有效性并返回当前身份
func VerifyHandler(c *gin.Context) {
	if username, ok := c.Get(ctxUsername); ok {
		c.JSON(200, gin.H{
			"valid":    true,
			"username": username,
			"role":     currentRole(c),
		})
		return
	}

	if !config.AppConfig.AuthEnabled {
		c.JSON(200, gin.H{
			"valid":   true,
			"message": "认证功能未启用",
		})
		return
	}

	c.JSON(401, gin.H{"error": "未授权"})
}

// LogoutHandler 退出登录（客户端删除token即可）
func LogoutHandler(c *gin.Context) {
	// JWT是无状态的，服务端不需要处理注销
	// 客户端删除存储的token即可
	c.JSON(200, gin.H{"message": "退出成功"})
}
