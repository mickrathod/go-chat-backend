package limiter

import (
	"sync"
	"time"
)

// RateLimiter implements a thread-safe token bucket rate limiter
type RateLimiter struct {
	lastCheck time.Time
	tokens    float64
	capacity  float64
	rate      float64 // tokens added per second
	mu        sync.Mutex
}

// NewRateLimiter creates a new Token Bucket RateLimiter instance
func NewRateLimiter(rate, capacity float64) *RateLimiter {
	return &RateLimiter{
		lastCheck: time.Now(),
		tokens:    capacity,
		capacity:  capacity,
		rate:      rate,
	}
}

// Allow checks if a request is permitted under current rate limit rules
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.lastCheck).Seconds()
	rl.lastCheck = now

	rl.tokens += elapsed * rl.rate
	if rl.tokens > rl.capacity {
		rl.tokens = rl.capacity
	}

	if rl.tokens >= 1.0 {
		rl.tokens -= 1.0
		return true
	}
	return false
}
