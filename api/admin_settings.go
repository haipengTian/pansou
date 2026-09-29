package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/util"
)

// adminPlugins 是运行中的插件管理器，由 SetSearchService 注入；测试可直接替换。
var adminPlugins *plugin.PluginManager

// TG 频道用户名只含字母、数字、下划线。放宽到 2-64 位以兼容历史配置。
var channelNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{2,64}$`)

// ApplySettings 把后台设置应用到运行中的服务：默认频道、启用插件与默认并发数。
// 启动时与每次后台保存后调用，无需重启。
func ApplySettings(pm *plugin.PluginManager, settings store.Settings) plugin.EnableResult {
	config.SetDefaultChannels(settings.DefaultChannels)

	result := plugin.EnableResult{Failed: map[string]string{}}
	pluginCount := 0
	if config.AppConfig.AsyncPluginEnabled && pm != nil {
		result = pm.SetEnabled(settings.EnabledPlugins)
		pluginCount = len(pm.GetPlugins())
	}
	config.UpdateDefaultConcurrency(pluginCount)
	return result
}

// validateSettings 在落盘前校验设置，返回面向管理员的错误说明。
func validateSettings(settings store.Settings, pm *plugin.PluginManager) error {
	if pm != nil {
		known := map[string]bool{}
		for _, p := range pm.AllPlugins() {
			known[p.Name()] = true
		}
		if unknown := missingFrom(settings.EnabledPlugins, known); len(unknown) > 0 {
			return fmt.Errorf("未知插件: %s", strings.Join(unknown, ", "))
		}
	}

	for field, channels := range map[string][]string{
		"默认频道":   settings.DefaultChannels,
		"客户可选频道": settings.AllowedChannels,
	} {
		if channelsOverLimit(len(channels)) {
			return fmt.Errorf("%s数量 %d 超过上限 %d", field, len(channels), maxRequestChannels)
		}
		for _, ch := range channels {
			if !channelNamePattern.MatchString(strings.TrimSpace(ch)) {
				return fmt.Errorf("%s中的 %q 不是合法的频道名", field, ch)
			}
		}
	}

	knownTypes := map[string]bool{}
	for _, lt := range util.LinkTypes {
		knownTypes[lt] = true
	}
	lowered := make([]string, 0, len(settings.AllowedCloudTypes))
	for _, ct := range settings.AllowedCloudTypes {
		lowered = append(lowered, strings.ToLower(strings.TrimSpace(ct)))
	}
	if unknown := missingFrom(lowered, knownTypes); len(unknown) > 0 {
		return fmt.Errorf("未知网盘类型: %s", strings.Join(unknown, ", "))
	}
	return nil
}

func missingFrom(values []string, known map[string]bool) []string {
	var missing []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !known[v] {
			missing = append(missing, v)
		}
	}
	return missing
}

// restrictSearchRequest 把非管理员的搜索参数收敛到后台允许的范围内，返回新的请求。
//
// 频道：只保留可选范围内的频道（不区分大小写，使用后台配置的写法）；
// 一个都不剩时置空，交给后续逻辑使用默认频道。
// 网盘类型：后台设置了范围时只保留范围内的类型；为空或全部越界时使用整个范围。
// 插件无需处理：搜索服务只会调用当前启用的插件。
func restrictSearchRequest(req model.SearchRequest, settings store.Settings) model.SearchRequest {
	restricted := req
	restricted.Channels = intersectFold(req.Channels, settings.EffectiveAllowedChannels())

	if len(settings.AllowedCloudTypes) > 0 {
		types := intersectFold(req.CloudTypes, settings.AllowedCloudTypes)
		if len(types) == 0 {
			types = append([]string(nil), settings.AllowedCloudTypes...)
		}
		restricted.CloudTypes = types
	} else {
		restricted.CloudTypes = append([]string(nil), req.CloudTypes...)
	}
	return restricted
}

// intersectFold 返回 requested 中出现在 allowed 里的项（按 allowed 的写法），不区分大小写。
func intersectFold(requested, allowed []string) []string {
	if len(requested) == 0 {
		return nil
	}
	canonical := make(map[string]string, len(allowed))
	for _, a := range allowed {
		canonical[strings.ToLower(a)] = a
	}
	var result []string
	seen := map[string]bool{}
	for _, r := range requested {
		key := strings.ToLower(strings.TrimSpace(r))
		if value, ok := canonical[key]; ok && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

// SearchOptionsHandler 返回当前用户可选的频道、插件与网盘类型。
func SearchOptionsHandler(c *gin.Context) {
	settings := store.Settings{DefaultChannels: config.DefaultChannels()}
	if adminStore != nil {
		settings = adminStore.Settings()
	}

	plugins := []string{}
	if config.AppConfig.AsyncPluginEnabled && adminPlugins != nil {
		for _, p := range adminPlugins.GetPlugins() {
			plugins = append(plugins, p.Name())
		}
	}

	cloudTypes := settings.AllowedCloudTypes
	if len(cloudTypes) == 0 {
		cloudTypes = append([]string(nil), util.LinkTypes...)
	}

	c.JSON(200, model.NewSuccessResponse(gin.H{
		"channels":         settings.EffectiveAllowedChannels(),
		"default_channels": config.DefaultChannels(),
		"plugins":          plugins,
		"cloud_types":      cloudTypes,
	}))
}
