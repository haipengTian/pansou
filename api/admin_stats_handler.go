package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/model"
	"pansou/stats"
)

const (
	defaultOverviewDays = 30
	maxOverviewDays     = 366
	overviewTopN        = 10
)

// registerStatsRoutes 注册 /api/admin/stats/*（调用方已挂好 RequireAdmin）。
func registerStatsRoutes(admin *gin.RouterGroup) {
	group := admin.Group("/stats")
	group.GET("/overview", StatsOverviewHandler)
	group.GET("/searches", StatsSearchesHandler)
	group.GET("/searches/export", StatsSearchesExportHandler)
	group.GET("/logins", StatsLoginsHandler)
}

func requireStats(c *gin.Context) bool {
	if statsStore == nil {
		respondError(c, http.StatusServiceUnavailable, "统计未启用：统计数据库不可用")
		return false
	}
	return true
}

func intQuery(c *gin.Context, key string, def int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return def
}

// dayQuery 读取 YYYY-MM-DD 形式的日期参数；为空返回空字符串。
func dayQuery(c *gin.Context, key string) (string, bool) {
	v := strings.TrimSpace(c.Query(key))
	if v == "" {
		return "", true
	}
	if _, err := time.ParseInLocation("2006-01-02", v, time.Local); err != nil {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("参数 %s 应为 YYYY-MM-DD 格式", key))
		return "", false
	}
	return v, true
}

func boolQuery(c *gin.Context, key string) bool {
	v := strings.ToLower(c.Query(key))
	return v == "1" || v == "true"
}

func historyFilterFrom(c *gin.Context) (stats.HistoryFilter, bool) {
	from, ok := dayQuery(c, "from")
	if !ok {
		return stats.HistoryFilter{}, false
	}
	to, ok := dayQuery(c, "to")
	if !ok {
		return stats.HistoryFilter{}, false
	}
	return stats.HistoryFilter{
		Username: strings.TrimSpace(c.Query("username")),
		Keyword:  strings.TrimSpace(c.Query("keyword")),
		IP:       strings.TrimSpace(c.Query("ip")),
		From:     from,
		To:       to,
		ZeroOnly: boolQuery(c, "zero_only"),
	}, true
}

// StatsOverviewHandler 返回访问统计概览。
func StatsOverviewHandler(c *gin.Context) {
	if !requireStats(c) {
		return
	}
	days := intQuery(c, "days", defaultOverviewDays)
	if days < 1 || days > maxOverviewDays {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("days 应在 1-%d 之间", maxOverviewDays))
		return
	}
	overview, err := statsStore.Overview(time.Now(), days, overviewTopN)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "查询统计失败: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(overview))
}

// StatsSearchesHandler 分页返回搜索历史。
func StatsSearchesHandler(c *gin.Context) {
	if !requireStats(c) {
		return
	}
	filter, ok := historyFilterFrom(c)
	if !ok {
		return
	}
	page, err := statsStore.History(filter, intQuery(c, "page", 1), intQuery(c, "page_size", 20))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "查询搜索历史失败: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

// StatsSearchesExportHandler 按筛选条件导出搜索历史为 CSV（流式写出）。
func StatsSearchesExportHandler(c *gin.Context) {
	if !requireStats(c) {
		return
	}
	filter, ok := historyFilterFrom(c)
	if !ok {
		return
	}

	filename := fmt.Sprintf("pansou-search-history-%s.csv", time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)

	// BOM 让 Excel 按 UTF-8 打开，中文不乱码。
	_, _ = c.Writer.Write([]byte("\xEF\xBB\xBF"))
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"时间", "用户", "角色", "IP", "关键词", "来源", "网盘类型", "结果数", "请求次数", "首次耗时(ms)", "状态码", "错误", "强制刷新", "User-Agent"})

	err := statsStore.EachSearch(filter, func(r stats.SearchRow) error {
		return w.Write([]string{
			r.CreatedAt.Format("2006-01-02 15:04:05"),
			r.Username,
			r.Role,
			r.IP,
			r.Keyword,
			r.SourceType,
			r.CloudTypes,
			strconv.Itoa(r.ResultTotal),
			strconv.Itoa(r.RequestCount),
			strconv.FormatInt(r.LatencyMs, 10),
			strconv.Itoa(r.Status),
			r.Error,
			strconv.FormatBool(r.Refresh),
			r.UserAgent,
		})
	})
	w.Flush()
	if err != nil {
		// 响应头已发出，只能记录错误；客户端会拿到截断的文件。
		fmt.Printf("[stats] 导出搜索历史中断: %v\n", err)
	}
}

// StatsLoginsHandler 分页返回登录记录。
func StatsLoginsHandler(c *gin.Context) {
	if !requireStats(c) {
		return
	}
	from, ok := dayQuery(c, "from")
	if !ok {
		return
	}
	to, ok := dayQuery(c, "to")
	if !ok {
		return
	}
	filter := stats.LoginFilter{
		Username: strings.TrimSpace(c.Query("username")),
		IP:       strings.TrimSpace(c.Query("ip")),
		From:     from,
		To:       to,
	}
	switch strings.ToLower(c.Query("success")) {
	case "true", "1":
		v := true
		filter.Success = &v
	case "false", "0":
		v := false
		filter.Success = &v
	}
	page, err := statsStore.Logins(filter, intQuery(c, "page", 1), intQuery(c, "page_size", 20))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "查询登录记录失败: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}
