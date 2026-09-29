package stats

import (
	"sync"
	"testing"
	"time"
)

func TestRecorderWritesAfterFlush(t *testing.T) {
	s, _ := openTestStore(t)
	r := NewRecorder(s, RecorderOptions{})
	defer r.Close()

	r.RecordSearch(search("s1", "alice", "1.1.1.1", "kw", 3, testNow))
	r.RecordSearch(search("s1", "alice", "1.1.1.1", "kw", 9, testNow.Add(time.Second)))
	r.RecordLogin(LoginEvent{Username: "alice", IP: "1.1.1.1", Success: true, At: testNow})
	r.RecordAPI("search", 200, 50*time.Millisecond, testNow)
	r.RecordAPI("search", 502, 150*time.Millisecond, testNow)
	r.Flush()

	history := mustHistory(t, s, HistoryFilter{})
	if history.Total != 1 || history.Items[0].RequestCount != 2 || history.Items[0].ResultTotal != 9 {
		t.Fatalf("搜索记录错误: %+v", history)
	}
	logins, _ := s.Logins(LoginFilter{}, 1, 10)
	if logins.Total != 1 {
		t.Fatalf("登录记录错误: %+v", logins)
	}
	o, err := s.Overview(testNow, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.API) != 1 || o.API[0].Requests != 2 || o.API[0].Errors5xx != 1 || o.API[0].AvgLatencyMs != 100 {
		t.Fatalf("接口统计错误: %+v", o.API)
	}
}

func TestRecorderDropsWhenQueueFull(t *testing.T) {
	s, _ := openTestStore(t)
	// 写入协程不启动，队列容量 2：第 3 条起必须立即丢弃而不是阻塞调用方。
	r := newRecorder(s, RecorderOptions{QueueSize: 2}, false)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			r.RecordSearch(search(string(rune('a'+i)), "alice", "1.1.1.1", "kw", 1, testNow))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("队列满时记录调用不能阻塞")
	}
	if got := r.Dropped(); got != 3 {
		t.Fatalf("丢弃计数应为 3，得到 %d", got)
	}
}

func TestRecorderCloseFlushesPending(t *testing.T) {
	s, _ := openTestStore(t)
	r := NewRecorder(s, RecorderOptions{FlushInterval: time.Hour})
	r.RecordSearch(search("s1", "alice", "1.1.1.1", "kw", 1, testNow))
	r.Close()

	if got := mustHistory(t, s, HistoryFilter{}).Total; got != 1 {
		t.Fatalf("Close 应写入尚未落盘的记录，得到 %d 条", got)
	}
	// 关闭后再记录不能 panic。
	r.RecordSearch(search("s2", "alice", "1.1.1.1", "kw", 1, testNow))
	r.Close()
}

func TestNilRecorderIsNoop(t *testing.T) {
	var r *Recorder
	r.RecordSearch(SearchEvent{})
	r.RecordLogin(LoginEvent{})
	r.RecordAPI("search", 200, time.Millisecond, testNow)
	r.Flush()
	r.Close()
	if r.Dropped() != 0 {
		t.Fatal("nil 记录器应为空操作")
	}
}

func TestRecorderConcurrentUse(t *testing.T) {
	s, _ := openTestStore(t)
	r := NewRecorder(s, RecorderOptions{})
	defer r.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.RecordSearch(search("shared", "alice", "1.1.1.1", "kw", i, testNow))
			r.RecordAPI("search", 200, time.Millisecond, testNow)
		}(i)
	}
	wg.Wait()
	r.Flush()

	row := mustHistory(t, s, HistoryFilter{}).Items[0]
	if row.RequestCount != 20 || row.ResultTotal != 19 {
		t.Fatalf("并发合并错误: %+v", row)
	}
}
