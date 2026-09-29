package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	"pansou/util"
)

// 上下文键：AuthMiddleware 解析出的身份。username 键名沿用旧实现，避免影响已有读取方。
const (
	ctxUsername = "username"
	ctxRole     = "role"
)

var (
	// adminStore 保存账号与后台设置；为 nil 时（如单元测试）只校验令牌签名。
	adminStore *store.Store

	// loginGuard 登录失败限速：15 分钟内失败 5 次锁定 15 分钟。
	loginGuard = newLoginLimiter(5, 15*time.Minute, 15*time.Minute)
)

// SetAdminStore 注入账号与设置存储。
func SetAdminStore(s *store.Store) {
	adminStore = s
}

var (
	errTokenMissing = errors.New("未授权：缺少认证令牌")
	errTokenFormat  = errors.New("未授权：令牌格式错误")
	errTokenInvalid = errors.New("未授权：令牌无效或已过期")
)

// identity 是一次请求中已验证的身份。
type identity struct {
	Username string
	Role     store.Role
}

// resolveIdentity 从 Authorization 头解析并核对身份。
//
// 签名有效还不够：账号必须仍然存在、未被禁用，且令牌版本与账号当前版本一致，
// 这样禁用、改密、改角色、删除都能让旧令牌立即失效。角色一律取存储中的当前值。
func resolveIdentity(c *gin.Context) (*identity, error) {
	header := c.GetHeader("Authorization")
	if header == "" {
		return nil, errTokenMissing
	}
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(header, bearerPrefix) {
		return nil, errTokenFormat
	}

	claims, err := util.ValidateToken(strings.TrimPrefix(header, bearerPrefix), config.AppConfig.AuthJWTSecret)
	if err != nil {
		return nil, errTokenInvalid
	}

	if adminStore == nil {
		return &identity{Username: claims.Username, Role: store.Role(claims.Role)}, nil
	}
	user, ok := adminStore.GetUser(claims.Username)
	if !ok || user.Disabled || user.TokenVersion != claims.Version {
		return nil, errTokenInvalid
	}
	return &identity{Username: user.Username, Role: user.Role}, nil
}

func tokenErrorCode(err error) string {
	switch {
	case errors.Is(err, errTokenMissing):
		return "AUTH_TOKEN_MISSING"
	case errors.Is(err, errTokenFormat):
		return "AUTH_TOKEN_INVALID_FORMAT"
	default:
		return "AUTH_TOKEN_INVALID"
	}
}

// currentRole 返回当前请求的角色；匿名请求返回空字符串。
func currentRole(c *gin.Context) store.Role {
	if role, ok := c.Get(ctxRole); ok {
		if r, ok := role.(store.Role); ok {
			return r
		}
	}
	return ""
}

func isAdminRequest(c *gin.Context) bool {
	return currentRole(c) == store.RoleAdmin
}

// RequireAdmin 要求请求来自管理员，不受 AUTH_ENABLED 影响。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, loggedIn := c.Get(ctxUsername); !loggedIn {
			c.AbortWithStatusJSON(401, gin.H{
				"error": "未授权：请使用管理员账号登录",
				"code":  "AUTH_TOKEN_MISSING",
			})
			return
		}
		if !isAdminRequest(c) {
			c.AbortWithStatusJSON(403, gin.H{
				"error": "禁止访问：需要管理员权限",
				"code":  "AUTH_FORBIDDEN",
			})
			return
		}
		c.Next()
	}
}
