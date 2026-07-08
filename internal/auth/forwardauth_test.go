package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Hosted forward-auth token + cookie (issue #305). These exercise the second,
// lower-privilege credential the box mints alongside the dashboard session: it
// resolves to the same session, but through a distinct column so it can never be
// replayed as a dashboard session, and validating it never bumps last_seen_at.

const testFADomain = "cindy-fox.malmo.network"

func TestIssueForwardAuthPersistsAndValidates(t *testing.T) {
	m, _, u, _ := fixture(t)
	m.ForwardAuthDomain = testFADomain
	sess, _ := m.Issue(u.ID)

	cookie, err := m.IssueForwardAuth(sess.Token)
	if err != nil {
		t.Fatalf("IssueForwardAuth: %v", err)
	}
	if cookie.Name != ForwardAuthCookieName {
		t.Fatalf("cookie name = %q, want %q", cookie.Name, ForwardAuthCookieName)
	}
	if cookie.Value == "" {
		t.Fatal("empty forward-auth token")
	}
	if cookie.Value == sess.Token {
		t.Fatal("forward-auth token must differ from the session token")
	}
	if cookie.Domain != testFADomain {
		t.Fatalf("cookie Domain = %q, want %q", cookie.Domain, testFADomain)
	}

	id, err := m.ValidateForwardAuth(cookie.Value)
	if err != nil {
		t.Fatalf("ValidateForwardAuth: %v", err)
	}
	if id.User.ID != u.ID {
		t.Fatalf("identity user = %+v, want %s", id.User, u.ID)
	}
}

func TestValidateForwardAuthRejectsUnknownAndEmpty(t *testing.T) {
	m, _, _, _ := fixture(t)
	if _, err := m.ValidateForwardAuth("never-minted"); err != ErrInvalidSession {
		t.Fatalf("unknown token = %v, want ErrInvalidSession", err)
	}
	if _, err := m.ValidateForwardAuth(""); err != ErrInvalidSession {
		t.Fatalf("empty token = %v, want ErrInvalidSession", err)
	}
}

// The two credentials must not be interchangeable: a forward-auth token replayed
// as a dashboard session must fail, and vice versa. This is the load-bearing
// "cannot be upgraded to an admin session" property (#305).
func TestForwardAuthAndSessionTokensAreNotInterchangeable(t *testing.T) {
	m, _, u, _ := fixture(t)
	m.ForwardAuthDomain = testFADomain
	sess, _ := m.Issue(u.ID)
	cookie, err := m.IssueForwardAuth(sess.Token)
	if err != nil {
		t.Fatalf("IssueForwardAuth: %v", err)
	}

	// Forward-auth token is not a dashboard session token.
	if _, err := m.Validate(cookie.Value); err != ErrInvalidSession {
		t.Fatalf("Validate(fa token) = %v, want ErrInvalidSession", err)
	}
	// Session token is not a forward-auth token.
	if _, err := m.ValidateForwardAuth(sess.Token); err != ErrInvalidSession {
		t.Fatalf("ValidateForwardAuth(session token) = %v, want ErrInvalidSession", err)
	}
}

// The forward-auth verify path is the box Caddy's per-request hook, so it must
// not extend the session's rolling idle window — unlike the dashboard Validate.
func TestValidateForwardAuthDoesNotTouchLastSeen(t *testing.T) {
	m, s, u, _ := fixture(t)
	m.ForwardAuthDomain = testFADomain
	sess, _ := m.Issue(u.ID)
	cookie, _ := m.IssueForwardAuth(sess.Token)

	before, _ := s.GetSession(sess.Token)
	// Advance the clock so a bump would be observable, then validate via FA.
	later := sess.CreatedAt.Add(3 * time.Hour)
	m.Clock = func() time.Time { return later }
	if _, err := m.ValidateForwardAuth(cookie.Value); err != nil {
		t.Fatalf("ValidateForwardAuth: %v", err)
	}
	after, _ := s.GetSession(sess.Token)
	if !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Fatalf("last_seen_at moved on forward-auth validate: %v -> %v", before.LastSeenAt, after.LastSeenAt)
	}

	// The dashboard path, by contrast, does bump it.
	if _, err := m.Validate(sess.Token); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	touched, _ := s.GetSession(sess.Token)
	if !touched.LastSeenAt.Equal(later) {
		t.Fatalf("Validate did not bump last_seen_at: got %v, want %v", touched.LastSeenAt, later)
	}
}

// Revoking the session must kill the forward-auth token with it (the token lives
// on the session row, so logout / expiry drop it).
func TestForwardAuthDiesWithSession(t *testing.T) {
	m, _, u, _ := fixture(t)
	m.ForwardAuthDomain = testFADomain
	sess, _ := m.Issue(u.ID)
	cookie, _ := m.IssueForwardAuth(sess.Token)

	if err := m.Revoke(sess.Token); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := m.ValidateForwardAuth(cookie.Value); err != ErrInvalidSession {
		t.Fatalf("ValidateForwardAuth after revoke = %v, want ErrInvalidSession", err)
	}
}

func TestValidateForwardAuthRejectsExpired(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance time.Duration
	}{
		{"idle", SessionIdleWindow + time.Second},
		{"hard-cap", SessionHardCap + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s, u, _ := fixture(t)
			m.ForwardAuthDomain = testFADomain
			sess, _ := m.Issue(u.ID)
			cookie, _ := m.IssueForwardAuth(sess.Token)

			m.Clock = func() time.Time { return sess.CreatedAt.Add(tc.advance) }
			if _, err := m.ValidateForwardAuth(cookie.Value); err != ErrInvalidSession {
				t.Fatalf("expired FA validate = %v, want ErrInvalidSession", err)
			}
			// Expiry deletes the row, so the session is gone too.
			if _, err := s.GetSession(sess.Token); err == nil {
				t.Fatal("expired session row not deleted by FA validate")
			}
		})
	}
}

func TestForwardAuthCookieAttributes(t *testing.T) {
	m, _, _, _ := fixture(t)
	m.ForwardAuthDomain = testFADomain

	c := m.ForwardAuthCookie("tok")
	if c.Name != ForwardAuthCookieName || c.Value != "tok" || c.Path != "/" ||
		c.Domain != testFADomain || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("forward-auth cookie attrs = %+v", c)
	}
	if c.Secure {
		t.Fatal("Secure must default to false in dev")
	}
	m.SecureCookies = true
	if !m.ForwardAuthCookie("x").Secure {
		t.Fatal("Secure not set when SecureCookies=true")
	}

	cleared := m.ClearForwardAuthCookie()
	if cleared.MaxAge >= 0 {
		t.Fatalf("ClearForwardAuthCookie MaxAge = %d; want negative", cleared.MaxAge)
	}
	if cleared.Domain != testFADomain {
		t.Fatalf("cleared cookie Domain = %q; must match issued cookie so the browser expires it", cleared.Domain)
	}
}

func TestForwardAuthTokenFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := ForwardAuthTokenFromRequest(r); got != "" {
		t.Fatalf("no-cookie request returned %q", got)
	}
	r.AddCookie(&http.Cookie{Name: ForwardAuthCookieName, Value: "fa"})
	if got := ForwardAuthTokenFromRequest(r); got != "fa" {
		t.Fatalf("ForwardAuthTokenFromRequest = %q", got)
	}
}
