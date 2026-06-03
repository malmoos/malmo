package auth

import (
	"strconv"
	"testing"
	"time"
)

// newTestThrottle returns a throttle on a hand-cranked clock. Advance time with
// `*clk = clk.Add(d)`.
func newTestThrottle() (thr *LoginThrottle, clk *time.Time) {
	cur := time.Unix(1_700_000_000, 0)
	thr = NewLoginThrottle()
	p := &cur
	thr.Clock = func() time.Time { return *p }
	return thr, p
}

func TestUsernameBackoffTiers(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{0, 0}, {1, 0}, {2, 0},
		{3, backoff3}, {4, backoff3},
		{5, backoff5}, {9, backoff5},
		{10, backoff10}, {19, backoff10},
		{20, lockDuration}, {25, lockDuration},
	}
	for _, c := range cases {
		if got := usernameBackoff(c.fails); got != c.want {
			t.Errorf("usernameBackoff(%d) = %v, want %v", c.fails, got, c.want)
		}
	}
}

func TestUsernameWaitExpiresAfterCooldown(t *testing.T) {
	thr, clk := newTestThrottle()
	const user = "andrei"
	for i := 0; i < 3; i++ {
		thr.RecordFailure(user)
	}
	if w := thr.usernameWait(user, *clk); w != backoff3 {
		t.Fatalf("immediately after 3rd fail: wait=%v, want %v", w, backoff3)
	}
	// Mid-cooldown: still positive, but less than the full tier.
	*clk = clk.Add(500 * time.Millisecond)
	if w := thr.usernameWait(user, *clk); w <= 0 || w >= backoff3 {
		t.Fatalf("mid-cooldown wait=%v, want in (0,%v)", w, backoff3)
	}
	// Past the cooldown: may proceed.
	*clk = clk.Add(600 * time.Millisecond)
	if w := thr.usernameWait(user, *clk); w != 0 {
		t.Fatalf("after cooldown wait=%v, want 0", w)
	}
}

func TestLockoutCrossingReportedOnce(t *testing.T) {
	thr, clk := newTestThrottle()
	const user = "andrei"

	// Failures 1..19 must not report a lock crossing.
	for i := 1; i < lockThreshold; i++ {
		if locked := thr.RecordFailure(user); locked {
			t.Fatalf("RecordFailure #%d reported lock prematurely", i)
		}
	}
	// The 20th crosses, exactly once.
	if locked := thr.RecordFailure(user); !locked {
		t.Fatal("RecordFailure #20 did not report the lock crossing")
	}
	// The 21st (still locked) must not re-report a crossing — keeps the audit
	// to one login.lockout per lock.
	if locked := thr.RecordFailure(user); locked {
		t.Fatal("RecordFailure #21 re-reported the lock crossing")
	}

	// While locked, even a would-be-valid attempt is rejected with ~15min wait.
	ok, d := thr.AllowAttempt(user, "10.0.0.1")
	if ok {
		t.Fatal("locked account was allowed an attempt")
	}
	if d < 14*time.Minute || d > lockDuration {
		t.Fatalf("lock wait=%v, want ~%v", d, lockDuration)
	}
	// After the lock elapses, attempts are allowed again.
	*clk = clk.Add(lockDuration + time.Second)
	if ok, _ := thr.AllowAttempt(user, "10.0.0.1"); !ok {
		t.Fatal("attempt still blocked after the lock expired")
	}
}

func TestSuccessResetsBackoff(t *testing.T) {
	thr, clk := newTestThrottle()
	const user = "andrei"
	for i := 0; i < 5; i++ {
		thr.RecordFailure(user)
	}
	if w := thr.usernameWait(user, *clk); w != backoff5 {
		t.Fatalf("after 5 fails: wait=%v, want %v", w, backoff5)
	}
	thr.RecordSuccess(user)
	if w := thr.usernameWait(user, *clk); w != 0 {
		t.Fatalf("after success reset: wait=%v, want 0", w)
	}
	// A single fresh failure restarts from tier 0 (no delay yet).
	thr.RecordFailure(user)
	if w := thr.usernameWait(user, *clk); w != 0 {
		t.Fatalf("one fail post-reset: wait=%v, want 0", w)
	}
}

func TestPerIPTokenBucket(t *testing.T) {
	thr, clk := newTestThrottle()
	const ip = "10.0.0.5"

	// Bucket starts full: 10 attempts in the same instant are allowed. Distinct
	// usernames keep the per-username gate out of the way.
	for i := 0; i < 10; i++ {
		if ok, d := thr.AllowAttempt("u"+strconv.Itoa(i), ip); !ok {
			t.Fatalf("attempt %d from a fresh IP was throttled (wait %v)", i, d)
		}
	}
	// 11th in the same minute is throttled, with a sub-minute wait.
	ok, d := thr.AllowAttempt("u10", ip)
	if ok {
		t.Fatal("11th attempt within a minute was allowed")
	}
	if d <= 0 || d > time.Minute {
		t.Fatalf("ip throttle wait=%v, want in (0,1m]", d)
	}
	// Throttled attempts don't spend tokens, so ~6s (one refill at 10/min) lets
	// the next attempt through.
	*clk = clk.Add(7 * time.Second)
	if ok, _ := thr.AllowAttempt("u11", ip); !ok {
		t.Fatal("attempt after a token refilled was still throttled")
	}
}

// A throttled per-username attempt is a soft reject (caller retries later); it
// must not, by itself, advance the failure counter — only real PAM failures do
// (via RecordFailure). This guards the "throttle gates before PAM" contract.
func TestAllowAttemptDoesNotCount(t *testing.T) {
	thr, clk := newTestThrottle()
	const user = "andrei"
	for i := 0; i < 3; i++ {
		thr.RecordFailure(user)
	}
	// Hammer AllowAttempt while in the 1s cooldown; none of these should change
	// the tier.
	for i := 0; i < 5; i++ {
		if ok, _ := thr.AllowAttempt(user, "10.0.0.7"); ok {
			t.Fatal("attempt allowed during the cooldown")
		}
	}
	*clk = clk.Add(backoff3 + time.Second)
	// Still tier-1 (3 fails): cooldown elapsed, so allowed — not escalated to 10s.
	if w := thr.usernameWait(user, *clk); w != 0 {
		t.Fatalf("wait=%v after cooldown; throttled probes wrongly escalated the tier", w)
	}
}
