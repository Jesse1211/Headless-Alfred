package auth

import (
	"sync"
	"time"
)

// RateLimiter is a simple per-key token bucket: each key gets `capacity`
// permits per `window`, refilled smoothly.
type RateLimiter struct {
	capacity int
	window   time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimiter(capacity int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		capacity: capacity,
		window:   window,
		buckets:  make(map[string]*bucket),
	}
}

// Allow consumes one token for the key. Returns true if allowed.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(r.capacity), last: now}
		r.buckets[key] = b
	}
	// Refill.
	elapsed := now.Sub(b.last).Seconds()
	refill := elapsed * float64(r.capacity) / r.window.Seconds()
	b.tokens += refill
	if b.tokens > float64(r.capacity) {
		b.tokens = float64(r.capacity)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
