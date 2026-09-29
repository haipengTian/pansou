package stats

import "time"

// SearchEvent 是一次搜索请求。同一 SessionID 的多个请求会合并为一条记录。
type SearchEvent struct {
	SessionID   string
	Username    string
	Role        string
	IP          string
	UserAgent   string
	Keyword     string
	SourceType  string
	CloudTypes  string
	Refresh     bool
	ResultTotal int
	LatencyMs   int64
	Status      int
	Error       string
	At          time.Time
}

// LoginEvent 是一次登录尝试。
type LoginEvent struct {
	Username  string
	IP        string
	UserAgent string
	Success   bool
	Reason    string
	At        time.Time
}

type hourKey struct {
	Day   string
	Hour  int
	Group string
}

type hourAgg struct {
	Requests     int64
	Errors4xx    int64
	Errors5xx    int64
	LatencyMsSum int64
}

// SearchRow 是搜索历史中的一条记录。
type SearchRow struct {
	ID           int64     `json:"id"`
	SessionID    string    `json:"session_id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	IP           string    `json:"ip"`
	UserAgent    string    `json:"user_agent"`
	Keyword      string    `json:"keyword"`
	SourceType   string    `json:"source_type"`
	CloudTypes   string    `json:"cloud_types"`
	Refresh      bool      `json:"refresh"`
	ResultTotal  int       `json:"result_total"`
	LatencyMs    int64     `json:"latency_ms"`
	RequestCount int       `json:"request_count"`
	Status       int       `json:"status"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// HistoryFilter 是搜索历史的筛选条件；From/To 为本地日期 YYYY-MM-DD（含端点）。
type HistoryFilter struct {
	Username string
	Keyword  string
	IP       string
	From     string
	To       string
	ZeroOnly bool
}

// HistoryPage 是一页搜索历史。
type HistoryPage struct {
	Total int         `json:"total"`
	Items []SearchRow `json:"items"`
}

// LoginRow 是一条登录记录。
type LoginRow struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Success   bool      `json:"success"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// LoginFilter 是登录记录的筛选条件；Success 为 nil 表示不限。
type LoginFilter struct {
	Username string
	IP       string
	Success  *bool
	From     string
	To       string
}

// LoginPage 是一页登录记录。
type LoginPage struct {
	Total int        `json:"total"`
	Items []LoginRow `json:"items"`
}

// Counts 是一段时间内的搜索量、活跃用户数与独立 IP 数。
type Counts struct {
	Searches int `json:"searches"`
	Users    int `json:"users"`
	IPs      int `json:"ips"`
}

// PeriodStats 是统计区间内的质量指标（只计算状态码 < 400 的搜索）。
type PeriodStats struct {
	Searches     int     `json:"searches"`
	ZeroResults  int     `json:"zero_results"`
	ZeroRate     float64 `json:"zero_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P95LatencyMs int64   `json:"p95_latency_ms"`
	Errors       int     `json:"errors"`
}

// DailyPoint 是按天序列中的一个点。
type DailyPoint struct {
	Day         string `json:"day"`
	Searches    int    `json:"searches"`
	Users       int    `json:"users"`
	IPs         int    `json:"ips"`
	ZeroResults int    `json:"zero_results"`
}

// KeywordCount 是关键词排行中的一项。
type KeywordCount struct {
	Keyword string `json:"keyword"`
	Count   int    `json:"count"`
	Users   int    `json:"users"`
}

// UserCount 是用户排行中的一项。
type UserCount struct {
	Username string    `json:"username"`
	Count    int       `json:"count"`
	LastAt   time.Time `json:"last_at"`
}

// IPCount 是 IP 排行中的一项。
type IPCount struct {
	IP     string    `json:"ip"`
	Count  int       `json:"count"`
	Users  int       `json:"users"`
	LastAt time.Time `json:"last_at"`
}

// APIGroupStats 是接口分组在统计区间内的访问量。
type APIGroupStats struct {
	Group        string  `json:"group"`
	Requests     int64   `json:"requests"`
	Errors4xx    int64   `json:"errors_4xx"`
	Errors5xx    int64   `json:"errors_5xx"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// Overview 是访问统计页的全部数据。
type Overview struct {
	Days         int             `json:"days"`
	Today        Counts          `json:"today"`
	Total        Counts          `json:"total"`
	Period       PeriodStats     `json:"period"`
	Daily        []DailyPoint    `json:"daily"`
	Hourly       []int           `json:"hourly"`
	TopKeywords  []KeywordCount  `json:"top_keywords"`
	ZeroKeywords []KeywordCount  `json:"zero_keywords"`
	TopUsers     []UserCount     `json:"top_users"`
	TopIPs       []IPCount       `json:"top_ips"`
	API          []APIGroupStats `json:"api"`
}

// UserActivity 是单个用户的搜索活跃度。
type UserActivity struct {
	LastSearchAt  time.Time
	TodaySearches int
}
