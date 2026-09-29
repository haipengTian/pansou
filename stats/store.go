// Package stats 记录搜索历史、登录记录与接口访问量，供管理后台监控使用。
//
// 数据存放在 SQLite（纯 Go 驱动 modernc.org/sqlite，镜像以 CGO_ENABLED=0 构建）。
// 写入全部经由 Recorder 异步批量完成，不阻塞搜索请求；读取只来自管理后台。
// 按天、按小时的统计依赖写入时由 Go 按本地时区算好的 day/hour 列，
// 不依赖 SQLite 的 localtime（纯 Go 驱动下其时区行为不可靠）。
package stats

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

const dayLayout = "2006-01-02"

// schema 按版本顺序执行；只追加，不修改已发布的条目。
var migrations = []string{
	`CREATE TABLE search_events (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id       TEXT    NOT NULL UNIQUE,
		username         TEXT    NOT NULL DEFAULT '',
		role             TEXT    NOT NULL DEFAULT '',
		ip               TEXT    NOT NULL DEFAULT '',
		user_agent       TEXT    NOT NULL DEFAULT '',
		keyword          TEXT    NOT NULL DEFAULT '',
		source_type      TEXT    NOT NULL DEFAULT '',
		cloud_types      TEXT    NOT NULL DEFAULT '',
		refresh          INTEGER NOT NULL DEFAULT 0,
		result_total     INTEGER NOT NULL DEFAULT 0,
		first_latency_ms INTEGER NOT NULL DEFAULT 0,
		request_count    INTEGER NOT NULL DEFAULT 1,
		status           INTEGER NOT NULL DEFAULT 0,
		error            TEXT    NOT NULL DEFAULT '',
		day              TEXT    NOT NULL,
		hour             INTEGER NOT NULL,
		created_at       INTEGER NOT NULL,
		updated_at       INTEGER NOT NULL
	);
	CREATE INDEX idx_search_created ON search_events(created_at);
	CREATE INDEX idx_search_day ON search_events(day);
	CREATE INDEX idx_search_username ON search_events(username);
	CREATE INDEX idx_search_keyword ON search_events(keyword);
	CREATE INDEX idx_search_ip ON search_events(ip);

	CREATE TABLE login_events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT    NOT NULL DEFAULT '',
		ip         TEXT    NOT NULL DEFAULT '',
		user_agent TEXT    NOT NULL DEFAULT '',
		success    INTEGER NOT NULL,
		reason     TEXT    NOT NULL DEFAULT '',
		day        TEXT    NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX idx_login_created ON login_events(created_at);
	CREATE INDEX idx_login_username ON login_events(username);
	CREATE INDEX idx_login_ip ON login_events(ip);

	CREATE TABLE api_hourly (
		day            TEXT    NOT NULL,
		hour           INTEGER NOT NULL,
		route_group    TEXT    NOT NULL,
		requests       INTEGER NOT NULL DEFAULT 0,
		errors_4xx     INTEGER NOT NULL DEFAULT 0,
		errors_5xx     INTEGER NOT NULL DEFAULT 0,
		latency_ms_sum INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (day, hour, route_group)
	);`,
}

// Store 是统计数据库的读写入口，可并发使用。
type Store struct {
	db   *sql.DB
	path string
}

// Open 打开（必要时创建）统计数据库并执行迁移。
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开统计数据库失败: %w", err)
	}
	// WAL 下读写可并发；写入只来自 Recorder 的单个协程。
	db.SetMaxOpenConns(4)

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("初始化 schema_version 失败: %w", err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("执行第 %d 个迁移失败: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	return s.db.Close()
}

// SizeBytes 返回数据库文件（含 WAL）当前占用的字节数。
func (s *Store) SizeBytes() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(s.path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func dayOf(t time.Time) string {
	return t.In(time.Local).Format(dayLayout)
}

func hourOf(t time.Time) int {
	return t.In(time.Local).Hour()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// writeBatch 在一个事务里写入一批事件与按小时聚合的接口访问量。
func (s *Store) writeBatch(searches []SearchEvent, logins []LoginEvent, hourly map[hourKey]*hourAgg) error {
	if len(searches) == 0 && len(logins) == 0 && len(hourly) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if len(searches) > 0 {
		// 同一会话（前端一次搜索的预热与多轮补齐）合并为一条：请求数累加、结果数取最大，
		// 先失败后成功时以成功为准。SET 中的列引用均为更新前的旧值。
		stmt, err := tx.Prepare(`
			INSERT INTO search_events (session_id, username, role, ip, user_agent, keyword, source_type, cloud_types,
				refresh, result_total, first_latency_ms, request_count, status, error, day, hour, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
				request_count = request_count + 1,
				result_total  = MAX(result_total, excluded.result_total),
				status = CASE WHEN status >= 400 AND excluded.status < 400 THEN excluded.status ELSE status END,
				error  = CASE WHEN status >= 400 AND excluded.status < 400 THEN '' ELSE error END,
				updated_at    = MAX(updated_at, excluded.updated_at)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range searches {
			ms := e.At.UnixMilli()
			if _, err := stmt.Exec(e.SessionID, e.Username, e.Role, e.IP, e.UserAgent, e.Keyword, e.SourceType, e.CloudTypes,
				boolInt(e.Refresh), e.ResultTotal, e.LatencyMs, e.Status, e.Error, dayOf(e.At), hourOf(e.At), ms, ms); err != nil {
				return fmt.Errorf("写入搜索记录失败: %w", err)
			}
		}
	}

	if len(logins) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO login_events (username, ip, user_agent, success, reason, day, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range logins {
			if _, err := stmt.Exec(e.Username, e.IP, e.UserAgent, boolInt(e.Success), e.Reason, dayOf(e.At), e.At.UnixMilli()); err != nil {
				return fmt.Errorf("写入登录记录失败: %w", err)
			}
		}
	}

	if len(hourly) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO api_hourly (day, hour, route_group, requests, errors_4xx, errors_5xx, latency_ms_sum)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(day, hour, route_group) DO UPDATE SET
				requests       = requests + excluded.requests,
				errors_4xx     = errors_4xx + excluded.errors_4xx,
				errors_5xx     = errors_5xx + excluded.errors_5xx,
				latency_ms_sum = latency_ms_sum + excluded.latency_ms_sum`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for k, v := range hourly {
			if _, err := stmt.Exec(k.Day, k.Hour, k.Group, v.Requests, v.Errors4xx, v.Errors5xx, v.LatencyMsSum); err != nil {
				return fmt.Errorf("写入接口统计失败: %w", err)
			}
		}
	}

	return tx.Commit()
}
