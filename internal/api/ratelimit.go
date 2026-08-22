package api

import (
	"sync"
	"time"
)

// IPRateLimiter is a minimal fixed-window per-IP limiter matching the
// provisional quotas in docs/decisions/DEC-005.md (10 creation/min, 100
// resolution/min per IP). Tested to confirm the *mechanism* enforces a
// limit correctly — production would use a distributed store (not
// in-process memory) once more than one instance runs.
type IPRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*windowCount
}

type windowCount struct {
	count     int
	windowEnd time.Time
}

func NewIPRateLimiter(limit int, window time.Duration) *IPRateLimiter {
	return &IPRateLimiter{limit: limit, window: window, counts: make(map[string]*windowCount)}
}

// Allow reports whether the given IP may proceed, incrementing its count
// if so. A request that arrives after the current window has elapsed
// starts a fresh window.
func (l *IPRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	wc, ok := l.counts[ip]
	if !ok || now.After(wc.windowEnd) {
		l.counts[ip] = &windowCount{count: 1, windowEnd: now.Add(l.window)}
		return true
	}
	if wc.count >= l.limit {
		return false
	}
	wc.count++
	return true
}
