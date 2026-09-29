package stats

import (
	"path/filepath"
	"testing"
	"time"
)

// 固定"现在"，让按天/按小时的统计可预期。
var testNow = time.Date(2026, 9, 29, 15, 30, 0, 0, time.Local)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stats.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func search(session, user, ip, keyword string, total int, at time.Time) SearchEvent {
	return SearchEvent{
		SessionID:   session,
		Username:    user,
		Role:        "user",
		IP:          ip,
		UserAgent:   "test-agent",
		Keyword:     keyword,
		SourceType:  "all",
		ResultTotal: total,
		LatencyMs:   100,
		Status:      200,
		At:          at,
	}
}

func mustWrite(t *testing.T, s *Store, searches []SearchEvent, logins []LoginEvent) {
	t.Helper()
	if err := s.writeBatch(searches, logins, nil); err != nil {
		t.Fatalf("writeBatch 失败: %v", err)
	}
}

func TestOpenCreatesSchemaAndReopens(t *testing.T) {
	s, path := openTestStore(t)
	mustWrite(t, s, []SearchEvent{search("s1", "alice", "1.1.1.1", "复仇者", 3, testNow)}, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer reopened.Close()
	page, err := reopened.History(HistoryFilter{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Keyword != "复仇者" {
		t.Fatalf("重开后数据丢失: %+v", page)
	}
}

// 前端一次搜索最多发 5 个请求（预热 + 首次 + 3 轮补齐），必须合并成一条记录。
func TestSameSessionMergesIntoOneRow(t *testing.T) {
	s, _ := openTestStore(t)
	events := []SearchEvent{
		search("sess-a", "alice", "1.1.1.1", "流浪地球", 3, testNow),
		search("sess-a", "alice", "1.1.1.1", "流浪地球", 40, testNow.Add(2*time.Second)),
		search("sess-a", "alice", "1.1.1.1", "流浪地球", 12, testNow.Add(4*time.Second)),
	}
	mustWrite(t, s, events, nil)

	page, err := s.History(HistoryFilter{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("同一会话应只有 1 条记录，得到 %d", page.Total)
	}
	row := page.Items[0]
	if row.RequestCount != 3 || row.ResultTotal != 40 {
		t.Fatalf("应累计请求数并取最大结果数: %+v", row)
	}
	if !row.CreatedAt.Equal(testNow) {
		t.Fatalf("创建时间应为首个请求的时间: %v", row.CreatedAt)
	}
}

func TestLaterSuccessReplacesEarlierFailure(t *testing.T) {
	s, _ := openTestStore(t)
	failed := search("sess-b", "alice", "1.1.1.1", "kw", 0, testNow)
	failed.Status, failed.Error = 500, "boom"
	ok := search("sess-b", "alice", "1.1.1.1", "kw", 5, testNow.Add(time.Second))
	mustWrite(t, s, []SearchEvent{failed, ok}, nil)

	row := mustHistory(t, s, HistoryFilter{}).Items[0]
	if row.Status != 200 || row.Error != "" {
		t.Fatalf("后续成功应覆盖先前的失败状态: %+v", row)
	}
}

func mustHistory(t *testing.T, s *Store, f HistoryFilter) HistoryPage {
	t.Helper()
	page, err := s.History(f, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestHistoryFilters(t *testing.T) {
	s, _ := openTestStore(t)
	yesterday := testNow.AddDate(0, 0, -1)
	mustWrite(t, s, []SearchEvent{
		search("1", "alice", "10.0.0.1", "流浪地球2", 5, testNow),
		search("2", "bob", "10.0.0.2", "三体", 0, testNow),
		search("3", "alice", "10.0.0.1", "三体 4K", 7, yesterday),
		search("4", "carol", "2001:db8::1", "100%_纯净", 1, testNow),
	}, nil)

	cases := []struct {
		name   string
		filter HistoryFilter
		want   int
	}{
		{"按用户", HistoryFilter{Username: "alice"}, 2},
		{"关键词模糊匹配", HistoryFilter{Keyword: "三体"}, 2},
		{"关键词中的 % 与 _ 按字面匹配", HistoryFilter{Keyword: "%_"}, 1},
		{"按完整 IP", HistoryFilter{IP: "10.0.0.1"}, 2},
		{"IPv6 完整保存", HistoryFilter{IP: "2001:db8::1"}, 1},
		{"仅零结果", HistoryFilter{ZeroOnly: true}, 1},
		{"日期范围", HistoryFilter{From: dayOf(testNow), To: dayOf(testNow)}, 3},
		{"组合条件", HistoryFilter{Username: "alice", Keyword: "三体"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustHistory(t, s, tc.filter).Total; got != tc.want {
				t.Fatalf("得到 %d 条，期望 %d", got, tc.want)
			}
		})
	}
}

func TestHistoryPaginationNewestFirst(t *testing.T) {
	s, _ := openTestStore(t)
	var events []SearchEvent
	for i := 0; i < 25; i++ {
		events = append(events, search(string(rune('a'+i)), "alice", "1.1.1.1", "kw", 1, testNow.Add(time.Duration(i)*time.Minute)))
	}
	mustWrite(t, s, events, nil)

	page, err := s.History(HistoryFilter{}, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 25 || len(page.Items) != 10 {
		t.Fatalf("total=%d len=%d", page.Total, len(page.Items))
	}
	if !page.Items[0].CreatedAt.Equal(testNow.Add(14 * time.Minute)) {
		t.Fatalf("第 2 页首条应为倒序第 11 条，得到 %v", page.Items[0].CreatedAt)
	}
}

func TestOverview(t *testing.T) {
	s, _ := openTestStore(t)
	yesterday := testNow.AddDate(0, 0, -1)
	mustWrite(t, s, []SearchEvent{
		search("1", "alice", "10.0.0.1", "流浪地球", 5, testNow),
		search("2", "alice", "10.0.0.1", "流浪地球", 8, testNow.Add(-time.Hour)),
		search("3", "bob", "10.0.0.2", "不存在的片", 0, testNow),
		search("4", "bob", "10.0.0.3", "流浪地球", 2, yesterday),
	}, nil)

	o, err := s.Overview(testNow, 7, 5)
	if err != nil {
		t.Fatal(err)
	}
	if o.Today.Searches != 3 || o.Today.Users != 2 || o.Today.IPs != 2 {
		t.Fatalf("今日统计错误: %+v", o.Today)
	}
	if o.Total.Searches != 4 {
		t.Fatalf("累计搜索应为 4，得到 %d", o.Total.Searches)
	}
	if len(o.Daily) != 7 || o.Daily[6].Day != dayOf(testNow) || o.Daily[6].Searches != 3 || o.Daily[5].Searches != 1 {
		t.Fatalf("按天序列应补齐 7 天且最后一天为今天: %+v", o.Daily)
	}
	if len(o.Hourly) != 24 || o.Hourly[15] != 2 || o.Hourly[14] != 1 {
		t.Fatalf("今日按小时分布错误: %v", o.Hourly)
	}
	if len(o.TopKeywords) == 0 || o.TopKeywords[0].Keyword != "流浪地球" || o.TopKeywords[0].Count != 3 {
		t.Fatalf("热门关键词错误: %+v", o.TopKeywords)
	}
	if len(o.ZeroKeywords) != 1 || o.ZeroKeywords[0].Keyword != "不存在的片" {
		t.Fatalf("零结果关键词错误: %+v", o.ZeroKeywords)
	}
	if o.Period.ZeroRate <= 0.24 || o.Period.ZeroRate >= 0.26 {
		t.Fatalf("零结果率应为 1/4，得到 %v", o.Period.ZeroRate)
	}
}

func TestLoginsAndUserActivity(t *testing.T) {
	s, _ := openTestStore(t)
	mustWrite(t, s,
		[]SearchEvent{
			search("1", "alice", "10.0.0.1", "a", 1, testNow),
			search("2", "alice", "10.0.0.1", "b", 1, testNow.AddDate(0, 0, -2)),
		},
		[]LoginEvent{
			{Username: "alice", IP: "10.0.0.1", UserAgent: "ua", Success: true, At: testNow},
			{Username: "mallory", IP: "203.0.113.9", UserAgent: "ua", Success: false, Reason: "invalid_credentials", At: testNow},
		})

	failed := false
	page, err := s.Logins(LoginFilter{Success: &failed}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Username != "mallory" || page.Items[0].Reason != "invalid_credentials" {
		t.Fatalf("失败登录筛选错误: %+v", page)
	}

	activity, err := s.UserActivity(testNow)
	if err != nil {
		t.Fatal(err)
	}
	a := activity["alice"]
	if a.TodaySearches != 1 || !a.LastSearchAt.Equal(testNow) {
		t.Fatalf("用户活跃度错误: %+v", a)
	}
}

func TestAPIHourlyAggregation(t *testing.T) {
	s, _ := openTestStore(t)
	agg := map[hourKey]*hourAgg{
		{Day: dayOf(testNow), Hour: 15, Group: "search"}: {Requests: 10, Errors4xx: 1, Errors5xx: 2, LatencyMsSum: 1000},
	}
	if err := s.writeBatch(nil, nil, agg); err != nil {
		t.Fatal(err)
	}
	// 同一小时再次写入应累加。
	if err := s.writeBatch(nil, nil, agg); err != nil {
		t.Fatal(err)
	}

	o, err := s.Overview(testNow, 7, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.API) != 1 || o.API[0].Requests != 20 || o.API[0].Errors5xx != 4 || o.API[0].AvgLatencyMs != 100 {
		t.Fatalf("接口分组统计错误: %+v", o.API)
	}
}

func TestEachSearchStreamsAllMatching(t *testing.T) {
	s, _ := openTestStore(t)
	mustWrite(t, s, []SearchEvent{
		search("1", "alice", "1.1.1.1", "a", 1, testNow),
		search("2", "bob", "1.1.1.2", "b", 1, testNow),
	}, nil)

	var names []string
	err := s.EachSearch(HistoryFilter{Username: "bob"}, func(r SearchRow) error {
		names = append(names, r.Username)
		return nil
	})
	if err != nil || len(names) != 1 || names[0] != "bob" {
		t.Fatalf("导出遍历错误: %v %v", names, err)
	}
}
