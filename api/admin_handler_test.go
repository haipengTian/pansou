package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	utiljson "pansou/util/json"
)

type fakeAdminPlugin struct{ *plugin.BaseAsyncPlugin }

func (p *fakeAdminPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}

// 全局注册表是进程级的，这里只注册一次带测试前缀的假插件。
var fakePluginNames = []string{"zz_admin_alpha", "zz_admin_beta"}

func init() {
	for _, name := range fakePluginNames {
		plugin.RegisterGlobalPlugin(&fakeAdminPlugin{plugin.NewBaseAsyncPlugin(name, 1)})
	}
}

// setupAdminTest 在 setupAuthTest 的基础上接入插件管理器并启用插件系统。
func setupAdminTest(t *testing.T) (*gin.Engine, *store.Store, *plugin.PluginManager) {
	t.Helper()
	s := setupAuthTest(t, true)
	config.AppConfig.AsyncPluginEnabled = true

	savedPM := adminPlugins
	pm := plugin.NewPluginManager()
	adminPlugins = pm
	t.Cleanup(func() {
		adminPlugins = savedPM
		config.SetDefaultChannels(nil)
	})

	r := gin.New()
	r.Use(AuthMiddleware())
	r.POST("/api/auth/login", LoginHandler)
	r.GET("/api/search/options", SearchOptionsHandler)
	registerAdminRoutes(r.Group("/api"))
	return r, s, pm
}

func doJSON(t *testing.T, r *gin.Engine, method, path, token string, body interface{}) (*httptest.ResponseRecorder, model.Response) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := utiljson.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp model.Response
	_ = utiljson.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func adminToken(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w, resp := login(t, r, "root", "root-password")
	if w.Code != http.StatusOK {
		t.Fatalf("管理员登录失败: %s", w.Body.String())
	}
	return resp.Token
}

func dataMap(t *testing.T, resp model.Response) map[string]interface{} {
	t.Helper()
	m, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("响应 data 不是对象: %#v", resp.Data)
	}
	return m
}

func TestAdminRoutesRejectNonAdmins(t *testing.T) {
	r, _, _ := setupAdminTest(t)
	_, user := login(t, r, "alice", "alice-password")

	for _, path := range []string{"/api/admin/settings", "/api/admin/plugins", "/api/admin/users", "/api/admin/health", "/api/admin/runtime"} {
		if w, _ := doJSON(t, r, http.MethodGet, path, user.Token, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s: 普通用户应被拒绝，得到 %d", path, w.Code)
		}
	}
}

func TestUpdateSettingsAppliesImmediately(t *testing.T) {
	r, s, pm := setupAdminTest(t)
	token := adminToken(t, r)

	w, resp := doJSON(t, r, http.MethodPut, "/api/admin/settings", token, map[string]interface{}{
		"enabled_plugins":     []string{"zz_admin_alpha"},
		"default_channels":    []string{"chan_one", "chan_two"},
		"allowed_channels":    []string{},
		"allowed_cloud_types": []string{"quark"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("保存设置失败: %d %s", w.Code, w.Body.String())
	}
	if resp.Code != 0 {
		t.Fatalf("响应码应为 0: %+v", resp)
	}

	if !pm.IsEnabled("zz_admin_alpha") || pm.IsEnabled("zz_admin_beta") {
		t.Fatal("插件启用状态应立即生效")
	}
	if got := config.DefaultChannels(); !reflect.DeepEqual(got, []string{"chan_one", "chan_two"}) {
		t.Fatalf("默认频道应立即生效，得到 %v", got)
	}
	if got := s.Settings(); got.UpdatedBy != "root" {
		t.Fatalf("应持久化并记录修改人: %+v", got)
	}
}

func TestUpdateSettingsValidation(t *testing.T) {
	r, s, _ := setupAdminTest(t)
	token := adminToken(t, r)
	before := s.Settings()

	cases := map[string]map[string]interface{}{
		"未知插件":   {"enabled_plugins": []string{"does_not_exist"}},
		"非法频道名":  {"default_channels": []string{"bad channel!"}},
		"未知网盘类型": {"allowed_cloud_types": []string{"dropbox"}},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if w, _ := doJSON(t, r, http.MethodPut, "/api/admin/settings", token, body); w.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，得到 %d %s", w.Code, w.Body.String())
			}
		})
	}
	if after := s.Settings(); !reflect.DeepEqual(after, before) {
		t.Fatal("校验失败时不应修改设置")
	}
}

