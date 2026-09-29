package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/admin/store"
	"pansou/model"
	"pansou/stats"
	utiljson "pansou/util/json"
)

// setupStats 为当前测试挂上一个真实的临时统计库与记录器。
func setupStats(t *testing.T) (*stats.Store, *stats.Recorder) {
	t.Helper()
	st, err := stats.Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	rec := stats.NewRecorder(st, stats.RecorderOptions{})
	savedStore, savedRec := statsStore, statsRecorder
	SetStats(st, rec)
	t.Cleanup(func() {
		rec.Close()
		_ = st.Close()
		statsStore, statsRecorder = savedStore, savedRec
	})
	return st, rec
}

func TestSearchSessionIDValidation(t *testing.T) {
	cases := []struct {
		header string
		keep   bool
	}{
		{"3f2a9c1e-5b7d-4e8f-9a0b-1c2d3e4f5a6b", true},
		{"abc", true},
		{"", false},
		{"has space", false},
		{"<script>", false},
		{strings.Repeat("a", 65), false},
	}
	for _, tc := range cases {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/search", nil)
		if tc.header != "" {
			c.Request.Header.Set(searchSessionHeader, tc.header)
		}
		got := searchSessionID(c)
		if tc.keep && got != tc.header {
			t.Fatalf("合法会话 ID %q 应原样使用，得到 %q", tc.header, got)
		}
		if !tc.keep && (got == tc.header || got == "") {
			t.Fatalf("非法会话 ID %q 应由服务端重新生成，得到 %q", tc.header, got)
		}
	}
}

func newRecordContext(t *testing.T, session, username string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/search?kw=x", nil)
	c.Request.RemoteAddr = "203.0.113.7:5555"
	c.Request.Header.Set("User-Agent", "pansou-test/1.0")
	if session != "" {
		c.Request.Header.Set(searchSessionHeader, session)
	}
	if username != "" {
		c.Set(ctxUsername, username)
		c.Set(ctxRole, store.RoleUser)
	}
	return c
}

func TestRecordSearchMergesSameSessionAndKeyword(t *testing.T) {
	st, rec := setupStats(t)
	req := model.SearchRequest{Keyword: "流浪地球", SourceType: "all", CloudTypes: []string{"quark", "baidu"}}
	started := time.Now()

	for _, total := range []int{3, 40, 12} {
		recordSearch(newRecordContext(t, "sess-1", "alice"), req, total, http.StatusOK, "", started)
	}
	// 同一会话 ID 搜不同关键词不能合并到一起。
	recordSearch(newRecordContext(t, "sess-1", "alice"), model.SearchRequest{Keyword: "三体"}, 1, http.StatusOK, "", started)
	rec.Flush()

	page, err := st.History(stats.HistoryFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("应有 2 条记录（按会话+关键词合并），得到 %d", page.Total)
	}
	var row stats.SearchRow
	for _, r := range page.Items {
		if r.Keyword == "流浪地球" {
			row = r
		}
	}
	if row.RequestCount != 3 || row.ResultTotal != 40 {
		t.Fatalf("合并结果错误: %+v", row)
	}
	if row.Username != "alice" || row.IP != "203.0.113.7" || row.UserAgent != "pansou-test/1.0" || row.CloudTypes != "quark,baidu" {
		t.Fatalf("记录的用户/完整 IP/UA/网盘类型错误: %+v", row)
	}
}

func TestRecordSearchWithoutSessionCountsEachRequest(t *testing.T) {
	st, rec := setupStats(t)
	req := model.SearchRequest{Keyword: "kw"}
	for i := 0; i < 3; i++ {
		recordSearch(newRecordContext(t, "", ""), req, 1, http.StatusOK, "", time.Now())
	}
	rec.Flush()
	if page, _ := st.History(stats.HistoryFilter{}, 1, 10); page.Total != 3 {
		t.Fatalf("没有会话头时每个请求各记一条，得到 %d", page.Total)
	}
}

func TestRecordSearchIsNoopWithoutStats(t *testing.T) {
	savedStore, savedRec := statsStore, statsRecorder
	SetStats(nil, nil)
	defer func() { statsStore, statsRecorder = savedStore, savedRec }()
	// 统计不可用时不能 panic，也不能影响调用方。
	recordSearch(newRecordContext(t, "s", "alice"), model.SearchRequest{Keyword: "x"}, 1, 200, "", time.Now())
}

func TestLoginAttemptsAreRecorded(t *testing.T) {
	setupAuthTest(t, true)
	st, rec := setupStats(t)
	r := newAuthTestRouter()

	login(t, r, "alice", "wrong-password")
	login(t, r, "alice", "alice-password")
	rec.Flush()

	page, err := st.Logins(stats.LoginFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("应记录 2 次登录尝试，得到 %d", page.Total)
	}
	var success, failure int
	for _, item := range page.Items {
		if item.Success {
			success++
		} else if item.Reason == loginReasonInvalid {
			failure++
		}
	}
	if success != 1 || failure != 1 {
		t.Fatalf("成功/失败记录错误: %+v", page.Items)
	}
}

