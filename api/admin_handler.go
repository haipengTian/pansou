package api

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/service"
)

// registerAdminRoutes 在 api 路由组下注册 /admin/*，全部要求管理员。
func registerAdminRoutes(api *gin.RouterGroup) {
	admin := api.Group("/admin", RequireAdmin())
	admin.GET("/settings", GetSettingsHandler)
	admin.PUT("/settings", UpdateSettingsHandler)
	admin.GET("/plugins", ListPluginsHandler)
	admin.GET("/health", AdminHealthHandler)
	admin.GET("/runtime", RuntimeHandler)
	admin.GET("/users", ListUsersHandler)
	admin.POST("/users", CreateUserHandler)
	admin.PATCH("/users/:name", UpdateUserHandler)
	admin.DELETE("/users/:name", DeleteUserHandler)
}

func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, model.NewErrorResponse(status, message))
}

func requireStore(c *gin.Context) bool {
	if adminStore == nil {
		respondError(c, http.StatusServiceUnavailable, "管理存储未初始化")
		return false
	}
	return true
}

func currentUsername(c *gin.Context) string {
	return c.GetString(ctxUsername)
}

// ---------- 设置 ----------

type settingsPayload struct {
	EnabledPlugins    []string `json:"enabled_plugins"`
	DefaultChannels   []string `json:"default_channels"`
	AllowedChannels   []string `json:"allowed_channels"`
	AllowedCloudTypes []string `json:"allowed_cloud_types"`
}

// GetSettingsHandler 返回当前设置。
func GetSettingsHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"settings":              adminStore.Settings(),
		"plugin_system_enabled": config.AppConfig.AsyncPluginEnabled,
	}))
}

// settingsMu 串行化"保存 + 应用"：两次保存交错时，落盘的设置与进程内生效的设置必须一致。
var settingsMu sync.Mutex

// UpdateSettingsHandler 校验、保存并立即应用设置。
func UpdateSettingsHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	var payload settingsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		respondError(c, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	next := store.Settings{
		EnabledPlugins:    payload.EnabledPlugins,
		DefaultChannels:   payload.DefaultChannels,
		AllowedChannels:   payload.AllowedChannels,
		AllowedCloudTypes: payload.AllowedCloudTypes,
	}
	if err := validateSettings(next, adminPlugins); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()

	saved, err := adminStore.UpdateSettings(next, currentUsername(c))
	if err != nil {
		respondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	result := ApplySettings(adminPlugins, saved)

	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"settings":       saved,
		"failed_plugins": result.Failed,
	}))
}

// ---------- 插件与运行状态 ----------

type pluginInfo struct {
	Name              string `json:"name"`
	Priority          int    `json:"priority"`
	Enabled           bool   `json:"enabled"`
	HasWebPage        bool   `json:"has_web_page"`
	SkipServiceFilter bool   `json:"skip_service_filter"`
}

// ListPluginsHandler 列出全部插件及其启用状态。
func ListPluginsHandler(c *gin.Context) {
	infos := []pluginInfo{}
	if adminPlugins != nil {
		for _, p := range adminPlugins.AllPlugins() {
			_, hasWeb := p.(plugin.PluginWithWebHandler)
			infos = append(infos, pluginInfo{
				Name:              p.Name(),
				Priority:          p.Priority(),
				Enabled:           adminPlugins.IsEnabled(p.Name()),
				HasWebPage:        hasWeb,
				SkipServiceFilter: p.SkipServiceFilter(),
			})
		}
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"plugin_system_enabled": config.AppConfig.AsyncPluginEnabled,
		"plugins":               infos,
	}))
}

// PublicHealthHandler 公开的健康检查：只报告存活与是否需要登录，
// 插件、频道、存活明细等内部信息见 AdminHealthHandler。
func PublicHealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":       "ok",
		"auth_enabled": config.AppConfig.AuthEnabled,
	})
}

// AdminHealthHandler 返回完整运行状态（原公开 /api/health 的内容）。
func AdminHealthHandler(c *gin.Context) {
	pluginsEnabled := config.AppConfig.AsyncPluginEnabled
	pluginNames := []string{}
	if pluginsEnabled && adminPlugins != nil {
		for _, p := range adminPlugins.GetPlugins() {
			pluginNames = append(pluginNames, p.Name())
		}
	}
	channels := config.DefaultChannels()

	response := gin.H{
		"status":          "ok",
		"auth_enabled":    config.AppConfig.AuthEnabled,
		"plugins_enabled": pluginsEnabled,
		"channels":        channels,
		"channels_count":  len(channels),
		// 存活观测：累积每轮产出/报错，一眼看出哪些插件与频道是失效的。
		// 只报事实不做淘汰——窗口内零产出不代表无数据（内容仍会经后台补齐进缓存）。
		"liveness": service.LivenessSnapshot(),
		// TG 可达性：被墙时 TG 阶段会被直接跳过，这里给出结论、原因与探测时间。
		"tg": service.TGReachabilitySnapshot(),
	}
	if pluginsEnabled {
		response["plugin_count"] = len(pluginNames)
		response["plugins"] = pluginNames
	}
	c.JSON(http.StatusOK, response)
}

