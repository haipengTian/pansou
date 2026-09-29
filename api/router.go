package api

import (
	"github.com/gin-gonic/gin"
	"pansou/config"
	"pansou/plugin"
	"pansou/service"
	"pansou/util"
)

// SetupRouter 设置路由
func SetupRouter(searchService *service.SearchService) *gin.Engine {
	// 设置搜索服务
	SetSearchService(searchService)

	// 设置为生产模式
	gin.SetMode(gin.ReleaseMode)

	// 创建默认路由
	r := gin.Default()
	configureTrustedProxies(r, trustedProxiesFromEnv())

	// 添加中间件
	r.Use(CORSMiddleware())
	r.Use(LoggerMiddleware())
	r.Use(StatsMiddleware())     // 接口访问量统计
	r.Use(util.GzipMiddleware()) // 添加压缩中间件
	r.Use(AuthMiddleware())      // 添加认证中间件

	// 定义API路由组
	api := r.Group("/api")
	{
		// 认证接口（不需要认证，由中间件公开路径处理）
		auth := api.Group("/auth")
		{
			auth.POST("/login", LoginHandler)
			auth.POST("/verify", VerifyHandler)
			auth.POST("/logout", LogoutHandler)
		}

		// 搜索接口 - 支持POST和GET两种方式
		api.POST("/search", SearchHandler)
		api.GET("/search", SearchHandler) // 添加GET方式支持
		api.POST("/check/links", CheckHandler)

		// 健康检查接口：公开，仅返回存活状态；完整运行状态见 /api/admin/health
		api.GET("/health", PublicHealthHandler)

		// 当前用户可选的频道/插件/网盘类型（启用认证时需登录）
		api.GET("/search/options", SearchOptionsHandler)

		// 管理后台接口（始终要求管理员）
		registerAdminRoutes(api)
	}

	registerPluginWebRoutes(r, searchService)

	return r
}

// registerPluginWebRoutes 为全部带管理页的插件注册路由（qqpd、gying、weibo 等数据源账号管理）。
//
// 这些页面管理的是全站共用的数据源账号，只允许管理员访问。路由在启动时一次性注册，
// 插件在后台被停用时由守卫返回 404，从而支持运行期启停而无需动态增删 gin 路由。
func registerPluginWebRoutes(r *gin.Engine, searchService *service.SearchService) {
	if !config.AppConfig.AsyncPluginEnabled || searchService == nil || searchService.GetPluginManager() == nil {
		return
	}
	pm := searchService.GetPluginManager()
	for _, p := range pm.AllPlugins() {
		webPlugin, ok := p.(plugin.PluginWithWebHandler)
		if !ok {
			continue
		}
		webPlugin.RegisterWebRoutes(r.Group("", RequireAdmin(), requirePluginEnabled(pm, p.Name())))
	}
}

func requirePluginEnabled(pm *plugin.PluginManager, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !pm.IsEnabled(name) {
			c.AbortWithStatusJSON(404, gin.H{"error": "插件未启用: " + name})
			return
		}
		c.Next()
	}
}
