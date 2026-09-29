package stats

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RecorderOptions 控制异步写入的节奏；零值使用默认值。
type RecorderOptions struct {
	QueueSize     int           // 事件队列容量，默认 10000
	BatchSize     int           // 攒够多少条立即写入，默认 200
	FlushInterval time.Duration // 事件写入间隔，默认 1 秒
	APIInterval   time.Duration // 接口访问量写入间隔，默认 1 分钟
}

func (o RecorderOptions) withDefaults() RecorderOptions {
	if o.QueueSize <= 0 {
		o.QueueSize = 10000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 200
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = time.Second
	}
	if o.APIInterval <= 0 {
		o.APIInterval = time.Minute
	}
	return o
}

type event struct {
	search *SearchEvent
	login  *LoginEvent
}

// Recorder 异步批量写入统计事件。调用方永不阻塞：队列满时丢弃并计数。
// nil 的 *Recorder 是合法的空操作，统计不可用时服务照常运行。
type Recorder struct {
	store *Store
	opts  RecorderOptions

	events  chan event
	flushes chan chan struct{}
	quit    chan struct{}
	stopped chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
	dropped   atomic.Int64

	// 接口访问量在内存里按小时累加，定期落盘。
	apiMu sync.Mutex
	api   map[hourKey]*hourAgg

	lastErrLog time.Time
}

// NewRecorder 创建记录器并启动写入协程。
func NewRecorder(store *Store, opts RecorderOptions) *Recorder {
	return newRecorder(store, opts, true)
}

func newRecorder(store *Store, opts RecorderOptions, start bool) *Recorder {
	opts = opts.withDefaults()
	r := &Recorder{
		store:   store,
		opts:    opts,
		events:  make(chan event, opts.QueueSize),
		flushes: make(chan chan struct{}),
		quit:    make(chan struct{}),
		stopped: make(chan struct{}),
		api:     map[hourKey]*hourAgg{},
	}
	if start {
		go r.loop()
	} else {
		close(r.stopped)
	}
	return r
}

func (r *Recorder) enqueue(e event) {
	if r == nil || r.closed.Load() {
		return
	}
	select {
	case r.events <- e:
	default:
		r.dropped.Add(1)
	}
}

// RecordSearch 记录一次搜索请求。
func (r *Recorder) RecordSearch(e SearchEvent) {
	r.enqueue(event{search: &e})
}

// RecordLogin 记录一次登录尝试。
func (r *Recorder) RecordLogin(e LoginEvent) {
	r.enqueue(event{login: &e})
}

// RecordAPI 累加一次接口访问。
func (r *Recorder) RecordAPI(group string, status int, latency time.Duration, at time.Time) {
	if r == nil || r.closed.Load() {
		return
	}
	key := hourKey{Day: dayOf(at), Hour: hourOf(at), Group: group}
	r.apiMu.Lock()
	agg := r.api[key]
	if agg == nil {
		agg = &hourAgg{}
		r.api[key] = agg
	}
	agg.Requests++
	switch {
	case status >= 500:
		agg.Errors5xx++
	case status >= 400:
		agg.Errors4xx++
	}
	agg.LatencyMsSum += latency.Milliseconds()
	r.apiMu.Unlock()
}

// Dropped 返回因队列满而丢弃的事件数。
func (r *Recorder) Dropped() int64 {
	if r == nil {
		return 0
	}
	return r.dropped.Load()
}

// Flush 同步写入所有已排队的事件与接口访问量。
func (r *Recorder) Flush() {
	if r == nil || r.closed.Load() {
		return
	}
	done := make(chan struct{})
	select {
	case r.flushes <- done:
		<-done
	case <-r.stopped:
	}
}

// Close 写入剩余数据并停止写入协程；可重复调用。
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		close(r.quit)
		<-r.stopped
	})
}

func (r *Recorder) loop() {
	defer close(r.stopped)
	ticker := time.NewTicker(r.opts.FlushInterval)
	defer ticker.Stop()
	apiTicker := time.NewTicker(r.opts.APIInterval)
	defer apiTicker.Stop()

	var searches []SearchEvent
	var logins []LoginEvent

	write := func(includeAPI bool) {
		var api map[hourKey]*hourAgg
		if includeAPI {
			api = r.takeAPI()
		}
		if err := r.store.writeBatch(searches, logins, api); err != nil {
			r.logError(err)
			if api != nil {
				r.restoreAPI(api)
			}
		}
		searches, logins = searches[:0], logins[:0]
	}
	add := func(e event) {
		if e.search != nil {
			searches = append(searches, *e.search)
		}
		if e.login != nil {
			logins = append(logins, *e.login)
		}
	}
	drain := func() {
		for {
			select {
			case e := <-r.events:
				add(e)
			default:
				return
			}
		}
	}

	for {
		select {
		case e := <-r.events:
			add(e)
			if len(searches)+len(logins) >= r.opts.BatchSize {
				write(false)
			}
		case <-ticker.C:
			write(false)
		case <-apiTicker.C:
			write(true)
		case done := <-r.flushes:
			drain()
			write(true)
			close(done)
		case <-r.quit:
			drain()
			write(true)
			return
		}
	}
}

func (r *Recorder) takeAPI() map[hourKey]*hourAgg {
	r.apiMu.Lock()
	defer r.apiMu.Unlock()
	if len(r.api) == 0 {
		return nil
	}
	taken := r.api
	r.api = map[hourKey]*hourAgg{}
	return taken
}

// restoreAPI 在写入失败时把聚合值放回内存，下次再试。
func (r *Recorder) restoreAPI(taken map[hourKey]*hourAgg) {
	r.apiMu.Lock()
	defer r.apiMu.Unlock()
	for k, v := range taken {
		cur := r.api[k]
		if cur == nil {
			r.api[k] = v
			continue
		}
		cur.Requests += v.Requests
		cur.Errors4xx += v.Errors4xx
		cur.Errors5xx += v.Errors5xx
		cur.LatencyMsSum += v.LatencyMsSum
	}
}

// logError 限频输出写入失败，避免数据库异常时刷屏。
func (r *Recorder) logError(err error) {
	if time.Since(r.lastErrLog) < time.Minute {
		return
	}
	r.lastErrLog = time.Now()
	fmt.Printf("[stats] 写入统计失败: %v\n", err)
}
