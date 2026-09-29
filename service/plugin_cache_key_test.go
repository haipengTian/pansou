package service

import (
	"reflect"
	"testing"

	"pansou/model"
	"pansou/plugin"
	"pansou/util/cache"
)

type cacheKeyFakePlugin struct{ *plugin.BaseAsyncPlugin }

func (p *cacheKeyFakePlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}

func init() {
	for _, name := range []string{"zz_key_alpha", "zz_key_beta", "zz_key_gamma"} {
		plugin.RegisterGlobalPlugin(&cacheKeyFakePlugin{plugin.NewBaseAsyncPlugin(name, 1)})
	}
}

// 后台在运行期启停插件后，"未指定插件"的请求不能继续命中旧启用集合写入的缓存，
// 否则刚停用插件的结果会一直被返回到缓存过期。缓存键必须随实际参与的插件集合变化。
func TestPluginCacheKeyFollowsEnabledSet(t *testing.T) {
	pm := plugin.NewPluginManager()
	s := &SearchService{pluginManager: pm}

	pm.SetEnabled([]string{"zz_key_alpha", "zz_key_beta"})
	if got := s.effectivePluginNames(nil); !reflect.DeepEqual(got, []string{"zz_key_alpha", "zz_key_beta"}) {
		t.Fatalf("未指定插件时应为全部启用插件，得到 %v", got)
	}
	before := cache.GeneratePluginCacheKey("kw", s.effectivePluginNames(nil), "")

	pm.SetEnabled([]string{"zz_key_alpha"})
	after := cache.GeneratePluginCacheKey("kw", s.effectivePluginNames(nil), "")
	if before == after {
		t.Fatal("启用集合变化后缓存键应随之变化")
	}
}

func TestEffectivePluginNamesIntersectsRequest(t *testing.T) {
	pm := plugin.NewPluginManager()
	pm.SetEnabled([]string{"zz_key_alpha", "zz_key_gamma"})
	s := &SearchService{pluginManager: pm}

	got := s.effectivePluginNames([]string{"ZZ_KEY_GAMMA", "zz_key_beta", ""})
	if !reflect.DeepEqual(got, []string{"zz_key_gamma"}) {
		t.Fatalf("应只保留已启用的请求插件（不区分大小写），得到 %v", got)
	}
}