func TestAdminPluginsListsEverythingWithState(t *testing.T) {
	r, _, pm := setupAdminTest(t)
	pm.SetEnabled([]string{"zz_admin_beta"})
	token := adminToken(t, r)

	w, resp := doJSON(t, r, http.MethodGet, "/api/admin/plugins", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	items, ok := dataMap(t, resp)["plugins"].([]interface{})
	if !ok {
		t.Fatalf("缺少 plugins 列表: %s", w.Body.String())
	}
	state := map[string]bool{}
	for _, item := range items {
		m := item.(map[string]interface{})
		state[m["name"].(string)] = m["enabled"].(bool)
	}
	if enabled, listed := state["zz_admin_alpha"]; !listed || enabled {
		t.Fatalf("未启用插件也应列出且标记为未启用: %v", state)
	}
	if !state["zz_admin_beta"] {
		t.Fatalf("已启用插件应标记为启用: %v", state)
	}
}

func TestAdminUserLifecycle(t *testing.T) {
	r, _, _ := setupAdminTest(t)
	token := adminToken(t, r)

	w, _ := doJSON(t, r, http.MethodPost, "/api/admin/users", token, map[string]string{
		"username": "carol", "password": "carol-password", "role": "user",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建用户失败: %d %s", w.Code, w.Body.String())
	}
	if w, _ := doJSON(t, r, http.MethodPost, "/api/admin/users", token, map[string]string{
		"username": "carol", "password": "carol-password", "role": "user",
	}); w.Code != http.StatusConflict {
		t.Fatalf("重复创建应返回 409，得到 %d", w.Code)
	}

	_, list := doJSON(t, r, http.MethodGet, "/api/admin/users", token, nil)
	body, _ := utiljson.Marshal(list)
	if bytes.Contains(body, []byte("password_hash")) {
		t.Fatal("用户列表不能返回密码哈希")
	}
	var names []string
	for _, item := range dataMap(t, list)["users"].([]interface{}) {
		names = append(names, item.(map[string]interface{})["username"].(string))
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"alice", "carol", "root"}) {
		t.Fatalf("用户列表 = %v", names)
	}

	if w, _ := doJSON(t, r, http.MethodPatch, "/api/admin/users/carol", token, map[string]interface{}{"disabled": true}); w.Code != http.StatusOK {
		t.Fatalf("禁用失败: %d %s", w.Code, w.Body.String())
	}
	if w, _ := login(t, r, "carol", "carol-password"); w.Code != http.StatusForbidden {
		t.Fatalf("禁用后登录应返回 403，得到 %d", w.Code)
	}
	if w, _ := doJSON(t, r, http.MethodDelete, "/api/admin/users/carol", token, nil); w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	if w, _ := doJSON(t, r, http.MethodDelete, "/api/admin/users/carol", token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("删除不存在的用户应返回 404，得到 %d", w.Code)
	}
}

func TestAdminCannotModifyEnvUsersOrSelf(t *testing.T) {
	r, s, _ := setupAdminTest(t)
	if _, err := s.CreateUser("boss", "boss-password", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	_, bossLogin := login(t, r, "boss", "boss-password")
	rootToken := adminToken(t, r)

	if w, _ := doJSON(t, r, http.MethodDelete, "/api/admin/users/root", bossLogin.Token, nil); w.Code != http.StatusConflict {
		t.Fatalf("删除环境账号应返回 409，得到 %d", w.Code)
	}
	if w, _ := doJSON(t, r, http.MethodDelete, "/api/admin/users/boss", bossLogin.Token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("删除自己应返回 400，得到 %d", w.Code)
	}
	if w, _ := doJSON(t, r, http.MethodPatch, "/api/admin/users/boss", bossLogin.Token, map[string]interface{}{"role": "user"}); w.Code != http.StatusBadRequest {
		t.Fatalf("给自己降级应返回 400，得到 %d", w.Code)
	}
	if w, _ := doJSON(t, r, http.MethodPatch, "/api/admin/users/boss", rootToken, map[string]interface{}{"role": "owner"}); w.Code != http.StatusBadRequest {
		t.Fatalf("非法角色应返回 400，得到 %d", w.Code)
	}
}

func TestSearchOptionsReflectSettings(t *testing.T) {
	r, s, pm := setupAdminTest(t)
	if _, err := s.UpdateSettings(store.Settings{
		DefaultChannels:   []string{"chan_one"},
		AllowedChannels:   []string{"chan_one", "chan_two"},
		AllowedCloudTypes: []string{"quark"},
	}, "root"); err != nil {
		t.Fatal(err)
	}
	pm.SetEnabled([]string{"zz_admin_alpha"})
	_, user := login(t, r, "alice", "alice-password")

	w, resp := doJSON(t, r, http.MethodGet, "/api/search/options", user.Token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	data := dataMap(t, resp)
	if got := toStrings(data["channels"]); !reflect.DeepEqual(got, []string{"chan_one", "chan_two"}) {
		t.Fatalf("channels = %v", got)
	}
	if got := toStrings(data["plugins"]); !reflect.DeepEqual(got, []string{"zz_admin_alpha"}) {
		t.Fatalf("plugins = %v", got)
	}
	if got := toStrings(data["cloud_types"]); !reflect.DeepEqual(got, []string{"quark"}) {
		t.Fatalf("cloud_types = %v", got)
	}
}

func TestPublicHealthHidesInternals(t *testing.T) {
	setupAuthTest(t, true)
	r := gin.New()
	r.Use(AuthMiddleware())
	r.GET("/api/health", PublicHealthHandler)

	w := get(r, "/api/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var body map[string]interface{}
	_ = utiljson.Unmarshal(w.Body.Bytes(), &body)
	for _, key := range []string{"plugins", "channels", "liveness", "tg"} {
		if _, leaked := body[key]; leaked {
			t.Fatalf("公开健康检查不应返回 %s: %s", key, w.Body.String())
		}
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}
}

func toStrings(v interface{}) []string {
	items, _ := v.([]interface{})
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}
