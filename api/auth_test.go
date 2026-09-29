package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	utiljson "pansou/util/json"
)

const testJWTSecret = "test-secret-for-api-auth"

// setupAuthTest 准备一个带环境管理员 root 与后台普通用户 alice 的存储，
// 并替换全局配置/存储/登录限速器，测试结束后还原。
func setupAuthTest(t *testing.T, authEnabled bool) *store.Store {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s, err := store.Open(t.TempDir(), store.Settings{}, store.EnvAccounts{
		Admins: map[string]string{"root": "root-password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice", "alice-password", store.RoleUser); err != nil {
		t.Fatal(err)
	}

	savedConfig, savedStore, savedLimiter := config.AppConfig, adminStore, loginGuard
	config.AppConfig = &config.Config{
		AuthEnabled:     authEnabled,
		AuthJWTSecret:   testJWTSecret,
		AuthTokenExpiry: time.Hour,
	}
	adminStore = s
	loginGuard = newLoginLimiter(5, 15*time.Minute, 15*time.Minute)
	t.Cleanup(func() {
		config.AppConfig, adminStore, loginGuard = savedConfig, savedStore, savedLimiter
	})
	return s
}

func newAuthTestRouter() *gin.Engine {
	r := gin.New()
	r.Use(AuthMiddleware())
	r.POST("/api/auth/login", LoginHandler)
	r.POST("/api/auth/verify", VerifyHandler)
	r.GET("/api/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/api/search", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.GET("/api/admin/ping", RequireAdmin(), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	return r
}

func login(t *testing.T, r *gin.Engine, username, password string) (*httptest.ResponseRecorder, LoginResponse) {
	t.Helper()
	body, _ := utiljson.Marshal(LoginRequest{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp LoginResponse
	if w.Code == http.StatusOK {
		if err := utiljson.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析登录响应失败: %v", err)
		}
	}
	return w, resp
}

func get(r *gin.Engine, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLoginReturnsRoleFromStore(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()

	w, resp := login(t, r, "root", "root-password")
	if w.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: %d %s", w.Code, w.Body.String())
	}
	if resp.Role != string(store.RoleAdmin) || resp.Token == "" {
		t.Fatalf("响应应包含 admin 角色与令牌: %+v", resp)
	}

	w, resp = login(t, r, "alice", "alice-password")
	if w.Code != http.StatusOK || resp.Role != string(store.RoleUser) {
		t.Fatalf("普通用户登录应返回 user 角色: %d %+v", w.Code, resp)
	}
}

func TestLoginRejectsBadCredentialsAndDisabledUsers(t *testing.T) {
	s := setupAuthTest(t, true)
	r := newAuthTestRouter()

	if w, _ := login(t, r, "alice", "wrong-password"); w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应返回 401，得到 %d", w.Code)
	}
	if w, _ := login(t, r, "ghost", "whatever-pass"); w.Code != http.StatusUnauthorized {
		t.Fatalf("未知用户应返回 401，得到 %d", w.Code)
	}

	disabled := true
	if _, err := s.UpdateUser("alice", store.UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if w, _ := login(t, r, "alice", "alice-password"); w.Code != http.StatusForbidden {
		t.Fatalf("禁用用户应返回 403，得到 %d", w.Code)
	}
}

func TestLoginIsRateLimitedAfterRepeatedFailures(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()

	for i := 0; i < 5; i++ {
		login(t, r, "alice", "wrong-password")
	}
	if w, _ := login(t, r, "alice", "alice-password"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("连续失败后即使密码正确也应被限速，得到 %d", w.Code)
	}
}

func TestLoginWorksEvenWhenCustomerAuthDisabled(t *testing.T) {
	setupAuthTest(t, false)
	r := newAuthTestRouter()

	// AUTH_ENABLED 只决定客户是否必须登录，管理员仍需要能登录后台。
	if w, resp := login(t, r, "root", "root-password"); w.Code != http.StatusOK || resp.Role != "admin" {
		t.Fatalf("认证关闭时管理员也应能登录: %d %s", w.Code, w.Body.String())
	}
}

func TestMiddlewareRequiresTokenWhenAuthEnabled(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()

	if w := get(r, "/api/search", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("缺少令牌应返回 401，得到 %d", w.Code)
	}
	if w := get(r, "/api/search", "garbage"); w.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌应返回 401，得到 %d", w.Code)
	}
	if w := get(r, "/api/health", ""); w.Code != http.StatusOK {
		t.Fatalf("健康检查应保持公开，得到 %d", w.Code)
	}

	_, resp := login(t, r, "alice", "alice-password")
	if w := get(r, "/api/search", resp.Token); w.Code != http.StatusOK {
		t.Fatalf("有效令牌应放行，得到 %d %s", w.Code, w.Body.String())
	}
}

func TestTokensAreRevokedBySecurityChanges(t *testing.T) {
	s := setupAuthTest(t, true)
	r := newAuthTestRouter()

	_, resp := login(t, r, "alice", "alice-password")
	newPassword := "another-password"
	if _, err := s.UpdateUser("alice", store.UserPatch{Password: &newPassword}); err != nil {
		t.Fatal(err)
	}
	if w := get(r, "/api/search", resp.Token); w.Code != http.StatusUnauthorized {
		t.Fatalf("重置密码后旧令牌应失效，得到 %d", w.Code)
	}

	_, resp = login(t, r, "alice", newPassword)
	if err := s.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if w := get(r, "/api/search", resp.Token); w.Code != http.StatusUnauthorized {
		t.Fatalf("删除用户后令牌应失效，得到 %d", w.Code)
	}
}

func TestRequireAdminRegardlessOfAuthSwitch(t *testing.T) {
	for _, authEnabled := range []bool{true, false} {
		setupAuthTest(t, authEnabled)
		r := newAuthTestRouter()

		if w := get(r, "/api/admin/ping", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("authEnabled=%v: 匿名访问后台应返回 401，得到 %d", authEnabled, w.Code)
		}
		_, user := login(t, r, "alice", "alice-password")
		if w := get(r, "/api/admin/ping", user.Token); w.Code != http.StatusForbidden {
			t.Fatalf("authEnabled=%v: 普通用户访问后台应返回 403，得到 %d", authEnabled, w.Code)
		}
		_, admin := login(t, r, "root", "root-password")
		if w := get(r, "/api/admin/ping", admin.Token); w.Code != http.StatusOK {
			t.Fatalf("authEnabled=%v: 管理员应能访问后台，得到 %d", authEnabled, w.Code)
		}
	}
}

func TestVerifyReportsRole(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()

	_, resp := login(t, r, "root", "root-password")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/verify", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var body map[string]interface{}
	_ = utiljson.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || body["role"] != "admin" || body["username"] != "root" {
		t.Fatalf("verify 应返回用户名与角色: %d %s", w.Code, w.Body.String())
	}
}

func loginFrom(t *testing.T, r *gin.Engine, remoteAddr, forwardedFor, username, password string) int {
	t.Helper()
	body, _ := utiljson.Marshal(LoginRequest{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// 直连的客户端伪造 X-Forwarded-For 不能绕过按 IP 的限速。
func TestLoginLimiterIgnoresSpoofedForwardedFor(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()
	configureTrustedProxies(r, defaultTrustedProxies)

	for i := 0; i < 5; i++ {
		loginFrom(t, r, "203.0.113.5:4000", fmt.Sprintf("198.51.100.%d", i), "alice", "wrong-password")
	}
	if code := loginFrom(t, r, "203.0.113.5:4000", "198.51.100.99", "alice", "alice-password"); code != http.StatusTooManyRequests {
		t.Fatalf("伪造 X-Forwarded-For 不应绕过限速，得到 %d", code)
	}
}

// 经由可信反向代理时，按代理转发的真实客户端 IP 限速；客户端在最左侧伪造的条目不起作用。
func TestLoginLimiterUsesRealClientBehindProxy(t *testing.T) {
	setupAuthTest(t, true)
	r := newAuthTestRouter()
	configureTrustedProxies(r, defaultTrustedProxies)

	for i := 0; i < 5; i++ {
		spoofed := fmt.Sprintf("10.9.9.%d, 198.51.100.7", i)
		loginFrom(t, r, "127.0.0.1:5000", spoofed, "alice", "wrong-password")
	}
	if code := loginFrom(t, r, "127.0.0.1:5000", "198.51.100.7", "alice", "alice-password"); code != http.StatusTooManyRequests {
		t.Fatalf("同一真实客户端应被限速，得到 %d", code)
	}
	// 另一个真实客户端不受影响——限速不能被用来把某个账号锁在门外。
	if code := loginFrom(t, r, "127.0.0.1:5000", "198.51.100.8", "alice", "alice-password"); code != http.StatusOK {
		t.Fatalf("其他客户端不应被牵连，得到 %d", code)
	}
}
