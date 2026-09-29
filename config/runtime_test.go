package config

import (
	"reflect"
	"sync"
	"testing"
)

func withTestConfig(t *testing.T, cfg *Config) {
	t.Helper()
	saved := AppConfig
	savedChannels := runtimeChannels.Load()
	savedConcurrency := runtimeConcurrency.Load()
	AppConfig = cfg
	runtimeChannels.Store(nil)
	runtimeConcurrency.Store(0)
	t.Cleanup(func() {
		AppConfig = saved
		runtimeChannels.Store(savedChannels)
		runtimeConcurrency.Store(savedConcurrency)
	})
}

func TestDefaultChannelsFallsBackToStartupConfig(t *testing.T) {
	withTestConfig(t, &Config{DefaultChannels: []string{"from-env"}})

	if got := DefaultChannels(); !reflect.DeepEqual(got, []string{"from-env"}) {
		t.Fatalf("未设置运行期频道时应返回启动配置，得到 %v", got)
	}
}

func TestSetDefaultChannelsTakesEffectAndIsCopied(t *testing.T) {
	withTestConfig(t, &Config{DefaultChannels: []string{"from-env"}})

	input := []string{"a", "b"}
	SetDefaultChannels(input)
	input[0] = "mutated"

	got := DefaultChannels()
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("得到 %v，期望 [a b]（且不受调用方后续修改影响）", got)
	}
	got[1] = "mutated-too"
	if DefaultChannels()[1] != "b" {
		t.Fatal("读取方修改返回值不应影响运行期配置")
	}
}

func TestDefaultConcurrencyRecomputedFromRuntimeChannels(t *testing.T) {
	t.Setenv("CONCURRENCY", "")
	withTestConfig(t, &Config{DefaultChannels: []string{"a"}, DefaultConcurrency: 99})

	if got := DefaultConcurrency(); got != 99 {
		t.Fatalf("未重算前应返回启动值，得到 %d", got)
	}

	SetDefaultChannels([]string{"a", "b", "c"})
	UpdateDefaultConcurrency(5)
	if got := DefaultConcurrency(); got != 3+5+10 {
		t.Fatalf("并发数应为 频道3 + 插件5 + 10 = 18，得到 %d", got)
	}
}

func TestExplicitConcurrencyIsNeverOverridden(t *testing.T) {
	t.Setenv("CONCURRENCY", "42")
	withTestConfig(t, &Config{DefaultChannels: []string{"a"}, DefaultConcurrency: 42})

	UpdateDefaultConcurrency(100)
	if got := DefaultConcurrency(); got != 42 {
		t.Fatalf("显式设置 CONCURRENCY 时不应重算，得到 %d", got)
	}
}

func TestRuntimeConfigConcurrentAccess(t *testing.T) {
	t.Setenv("CONCURRENCY", "")
	withTestConfig(t, &Config{DefaultChannels: []string{"a"}})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			SetDefaultChannels([]string{"x", "y"})
			UpdateDefaultConcurrency(3)
		}()
		go func() {
			defer wg.Done()
			_ = DefaultChannels()
			_ = DefaultConcurrency()
		}()
	}
	wg.Wait()
}
