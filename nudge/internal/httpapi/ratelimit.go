package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter is a per-client token bucket. Client IPs live only in this map, in
// memory, and are dropped once idle; they are never logged or stored.
type Limiter struct {
	rate  float64 // tokens per second
	burst float64
	idle  time.Duration
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter(perSecond float64, burst int) *Limiter {
	return &Limiter{
		rate:    perSecond,
		burst:   float64(burst),
		idle:    5 * time.Minute,
		now:     time.Now,
		buckets: map[string]*bucket{},
	}
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	if now.Sub(l.lastSweep) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > l.idle {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
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

// clientIP uses the rightmost X-Forwarded-For entry, which on Cloud Run is
// the address Google's front end saw; entries to its left are client-supplied
// and can be spoofed.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
