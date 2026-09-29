package stats

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// maxPageSize 限制单页条数，避免一次把大表读进内存。
const maxPageSize = 200

func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size
}

// escapeLike 让关键词中的 % 与 _ 按字面匹配。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func (f HistoryFilter) where() (string, []interface{}) {
	var conds []string
	var args []interface{}
	if f.Username != "" {
		conds = append(conds, "username = ?")
		args = append(args, f.Username)
	}
	if f.Keyword != "" {
		conds = append(conds, `keyword LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(f.Keyword))
	}
	if f.IP != "" {
		conds = append(conds, "ip = ?")
		args = append(args, f.IP)
	}
	if f.From != "" {
		conds = append(conds, "day >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "day <= ?")
		args = append(args, f.To)
	}
	if f.ZeroOnly {
		conds = append(conds, "result_total = 0 AND status < 400")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const searchColumns = `id, session_id, username, role, ip, user_agent, keyword, source_type, cloud_types,
	refresh, result_total, first_latency_ms, request_count, status, error, created_at, updated_at`

func scanSearch(rows *sql.Rows) (SearchRow, error) {
	var r SearchRow
	var refresh int
	var created, updated int64
	err := rows.Scan(&r.ID, &r.SessionID, &r.Username, &r.Role, &r.IP, &r.UserAgent, &r.Keyword, &r.SourceType,
		&r.CloudTypes, &refresh, &r.ResultTotal, &r.LatencyMs, &r.RequestCount, &r.Status, &r.Error, &created, &updated)
	r.Refresh = refresh == 1
	r.CreatedAt = time.UnixMilli(created)
	r.UpdatedAt = time.UnixMilli(updated)
	return r, err
}

// History 按筛选条件分页返回搜索历史（新的在前）。
func (s *Store) History(f HistoryFilter, page, size int) (HistoryPage, error) {
	page, size = normalizePage(page, size)
	where, args := f.where()

	result := HistoryPage{Items: []SearchRow{}}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM search_events`+where, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("统计搜索历史失败: %w", err)
	}

	rows, err := s.db.Query(`SELECT `+searchColumns+` FROM search_events`+where+
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, size, (page-1)*size)...)
	if err != nil {
		return result, fmt.Errorf("查询搜索历史失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanSearch(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, r)
	}
	return result, rows.Err()
}

