package api

import (
	"sync"
	"time"
)

// loginLimiter 在固定窗口内按键（目前是客户端 IP）累计登录失败次数，达到上限后锁定一段时间。
type loginLimiter struct {
	mu       sync.Mutex
	entries  map[string]loginAttempts
	maxFails int
	window   time.Duration
	lockout  time.Duration
	now      func() time.Time
}

type loginAttempts struct {
	fails       int
	windowStart time.Time
	lockedUntil time.Time
}

// 超过该数量时顺带清理过期条目，防止被大量伪造的 IP/用户名撑大内存。
const loginLimiterPruneThreshold = 10000

func newLoginLimiter(maxFails int, window, lockout time.Duration) *loginLimiter {
	return &loginLimiter{
		entries:  make(map[string]loginAttempts),
		maxFails: maxFails,
		window:   window,
		lockout:  lockout,
		now:      time.Now,
	}
}

// retryAfter 返回任一键仍处于锁定期时的剩余时长；未锁定返回 0。
func (l *loginLimiter) retryAfter(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	var longest time.Duration
	for _, key := range keys {
		if remaining := l.entries[key].lockedUntil.Sub(now); remaining > longest {
			longest = remaining
		}
	}
	return longest
}

func (l *loginLimiter) recordFailure(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if len(l.entries) > loginLimiterPruneThreshold {
		l.pruneLocked(now)
	}
	for _, key := range keys {
		entry := l.entries[key]
		if now.Sub(entry.windowStart) > l.window {
			entry = loginAttempts{windowStart: now}
		}
		entry.fails++
		if entry.fails >= l.maxFails {
			entry.lockedUntil = now.Add(l.lockout)
			entry.fails = 0
			entry.windowStart = now
		}
		l.entries[key] = entry
	}
}

func (l *loginLimiter) recordSuccess(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.entries, key)
	}
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for key, entry := range l.entries {
		if now.After(entry.lockedUntil) && now.Sub(entry.windowStart) > l.window {
			delete(l.entries, key)
		}
	}
}
