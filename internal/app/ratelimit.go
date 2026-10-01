package app

import (
	"net"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// ipRateLimiter throttles requests per client IP. It fronts the login and
// register endpoints, where each attempt runs bcrypt and is a brute-force and
// CPU-exhaustion target.
type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

func newIPRateLimiter(r rate.Limit, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     r,
		burst:    burst,
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	limiter, ok := l.limiters[ip]
	if !ok {
		limiter = rate.NewLimiter(l.rate, l.burst)
		l.limiters[ip] = limiter
	}
	l.mu.Unlock()
	return limiter.Allow()
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !l.allow(clientIP(r)) {
			http.Error(w, "Too many attempts. Please wait and try again.", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// maxRequestBodyBytes caps request bodies on authenticated endpoints. It is
// larger than any text save and covers the multipart upload path, so a single
// request cannot stream unbounded data into memory or disk.
const maxRequestBodyBytes = 64 << 20

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
