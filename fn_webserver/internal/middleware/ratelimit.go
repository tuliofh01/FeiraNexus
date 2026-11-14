package middleware

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/views"
)

// RateLimiter implements a per-IP TOKEN BUCKET in memory.
//
// DIDACTIC — how a token bucket works:
//   - Each IP owns a bucket that refills at `rate` tokens/second, capped at
//     `burst` tokens. Each request consumes 1 token. Empty bucket → 429.
//   - Refill is computed lazily from elapsed time (no per-visitor goroutine).
//   - Single mutex guards the map: simple and correct. Fine for one instance.
//
// SUGGESTION (scale-out): this is per-process. Multiple replicas = N× the
// limit. For production use a shared store (Redis `INCR` + `EXPIRE`, or
// golang.org/x/time/rate + Redis). Also consider trusting X-Forwarded-For
// only behind DigitalOcean's proxy (gin's default trusted proxies are okay
// for local dev — review them for production).
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*bucket
	rate     float64 // tokens per second
	burst    float64 // bucket capacity
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter creates a limiter allowing `rate` req/s per IP with a
// one-time `burst` cushion (burst absorbs traffic spikes; rate is the
// sustained limit).
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	l := &RateLimiter{
		visitors: make(map[string]*bucket),
		rate:     rate,
		burst:    float64(burst),
	}
	go l.cleanupLoop()
	return l
}

// Allow reports whether the IP may proceed (and consumes a token if so).
func (l *RateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.visitors[ip]
	if !ok {
		l.visitors[ip] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	// Refill based on elapsed time since last visit.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// cleanupLoop periodically drops idle visitors so the map can't grow forever
// (a memory-exhaustion vector if left unbounded).
//
// SUGGESTION: if you need precise eviction, switch to an LRU cache with TTL
// (github.com/hashicorp/golang-lru/v2 with expiration).
func (l *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C { // runs for the life of the process (one goroutine)
		cutoff := time.Now().Add(-10 * time.Minute)
		l.mu.Lock()
		for ip, b := range l.visitors {
			if b.last.Before(cutoff) {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// Limit is the Gin middleware using the limiter. Registered on auth routes.
//
// Note the envelope: it matches views.APIResponse so clients always parse
// errors the same shape — a good habit even in middleware.
func Limit(l *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.Allow(c.ClientIP()) {
			views.RenderError(c, 429, "rate limit exceeded, slow down")
			c.Abort()
			return
		}
		c.Next()
	}
}
