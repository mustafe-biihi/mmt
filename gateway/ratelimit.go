package main

import (
	"sync"
	"time"
)

// rateLimiter is an in-memory token bucket per client key.
// For multiple gateway replicas, replace with a Redis-backed limiter.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	idleTTL time.Duration
	buckets map[string]*bucket
	now     func() time.Time
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps float64, burst int, idleTTL time.Duration) *rateLimiter {
	return &rateLimiter{
		rate: rps, burst: float64(burst), idleTTL: idleTTL,
		buckets: map[string]*bucket{}, now: time.Now,
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if now.Sub(l.lastGC) > l.idleTTL {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.idleTTL {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
