package config

import "sync/atomic"

// 可在运行期由管理后台修改的配置项。
//
// AppConfig 的字段在启动后按约定只读；需要热更新的值单独放在原子变量里，
// 读取方通过下面的访问器获取，未设置运行期值时回落到启动配置。
var (
	runtimeChannels    atomic.Pointer[[]string]
	runtimeConcurrency atomic.Int64
)

// DefaultChannels 返回当前生效的默认 TG 频道（独立副本，调用方可随意修改）。
func DefaultChannels() []string {
	if channels := runtimeChannels.Load(); channels != nil {
		return append([]string(nil), (*channels)...)
	}
	if AppConfig == nil {
		return nil
	}
	return append([]string(nil), AppConfig.DefaultChannels...)
}

// SetDefaultChannels 在运行期替换默认频道。
func SetDefaultChannels(channels []string) {
	snapshot := append([]string(nil), channels...)
	runtimeChannels.Store(&snapshot)
}

// DefaultConcurrency 返回当前生效的默认并发数。
func DefaultConcurrency() int {
	if v := runtimeConcurrency.Load(); v > 0 {
		return int(v)
	}
	if AppConfig == nil {
		return 0
	}
	return AppConfig.DefaultConcurrency
}
