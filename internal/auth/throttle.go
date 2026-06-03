package auth

import (
	"sync"
	"time"
)

// LoginThrottle rate-limits the dashboard login path (AUTH.md # Rate limiting).
// It protects the deliberately-expensive PAM round-trip behind two independent
// gates, both checked before VerifyPassword:
//
//   - Per-username exponential backoff, escalating to a 15-minute lock. This is
//     the primary defence against guessing one account's password.
//   - Per-IP token bucket (10 attempts/minute). This is the backstop against
//     username-spray from a single source — each fresh username starts with no
//     per-username delay, so only the IP gate slows a spray. It throttles, it
//     never locks (most boxes only ever see LAN IPs; banning a LAN address is
//     wrong).
//
// State is in-memory and resets on brain restart — AUTH.md requires no
// persistence. The maps grow one entry per distinct username / IP that has
// failed; for a LAN home box that is negligible and the per-IP gate bounds the
// growth rate of attacker-supplied usernames to 10/min. Eviction waits for a
// timer seam the brain doesn't have yet.
type LoginThrottle struct {
	// Clock returns "now"; injected for deterministic tests (mirrors Manager).
	Clock func() time.Time

	mu    sync.Mutex
	users map[string]*userAttempts
	ips   map[string]*ipBucket
}

// Per-username backoff schedule (AUTH.md # Rate limiting). The value is the
// minimum wait since the last failed attempt before another is allowed; at the
// lock threshold the wait becomes the full 15-minute lock.
const (
	backoff3  = 1 * time.Second
	backoff5  = 10 * time.Second
	backoff10 = 60 * time.Second

	lockThreshold = 20
	lockDuration  = 15 * time.Minute
)

// Per-IP token bucket: 10 attempts per minute, refilled continuously.
const (
	ipBucketCapacity = 10.0
	ipRefillPerSec   = ipBucketCapacity / 60.0
)

type userAttempts struct {
	fails    int
	lastFail time.Time
}

type ipBucket struct {
	tokens   float64
	lastFill time.Time
}

// NewLoginThrottle returns a throttle keyed on a wall clock.
func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{
		Clock: time.Now,
		users: map[string]*userAttempts{},
		ips:   map[string]*ipBucket{},
	}
}

func (t *LoginThrottle) now() time.Time {
	if t.Clock != nil {
		return t.Clock()
	}
	return time.Now()
}

// AllowAttempt is called immediately before the PAM round-trip. It consumes one
// per-IP token and checks the per-username backoff/lock. When the attempt must
// be rejected without touching PAM it returns ok=false and a best-effort hint
// for how long the caller should wait. The username gate and the IP gate return
// the same shape so a rejection never leaks whether the username exists.
func (t *LoginThrottle) AllowAttempt(username, ip string) (ok bool, retryAfter time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	// Per-IP bucket first: every attempt spends a token, so a spray across many
	// usernames still drains the source's budget even when each username is
	// individually below its backoff threshold.
	if d := t.spendIPToken(ip, now); d > 0 {
		return false, d
	}
	if d := t.usernameWait(username, now); d > 0 {
		return false, d
	}
	return true, 0
}

// spendIPToken refills the bucket by elapsed time, then spends one token.
// Returns 0 when a token was available, or the wait until the next token.
func (t *LoginThrottle) spendIPToken(ip string, now time.Time) time.Duration {
	b := t.ips[ip]
	if b == nil {
		b = &ipBucket{tokens: ipBucketCapacity, lastFill: now}
		t.ips[ip] = b
	}
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = min(ipBucketCapacity, b.tokens+elapsed*ipRefillPerSec)
		b.lastFill = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	// Wait until the bucket accrues one whole token.
	return time.Duration((1 - b.tokens) / ipRefillPerSec * float64(time.Second))
}

// usernameWait returns how long the username must wait before another attempt,
// or 0 if it may proceed now.
func (t *LoginThrottle) usernameWait(username string, now time.Time) time.Duration {
	ua := t.users[username]
	if ua == nil || ua.fails == 0 {
		return 0
	}
	delay := usernameBackoff(ua.fails)
	if delay == 0 {
		return 0
	}
	if elapsed := now.Sub(ua.lastFail); elapsed < delay {
		return delay - elapsed
	}
	return 0
}

// usernameBackoff maps a consecutive-failure count to its required cooldown.
func usernameBackoff(fails int) time.Duration {
	switch {
	case fails >= lockThreshold:
		return lockDuration
	case fails >= 10:
		return backoff10
	case fails >= 5:
		return backoff5
	case fails >= 3:
		return backoff3
	default:
		return 0
	}
}

// RecordFailure is called after a failed VerifyPassword (bad password OR unknown
// username — both, so the throttle can't be used to enumerate accounts). It
// increments the per-username counter and reports whether this failure is the
// one that crossed into the 15-minute lock, so the caller emits the
// login.lockout audit exactly once per lock (not on every subsequent attempt).
func (t *LoginThrottle) RecordFailure(username string) (lockedNow bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ua := t.users[username]
	if ua == nil {
		ua = &userAttempts{}
		t.users[username] = ua
	}
	ua.fails++
	ua.lastFail = t.now()
	return ua.fails == lockThreshold
}

// RecordSuccess resets the per-username counter on a successful login
// (AUTH.md: "Successful login resets the per-username counter"). The per-IP
// bucket is intentionally untouched — it throttles request rate, not auth
// outcome.
func (t *LoginThrottle) RecordSuccess(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.users, username)
}