// RuntimeHandler 返回只读的运行参数（来自环境变量，修改需重启）。不含任何凭据。
func RuntimeHandler(c *gin.Context) {
	cfg := config.AppConfig
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"port":                         cfg.Port,
		"proxy_configured":             cfg.ProxyURL != "" || cfg.HTTPProxyURL != "" || cfg.HTTPSProxyURL != "",
		"cache_enabled":                cfg.CacheEnabled,
		"cache_max_size_mb":            cfg.CacheMaxSizeMB,
		"cache_ttl_minutes":            cfg.CacheTTLMinutes,
		"plugin_system_enabled":        cfg.AsyncPluginEnabled,
		"plugin_timeout_seconds":       cfg.PluginTimeout.Seconds(),
		"async_response_timeout":       cfg.AsyncResponseTimeout,
		"async_max_background_workers": cfg.AsyncMaxBackgroundWorkers,
		"async_max_background_tasks":   cfg.AsyncMaxBackgroundTasks,
		"async_cache_ttl_hours":        cfg.AsyncCacheTTLHours,
		"outbound_max_concurrency":     cfg.OutboundMaxConcurrency,
		"default_concurrency":          config.DefaultConcurrency(),
		"auth_enabled":                 cfg.AuthEnabled,
		"auth_token_expiry_hours":      cfg.AuthTokenExpiry.Hours(),
		"insecure_skip_tls_verify":     cfg.InsecureSkipTLSVerify,
		"tg_backfill_enabled":          cfg.TGBackfillEnabled,
		"plugin_backfill_enabled":      cfg.PluginBackfillEnabled,
		"http_max_conns":               cfg.HTTPMaxConns,
		"settings_seeded_from_env":     adminStore != nil && adminStore.SeededFromEnv(),
	}))
}

// ---------- 用户 ----------

type userView struct {
	Username    string     `json:"username"`
	Role        store.Role `json:"role"`
	Disabled    bool       `json:"disabled"`
	Source      string     `json:"source"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

func toUserView(u store.User) userView {
	view := userView{Username: u.Username, Role: u.Role, Disabled: u.Disabled, Source: string(u.Source)}
	if !u.CreatedAt.IsZero() {
		created := u.CreatedAt
		view.CreatedAt = &created
	}
	if !u.LastLoginAt.IsZero() {
		last := u.LastLoginAt
		view.LastLoginAt = &last
	}
	return view
}

// ListUsersHandler 列出全部账号（不含密码哈希）。
func ListUsersHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	users := adminStore.Users()
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, toUserView(u))
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"users": views}))
}

type createUserPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// CreateUserHandler 新建账号。
func CreateUserHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	var payload createUserPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		respondError(c, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	role := store.Role(payload.Role)
	if role == "" {
		role = store.RoleUser
	}
	u, err := adminStore.CreateUser(payload.Username, payload.Password, role)
	if err != nil {
		respondStoreError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(gin.H{"user": toUserView(u)}))
}

type updateUserPayload struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

// UpdateUserHandler 修改角色、禁用状态或重置密码。
func UpdateUserHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	name := c.Param("name")
	var payload updateUserPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		respondError(c, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}

	patch := store.UserPatch{Disabled: payload.Disabled, Password: payload.Password}
	if payload.Role != nil {
		role := store.Role(*payload.Role)
		patch.Role = &role
	}

	// 防止管理员把自己锁在门外：不允许给自己降级或禁用自己。
	if name == currentUsername(c) {
		demoting := patch.Role != nil && *patch.Role != store.RoleAdmin
		disabling := patch.Disabled != nil && *patch.Disabled
		if demoting || disabling {
			respondError(c, http.StatusBadRequest, "不能对当前登录的账号降级或禁用")
			return
		}
	}

	u, err := adminStore.UpdateUser(name, patch)
	if err != nil {
		respondStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"user": toUserView(u)}))
}

// DeleteUserHandler 删除账号。
func DeleteUserHandler(c *gin.Context) {
	if !requireStore(c) {
		return
	}
	name := c.Param("name")
	if name == currentUsername(c) {
		respondError(c, http.StatusBadRequest, "不能删除当前登录的账号")
		return
	}
	if err := adminStore.DeleteUser(name); err != nil {
		respondStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"deleted": name}))
}

func respondStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidUsername),
		errors.Is(err, store.ErrInvalidPassword),
		errors.Is(err, store.ErrInvalidRole):
		respondError(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrUserNotFound):
		respondError(c, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrUserExists),
		errors.Is(err, store.ErrEnvUserReadOnly),
		errors.Is(err, store.ErrLastAdmin):
		respondError(c, http.StatusConflict, err.Error())
	default:
		respondError(c, http.StatusInternalServerError, err.Error())
	}
}