// EachSearch 按筛选条件逐条遍历搜索历史（新的在前），用于导出。fn 返回错误时停止。
func (s *Store) EachSearch(f HistoryFilter, fn func(SearchRow) error) error {
	where, args := f.where()
	rows, err := s.db.Query(`SELECT `+searchColumns+` FROM search_events`+where+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return fmt.Errorf("查询搜索历史失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanSearch(rows)
		if err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Logins 按筛选条件分页返回登录记录（新的在前）。
func (s *Store) Logins(f LoginFilter, page, size int) (LoginPage, error) {
	page, size = normalizePage(page, size)
	var conds []string
	var args []interface{}
	if f.Username != "" {
		conds = append(conds, "username = ?")
		args = append(args, f.Username)
	}
	if f.IP != "" {
		conds = append(conds, "ip = ?")
		args = append(args, f.IP)
	}
	if f.Success != nil {
		conds = append(conds, "success = ?")
		args = append(args, boolInt(*f.Success))
	}
	if f.From != "" {
		conds = append(conds, "day >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "day <= ?")
		args = append(args, f.To)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	result := LoginPage{Items: []LoginRow{}}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM login_events`+where, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("统计登录记录失败: %w", err)
	}
	rows, err := s.db.Query(`SELECT id, username, ip, user_agent, success, reason, created_at FROM login_events`+where+
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, size, (page-1)*size)...)
	if err != nil {
		return result, fmt.Errorf("查询登录记录失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r LoginRow
		var success int
		var created int64
		if err := rows.Scan(&r.ID, &r.Username, &r.IP, &r.UserAgent, &success, &r.Reason, &created); err != nil {
			return result, err
		}
		r.Success = success == 1
		r.CreatedAt = time.UnixMilli(created)
		result.Items = append(result.Items, r)
	}
	return result, rows.Err()
}

// Overview 汇总访问统计：今日与累计计数、最近 days 天的质量指标与序列、今日按小时分布，以及各项 Top N。
func (s *Store) Overview(now time.Time, days, topN int) (Overview, error) {
	if days < 1 {
		days = 1
	}
	today := dayOf(now)
	start := dayOf(now.AddDate(0, 0, -(days - 1)))
	o := Overview{Days: days}

	counts := func(where string, args ...interface{}) (Counts, error) {
		var c Counts
		err := s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT NULLIF(username, '')), COUNT(DISTINCT NULLIF(ip, ''))
			FROM search_events`+where, args...).Scan(&c.Searches, &c.Users, &c.IPs)
		return c, err
	}
	var err error
	if o.Today, err = counts(` WHERE day = ?`, today); err != nil {
		return o, err
	}
	if o.Total, err = counts(``); err != nil {
		return o, err
	}
	if o.Period, err = s.periodStats(start); err != nil {
		return o, err
	}
	if o.Daily, err = s.daily(now, days, start); err != nil {
		return o, err
	}
	if o.Hourly, err = s.hourly(today); err != nil {
		return o, err
	}
	if o.TopKeywords, err = s.keywordRank(start, false, topN); err != nil {
		return o, err
	}
	if o.ZeroKeywords, err = s.keywordRank(start, true, topN); err != nil {
		return o, err
	}
	if o.TopUsers, err = s.userRank(start, topN); err != nil {
		return o, err
	}
	if o.TopIPs, err = s.ipRank(start, topN); err != nil {
		return o, err
	}
	if o.API, err = s.apiGroups(start); err != nil {
		return o, err
	}
	return o, nil
}

func (s *Store) periodStats(start string) (PeriodStats, error) {
	var p PeriodStats
	var avg sql.NullFloat64
	err := s.db.QueryRow(`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN result_total = 0 THEN 1 ELSE 0 END), 0),
			AVG(first_latency_ms)
		FROM search_events WHERE day >= ? AND status < 400`, start).Scan(&p.Searches, &p.ZeroResults, &avg)
	if err != nil {
		return p, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM search_events WHERE day >= ? AND status >= 400`, start).Scan(&p.Errors); err != nil {
		return p, err
	}
	p.AvgLatencyMs = math.Round(avg.Float64*10) / 10
	if p.Searches > 0 {
		p.ZeroRate = float64(p.ZeroResults) / float64(p.Searches)
		offset := int(math.Ceil(0.95*float64(p.Searches))) - 1
		if err := s.db.QueryRow(`SELECT first_latency_ms FROM search_events WHERE day >= ? AND status < 400
			ORDER BY first_latency_ms LIMIT 1 OFFSET ?`, start, offset).Scan(&p.P95LatencyMs); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (s *Store) daily(now time.Time, days int, start string) ([]DailyPoint, error) {
	rows, err := s.db.Query(`SELECT day, COUNT(*), COUNT(DISTINCT NULLIF(username, '')), COUNT(DISTINCT NULLIF(ip, '')),
			COALESCE(SUM(CASE WHEN result_total = 0 AND status < 400 THEN 1 ELSE 0 END), 0)
		FROM search_events WHERE day >= ? GROUP BY day`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]DailyPoint{}
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.Day, &p.Searches, &p.Users, &p.IPs, &p.ZeroResults); err != nil {
			return nil, err
		}
		byDay[p.Day] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 补齐没有数据的日期，保证序列连续。
	points := make([]DailyPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := dayOf(now.AddDate(0, 0, -i))
		p, ok := byDay[day]
		if !ok {
			p = DailyPoint{Day: day}
		}
		points = append(points, p)
	}
	return points, nil
}

func (s *Store) hourly(today string) ([]int, error) {
	hours := make([]int, 24)
	rows, err := s.db.Query(`SELECT hour, COUNT(*) FROM search_events WHERE day = ? GROUP BY hour`, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h, c int
		if err := rows.Scan(&h, &c); err != nil {
			return nil, err
		}
		if h >= 0 && h < 24 {
			hours[h] = c
		}
	}
	return hours, rows.Err()
}

func (s *Store) keywordRank(start string, zeroOnly bool, n int) ([]KeywordCount, error) {
	cond := ""
	if zeroOnly {
		cond = " AND result_total = 0 AND status < 400"
	}
	rows, err := s.db.Query(`SELECT keyword, COUNT(*) AS c, COUNT(DISTINCT NULLIF(username, ''))
		FROM search_events WHERE day >= ? AND keyword != ''`+cond+`
		GROUP BY keyword ORDER BY c DESC, keyword LIMIT ?`, start, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []KeywordCount{}
	for rows.Next() {
		var k KeywordCount
		if err := rows.Scan(&k.Keyword, &k.Count, &k.Users); err != nil {
			return nil, err
		}
		result = append(result, k)
	}
	return result, rows.Err()
}

func (s *Store) userRank(start string, n int) ([]UserCount, error) {
	rows, err := s.db.Query(`SELECT username, COUNT(*) AS c, MAX(created_at)
		FROM search_events WHERE day >= ? AND username != ''
		GROUP BY username ORDER BY c DESC, username LIMIT ?`, start, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UserCount{}
	for rows.Next() {
		var u UserCount
		var last int64
		if err := rows.Scan(&u.Username, &u.Count, &last); err != nil {
			return nil, err
		}
		u.LastAt = time.UnixMilli(last)
		result = append(result, u)
	}
	return result, rows.Err()
}

func (s *Store) ipRank(start string, n int) ([]IPCount, error) {
	rows, err := s.db.Query(`SELECT ip, COUNT(*) AS c, COUNT(DISTINCT NULLIF(username, '')), MAX(created_at)
		FROM search_events WHERE day >= ? AND ip != ''
		GROUP BY ip ORDER BY c DESC, ip LIMIT ?`, start, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []IPCount{}
	for rows.Next() {
		var ip IPCount
		var last int64
		if err := rows.Scan(&ip.IP, &ip.Count, &ip.Users, &last); err != nil {
			return nil, err
		}
		ip.LastAt = time.UnixMilli(last)
		result = append(result, ip)
	}
	return result, rows.Err()
}

func (s *Store) apiGroups(start string) ([]APIGroupStats, error) {
	rows, err := s.db.Query(`SELECT route_group, SUM(requests), SUM(errors_4xx), SUM(errors_5xx), SUM(latency_ms_sum)
		FROM api_hourly WHERE day >= ? GROUP BY route_group ORDER BY SUM(requests) DESC`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []APIGroupStats{}
	for rows.Next() {
		var g APIGroupStats
		var latencySum int64
		if err := rows.Scan(&g.Group, &g.Requests, &g.Errors4xx, &g.Errors5xx, &latencySum); err != nil {
			return nil, err
		}
		if g.Requests > 0 {
			g.AvgLatencyMs = math.Round(float64(latencySum)/float64(g.Requests)*10) / 10
		}
		result = append(result, g)
	}
	return result, rows.Err()
}

// UserActivity 返回每个用户最近一次搜索的时间与今日搜索次数。
func (s *Store) UserActivity(now time.Time) (map[string]UserActivity, error) {
	rows, err := s.db.Query(`SELECT username, MAX(created_at), SUM(CASE WHEN day = ? THEN 1 ELSE 0 END)
		FROM search_events WHERE username != '' GROUP BY username`, dayOf(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]UserActivity{}
	for rows.Next() {
		var name string
		var last int64
		var today int
		if err := rows.Scan(&name, &last, &today); err != nil {
			return nil, err
		}
		result[name] = UserActivity{LastSearchAt: time.UnixMilli(last), TodaySearches: today}
	}
	return result, rows.Err()
}
