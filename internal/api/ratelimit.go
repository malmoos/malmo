package api

import (
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/malmoos/malmo/internal/auth"
)

// Request-rate throttling (BRAIN_UI_PROTOCOL.md # Rate limiting & abuse). Two
// token-bucket planes guard the brain against a runaway client — a dashboard
// tab in a reconnect loop, a tight CLI poll, a compromised LAN device — without
// being internet-scale DoS defence (a named non-goal). Posture: throttle, never
// ban; in-memory state; resets on restart; log the source but never blacklist.
// The third plane (≤16 concurrent SSE streams per session) is a separate budget
// and lives in streamcap.go — opening a stream must not draw from plane 1.
const (
	// Plane 1 — per-session request rate, keyed on the malmo_session token over
	// authenticated short requests: 120 req/min sustained, burst 60. In a token
	// bucket the burst is the capacity and the sustained rate is the refill.
	sessionRateBurst  = 60
	sessionRatePerMin = 120

	// Plane 2 — per-IP request rate over the unauthenticated allowlist
	// (publicPaths): 30 req/min/IP. Capacity is one minute's budget.
	ipRateBurst  = 30
	ipRatePerMin = 30

	// Idle buckets are swept so a churn of short-lived sessions/IPs doesn't leak
	// map keys (no persistence — the whole limiter resets on restart). A bucket
	// fully refills within ~60s, so a 5-minute idle window only drops keys that
	// are genuinely dormant; a swept key is recreated full on its next request,
	// identical to a fully-refilled idle one.
	rateLimitIdleTTL   = 5 * time.Minute
	rateLimitReapEvery = 5 * time.Minute
)

// bucket is one token bucket's state: a fractional token count and the instant
// it was last refilled. Refill is lazy — computed on access from elapsed time,
// so there is no per-bucket timer.
type bucket struct {
	tokens float64
	last   time.Time
}

// bucketSet is a keyed family of token buckets sharing one capacity/refill rate
// (one plane). Safe for concurrent use; idle buckets are reaped opportunistically
// on access so the map can't grow without bound under client churn.
type bucketSet struct {
	mu         sync.Mutex
	capacity   float64 // max tokens == instantaneous burst
	refillRate float64 // tokens per second == sustained rate
	now        func() time.Time
	buckets    map[string]*bucket
	lastReap   time.Time
}

func newBucketSet(capacity, refillPerSec float64, now func() time.Time) *bucketSet {
	return &bucketSet{
		capacity:   capacity,
		refillRate: refillPerSec,
		now:        now,
		buckets:    map[string]*bucket{},
		lastReap:   now(),
	}
}

// allow takes one token for key. It returns (true, 0) when a token was
// available, or (false, retry) when the bucket is empty — retry is the time
// until one token refills. A sweep of idle buckets runs at most once per
// rateLimitReapEvery, piggy-backed on this call.
func (s *bucketSet) allow(key string) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Sub(s.lastReap) >= rateLimitReapEvery {
		s.lastReap = now
		s.sweepLocked(now)
	}

	b := s.buckets[key]
	if b == nil {
		b = &bucket{tokens: s.capacity, last: now}
		s.buckets[key] = b
	} else if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(s.capacity, b.tokens+elapsed*s.refillRate)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	deficit := 1 - b.tokens
	return false, time.Duration(deficit / s.refillRate * float64(time.Second))
}

// sweepLocked drops every bucket untouched for longer than rateLimitIdleTTL.
// Caller holds mu.
func (s *bucketSet) sweepLocked(now time.Time) {
	for k, b := range s.buckets {
		if now.Sub(b.last) > rateLimitIdleTTL {
			delete(s.buckets, k)
		}
	}
}

// rateLimiter bundles the two request-rate planes. Plane 3 (SSE concurrency) is
// streamCap, intentionally separate.
type rateLimiter struct {
	session *bucketSet
	ip      *bucketSet
}

func newRateLimiter(now func() time.Time) *rateLimiter {
	return &rateLimiter{
		session: newBucketSet(sessionRateBurst, sessionRatePerMin/60.0, now),
		ip:      newBucketSet(ipRateBurst, ipRatePerMin/60.0, now),
	}
}

// rateLimit is the middleware that enforces the two request-rate planes. It sits
// after authMiddleware (so identity is resolved) but before the mux, so it keys
// on the session token for authenticated requests and falls back to client IP on
// the unauthenticated allowlist. Long-lived streams are exempt — they're
// governed by the per-session stream cap, not the request-rate bucket.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStreaming(r) {
			next.ServeHTTP(w, r)
			return
		}
		if id, ok := auth.FromContext(r.Context()); ok {
			// Plane 1: authenticated short requests, keyed on the session token.
			if ok, retry := s.limiter.session.allow(id.Session.Token); !ok {
				slog.Info("rate limited", "scope", "session", "user_id", id.User.ID, "retry_after_s", retryAfterSeconds(retry))
				writeRateLimited(w, "session", retry)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Plane 2: the unauthenticated allowlist (publicPaths), keyed on client IP.
		// authMiddleware has already rejected any non-public request that lacks a
		// session, so reaching here means a public path: log the IP, never ban.
		ip := clientIP(r)
		if ok, retry := s.limiter.ip.allow(ip); !ok {
			slog.Warn("rate limited", "scope", "ip", "host", ip, "retry_after_s", retryAfterSeconds(retry))
			writeRateLimited(w, "ip", retry)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isStreaming reports whether the request targets a long-lived endpoint exempt
// from the per-session request-rate bucket (BRAIN_UI_PROTOCOL.md: SSE streams
// and the streaming /files/content transfers are not short requests). The SSE
// stream cap (streamcap.go) is their governor instead. The /files/content path
// is the file-manager download/upload transfer (FILES.md); listing it here keeps
// the exemption honest for when those handlers land.
func isStreaming(r *http.Request) bool {
	switch p := r.URL.Path; {
	case p == "/api/v1/events", p == "/api/v1/system/live":
		return true
	case strings.HasPrefix(p, "/api/v1/files/content"):
		return true
	default:
		return false
	}
}

// retryAfterSeconds rounds a refill delay up to whole seconds, floored at 1 — a
// throttled caller is always told to wait at least a second.
func retryAfterSeconds(d time.Duration) int {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs
}

type rateLimitedDetails struct {
	Scope       string `json:"scope"`
	RetryAfterS int    `json:"retry_after_s"`
}

type rateLimitedBody struct {
	Code    string             `json:"code"`
	Message string             `json:"message"`
	Details rateLimitedDetails `json:"details"`
}

// writeRateLimited emits the locked 429 contract (BRAIN_UI_PROTOCOL.md # Rate
// limiting & abuse): the {code, message, details} envelope with code
// "rate-limited" plus a Retry-After header in seconds. The message is
// plain-English for the dashboard; the raw scope/IP detail goes to slog only.
func writeRateLimited(w http.ResponseWriter, scope string, retry time.Duration) {
	secs := retryAfterSeconds(retry)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(rateLimitedBody{
		Code:    "rate-limited",
		Message: "malmo is busy — please retry in a moment.",
		Details: rateLimitedDetails{Scope: scope, RetryAfterS: secs},
	})
}
