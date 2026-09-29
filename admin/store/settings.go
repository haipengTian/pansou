package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Settings 是后台可在运行期修改、修改后立即生效的搜索设置。
//
// 所有切片字段都视为只读：Store 对外只返回副本，调用方随意修改也不会影响存储。
type Settings struct {
	// EnabledPlugins 启用的插件名。
	EnabledPlugins []string `json:"enabled_plugins"`
	// DefaultChannels 请求未指定频道时使用的 TG 频道。
	DefaultChannels []string `json:"default_channels"`
	// AllowedChannels 普通用户可自选的频道范围；为空表示与 DefaultChannels 相同。
	AllowedChannels []string `json:"allowed_channels"`
	// AllowedCloudTypes 普通用户可见的网盘类型；为空表示不限制。
	AllowedCloudTypes []string `json:"allowed_cloud_types"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// EffectiveAllowedChannels 返回普通用户实际可选的频道范围。
func (s Settings) EffectiveAllowedChannels() []string {
	if len(s.AllowedChannels) > 0 {
		return cloneStrings(s.AllowedChannels)
	}
	return cloneStrings(s.DefaultChannels)
}

// normalized 返回去空白、去重后的独立副本。网盘类型统一小写，与 util.GetLinkType 的取值一致。
func (s Settings) normalized() Settings {
	return Settings{
		EnabledPlugins:    uniqueTrimmed(s.EnabledPlugins, false),
		DefaultChannels:   uniqueTrimmed(s.DefaultChannels, false),
		AllowedChannels:   uniqueTrimmed(s.AllowedChannels, false),
		AllowedCloudTypes: uniqueTrimmed(s.AllowedCloudTypes, true),
		UpdatedAt:         s.UpdatedAt,
		UpdatedBy:         s.UpdatedBy,
	}
}

// Settings 返回当前设置的独立副本。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.normalized()
}

// UpdateSettings 以 next 整体替换当前设置并落盘，返回规范化后的结果。
// 落盘失败时内存中的设置保持不变。
func (s *Store) UpdateSettings(next Settings, updatedBy string) (Settings, error) {
	candidate := next.normalized()
	candidate.UpdatedAt = time.Now()
	candidate.UpdatedBy = updatedBy

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := writeJSONAtomic(filepath.Join(s.dir, settingsFileName), candidate); err != nil {
		return Settings{}, fmt.Errorf("保存设置失败: %w", err)
	}
	s.settings = candidate
	return candidate.normalized(), nil
}

func uniqueTrimmed(values []string, lower bool) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	return result
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
