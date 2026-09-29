package plugin

import (
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"pansou/model"
)

// fakePlugin 是最小可用的搜索插件。
type fakePlugin struct{ *BaseAsyncPlugin }

func newFake(name string) *fakePlugin { return &fakePlugin{NewBaseAsyncPlugin(name, 1)} }

func (p *fakePlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}

// fakeInitPlugin 记录 Initialize 被调用的次数，可配置为初始化失败。
type fakeInitPlugin struct {
	*fakePlugin
	mu        sync.Mutex
	initCalls int
	initErr   error
}

func (p *fakeInitPlugin) Initialize() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initCalls++
	return p.initErr
}

func (p *fakeInitPlugin) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initCalls
}

func newManagerWith(plugins ...AsyncSearchPlugin) *PluginManager {
	pm := NewPluginManager()
	pm.candidates = func() []AsyncSearchPlugin { return plugins }
	return pm
}

func enabledNames(pm *PluginManager) []string {
	var names []string
	for _, p := range pm.GetPlugins() {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	return names
}

func TestSetEnabledSwitchesPluginsAtRuntime(t *testing.T) {
	alpha := newFake("alpha")
	beta := newFake("beta")
	gamma := newFake("gamma")
	pm := newManagerWith(alpha, beta, gamma)

	result := pm.SetEnabled([]string{"alpha", "gamma"})
	if got := enabledNames(pm); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("启用 = %v", got)
	}
	if !reflect.DeepEqual(result.Enabled, []string{"alpha", "gamma"}) || len(result.Unknown) != 0 {
		t.Fatalf("结果 = %+v", result)
	}

	pm.SetEnabled([]string{"beta"})
	if got := enabledNames(pm); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("切换后启用 = %v", got)
	}
	if !pm.IsEnabled("beta") || pm.IsEnabled("alpha") {
		t.Fatal("IsEnabled 与当前快照不一致")
	}
}

func TestSetEnabledReportsUnknownNames(t *testing.T) {
	pm := newManagerWith(newFake("alpha"))

	result := pm.SetEnabled([]string{"alpha", "nope"})
	if !reflect.DeepEqual(result.Unknown, []string{"nope"}) {
		t.Fatalf("未知插件 = %v", result.Unknown)
	}
}

func TestSetEnabledInitializesOnlyOnce(t *testing.T) {
	lazy := &fakeInitPlugin{fakePlugin: newFake("lazy")}
	pm := newManagerWith(lazy)

	pm.SetEnabled(nil)
	if lazy.calls() != 0 {
		t.Fatal("未启用的插件不应被初始化")
	}

	pm.SetEnabled([]string{"lazy"})
	pm.SetEnabled(nil)
	pm.SetEnabled([]string{"lazy"})
	if lazy.calls() != 1 {
		t.Fatalf("反复启停只应初始化一次，实际 %d 次", lazy.calls())
	}
}

func TestSetEnabledSkipsPluginsThatFailToInitialize(t *testing.T) {
	broken := &fakeInitPlugin{fakePlugin: newFake("broken"), initErr: errors.New("boom")}
	healthy := newFake("healthy")
	pm := newManagerWith(broken, healthy)

	result := pm.SetEnabled([]string{"broken", "healthy"})
	if got := enabledNames(pm); !reflect.DeepEqual(got, []string{"healthy"}) {
		t.Fatalf("初始化失败的插件不应启用: %v", got)
	}
	if _, failed := result.Failed["broken"]; !failed {
		t.Fatalf("应报告初始化失败: %+v", result)
	}

	// 失败后允许重试：下一次启用会再次尝试初始化。
	broken.mu.Lock()
	broken.initErr = nil
	broken.mu.Unlock()
	pm.SetEnabled([]string{"broken", "healthy"})
	if got := enabledNames(pm); !reflect.DeepEqual(got, []string{"broken", "healthy"}) {
		t.Fatalf("修复后重试应能启用: %v", got)
	}
}

func TestGetPluginsIsSafeDuringConcurrentSwitching(t *testing.T) {
	pm := newManagerWith(newFake("alpha"), newFake("beta"))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				pm.SetEnabled([]string{"alpha"})
			} else {
				pm.SetEnabled([]string{"alpha", "beta"})
			}
		}(i)
		go func() {
			defer wg.Done()
			for _, p := range pm.GetPlugins() {
				_ = p.Name()
			}
		}()
	}
	wg.Wait()
}

func TestAllPluginsListsCandidatesSortedByName(t *testing.T) {
	pm := newManagerWith(newFake("beta"), newFake("alpha"))
	var names []string
	for _, p := range pm.AllPlugins() {
		names = append(names, p.Name())
	}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("AllPlugins = %v", names)
	}
}

// 旧的注册路径（启动时按 ENABLED_PLUGINS 注册）必须保持原有语义。
func TestLegacyRegisterWithFilterStillWorks(t *testing.T) {
	pm := newManagerWith(newFake("alpha"), newFake("beta"))
	pm.RegisterPlugin(newFake("alpha"))
	if got := enabledNames(pm); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("RegisterPlugin 后启用 = %v", got)
	}
}