func TestRouteGroup(t *testing.T) {
	cases := map[string]string{
		"/api/search":           "search",
		"/api/search/options":   "search-options",
		"/api/check/links":      "check",
		"/api/auth/login":       "auth",
		"/api/admin/stats/logs": "admin",
		"/api/health":           "health",
		"/qqpd/abc":             "plugin-page",
		"/woniu/abc":            "plugin-page",
		"/api/unknown":          "api-other",
		"/favicon.ico":          "other",
	}
	for path, want := range cases {
		if got := routeGroup(path); got != want {
			t.Errorf("routeGroup(%q) = %q, 期望 %q", path, got, want)
		}
	}
}

func TestStatsMiddlewareCountsRequests(t *testing.T) {
	st, rec := setupStats(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(StatsMiddleware())
	r.GET("/api/search", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/check/links", func(c *gin.Context) { c.Status(http.StatusBadGateway) })

	for _, path := range []string{"/api/search", "/api/search", "/api/check/links"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	rec.Flush()

	o, err := st.Overview(time.Now(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]stats.APIGroupStats{}
	for _, g := range o.API {
		got[g.Group] = g
	}
	if got["search"].Requests != 2 || got["check"].Errors5xx != 1 {
		t.Fatalf("接口分组计数错误: %+v", o.API)
	}
}

// ---------- 管理接口 ----------

func setupStatsAdmin(t *testing.T) (*gin.Engine, *stats.Store, *stats.Recorder) {
	t.Helper()
	r, _, _ := setupAdminTest(t)
	st, rec := setupStats(t)
	now := time.Now()
	for i, kw := range []string{"流浪地球", "流浪地球", "不存在的片"} {
		total := 5
		if kw == "不存在的片" {
			total = 0
		}
		statsRecorder.RecordSearch(stats.SearchEvent{
			SessionID: string(rune('a' + i)), Username: "alice", IP: "198.51.100.1", UserAgent: "ua, \"quoted\"",
			Keyword: kw, ResultTotal: total, Status: 200, LatencyMs: 50, At: now,
		})
	}
	rec.Flush()
	return r, st, rec
}

func TestAdminStatsRequireAdmin(t *testing.T) {
	r, _, _ := setupStatsAdmin(t)
	_, user := login(t, r, "alice", "alice-password")
	for _, path := range []string{"/api/admin/stats/overview", "/api/admin/stats/searches", "/api/admin/stats/searches/export", "/api/admin/stats/logins"} {
		if w, _ := doJSON(t, r, http.MethodGet, path, user.Token, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s: 普通用户应被拒绝，得到 %d", path, w.Code)
		}
	}
}

func TestAdminStatsOverviewAndSearches(t *testing.T) {
	r, _, _ := setupStatsAdmin(t)
	token := adminToken(t, r)

	w, resp := doJSON(t, r, http.MethodGet, "/api/admin/stats/overview?days=7", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	today := dataMap(t, resp)["today"].(map[string]interface{})
	if num(today["searches"]) != 3 {
		t.Fatalf("今日搜索量错误: %v", today)
	}

	w, resp = doJSON(t, r, http.MethodGet, "/api/admin/stats/searches?zero_only=true", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if num(dataMap(t, resp)["total"]) != 1 {
		t.Fatalf("仅零结果筛选错误: %s", w.Body.String())
	}

	if w, _ := doJSON(t, r, http.MethodGet, "/api/admin/stats/searches?from=2026/09/01", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("非法日期应返回 400，得到 %d", w.Code)
	}
}

func TestAdminStatsExportCSV(t *testing.T) {
	r, _, _ := setupStatsAdmin(t)
	token := adminToken(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/stats/searches/export?keyword=流浪", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("响应头错误: %v", w.Header())
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("\xEF\xBB\xBF")) {
		t.Fatal("CSV 应以 UTF-8 BOM 开头，便于 Excel 识别中文")
	}
	lines := strings.Split(strings.TrimSpace(string(body[3:])), "\n")
	if len(lines) != 3 {
		t.Fatalf("应为 1 行表头 + 2 行数据，得到 %d 行:\n%s", len(lines), body)
	}
	if !strings.Contains(lines[1], `"ua, ""quoted"""`) {
		t.Fatalf("含逗号与引号的字段应正确转义: %s", lines[1])
	}
}

func TestUsersListIncludesActivity(t *testing.T) {
	r, _, _ := setupStatsAdmin(t)
	token := adminToken(t, r)

	_, resp := doJSON(t, r, http.MethodGet, "/api/admin/users", token, nil)
	body, _ := utiljson.Marshal(resp)
	for _, item := range dataMap(t, resp)["users"].([]interface{}) {
		u := item.(map[string]interface{})
		if u["username"] == "alice" {
			if num(u["today_searches"]) != 3 || u["last_search_at"] == nil {
				t.Fatalf("用户活跃度缺失: %s", body)
			}
			return
		}
	}
	t.Fatalf("未找到 alice: %s", body)
}

// num 读取 JSON 数字（项目的 JSON 封装把数字解码为 json.Number，这里按文本解析兼容两种形式）。
func num(v interface{}) float64 {
	f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil {
		return -1
	}
	return f
}
