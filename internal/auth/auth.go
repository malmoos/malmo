// Package auth owns dashboard sessions: opaque random tokens issued by the
// brain, validated on every authenticated request, and revoked on logout.
// The credential itself never lives here — PAM (via host-agent) is the
// source of truth for passwords (AUTH.md # Identity primitive). This package
// owns the session lifecycle and the cookie shape; it does not implement the
// HTTP middleware (that lives in internal/api so it can use huma's types).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/molmaos/molma/internal/store"
)

// CookieName is the dashboard's session cookie. AUTH.md # Sessions.
const CookieName = "molma_session"

// tokenBytes is the entropy of one session token. 32 bytes = 256 bits;
// base64url-encoded that's 43 chars. Server-side validated, so length here
// is for collision resistance, not guess resistance.
const tokenBytes = 32

// Session lifetime constants (AUTH.md # Lifetime).
const (
	// SessionIdleWindow is the rolling idle timeout. A session that hasn't
	// been seen within this window is treated as expired.
	SessionIdleWindow = 30 * 24 * time.Hour

	// SessionHardCap is the absolute maximum lifetime of a session. Even a
	// constantly-active session is invalidated after this duration.
	SessionHardCap = 90 * 24 * time.Hour

	// ElevationWindow is the duration a session stays elevated after a
	// successful POST /api/v1/auth/elevate (USERS_AND_GROUPS.md # Elevation).
	ElevationWindow = 5 * time.Minute
)

// ErrInvalidSession is returned when a token can't be resolved to a live
// user. The HTTP layer maps it to 401.
var ErrInvalidSession = errors.New("invalid session")

// SessionStore is the persistence surface auth needs. Declared here, not in
// internal/store, so auth doesn't depend on the store's full API
// (consumer-side interface rule from CLAUDE.md).
type SessionStore interface {
	CreateSession(store.Session) error
	GetSession(token string) (store.Session, error)
	TouchSession(token string, at time.Time) error
	DeleteSession(token string) error
	SetElevatedUntil(token string, until time.Time) error
	GetUser(id string) (store.User, error)
}

// Identity is the authenticated principal attached to a request context by
// the API's auth middleware. The raw session token is not part of Identity —
// handlers shouldn't be able to forward it.
type Identity struct {
	User     store.User
	Session  store.Session
	elevated bool // computed by Validate at the Manager's clock; use IsElevated()
}

// IsAdmin is a convenience for role-gated handlers.
func (id Identity) IsAdmin() bool { return id.User.Role == store.RoleAdmin }

// IsElevated reports whether the session was in the 5-minute elevation window
// at the time Validate ran (USERS_AND_GROUPS.md # Elevation in the UI).
func (id Identity) IsElevated() bool { return id.elevated }

// Manager issues, validates, and revokes sessions. One per brain.
type Manager struct {
	store SessionStore
	// Clock returns "now". Injected for deterministic tests.
	Clock func() time.Time
	// SecureCookies sets the Secure attribute on issued cookies. True only
	// when the brain is serving HTTPS — Caddy fronts us in prod, but in dev
	// the brain listens on plain HTTP and Secure would make the browser drop
	// the cookie.
	SecureCookies bool
}

func NewManager(s SessionStore) *Manager {
	return &Manager{store: s, Clock: time.Now}
}

// Issue mints a new session for userID. ExpiresAt is set to now + SessionHardCap.
// Caller sets the resulting cookie on the response.
func (m *Manager) Issue(userID string) (store.Session, error) {
	token, err := newToken()
	if err != nil {
		return store.Session{}, err
	}
	now := m.Clock()
	sess := store.Session{
		Token:      token,
		UserID:     userID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(SessionHardCap),
	}
	if err := m.store.CreateSession(sess); err != nil {
		return store.Session{}, err
	}
	return sess, nil
}

// Validate resolves a token to an Identity and bumps last_seen_at. Unknown
// token → ErrInvalidSession. Expired sessions (idle window or hard cap) are
// deleted and return ErrInvalidSession. A session whose user has been removed
// is also ErrInvalidSession (defensive — the FK cascade should have killed
// the session row already, but we don't want to leak a deleted user back to a
// handler under any circumstance).
func (m *Manager) Validate(token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrInvalidSession
	}
	sess, err := m.store.GetSession(token)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrInvalidSession
	}
	if err != nil {
		return Identity{}, err
	}

	// Enforce session lifetime (AUTH.md # Lifetime).
	now := m.Clock()
	idleExpiry := sess.LastSeenAt.Add(SessionIdleWindow)
	hardExpiry := sess.ExpiresAt
	if now.After(idleExpiry) || (!hardExpiry.IsZero() && now.After(hardExpiry)) {
		// Session is expired — delete it and reject.
		_ = m.store.DeleteSession(token)
		return Identity{}, ErrInvalidSession
	}

	user, err := m.store.GetUser(sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrInvalidSession
	}
	if err != nil {
		return Identity{}, err
	}
	if err := m.store.TouchSession(token, now); err != nil {
		return Identity{}, err
	}
	sess.LastSeenAt = now
	elevated := !sess.ElevatedUntil.IsZero() && now.Before(sess.ElevatedUntil)
	return Identity{User: user, Session: sess, elevated: elevated}, nil
}

// Elevate marks the session elevated for ElevationWindow. Used by the elevate
// handler; the updated ElevatedUntil is reflected on the next Validate call.
func (m *Manager) Elevate(token string) error {
	until := m.Clock().Add(ElevationWindow)
	return m.store.SetElevatedUntil(token, until)
}

// Revoke deletes the session. Idempotent: unknown token returns nil.
func (m *Manager) Revoke(token string) error {
	return m.store.DeleteSession(token)
}

// Cookie returns the http.Cookie that carries `token` to the browser.
// HttpOnly + SameSite=Lax + Path=/. Secure follows m.SecureCookies.
func (m *Manager) Cookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie returns a cookie that tells the browser to drop the session
// cookie. Used on logout.
func (m *Manager) ClearCookie() *http.Cookie {
	c := m.Cookie("")
	c.MaxAge = -1
	return c
}

// TokenFromRequest extracts the session token from the request's cookie. ""
// when no cookie is set — callers treat that as "no session", not an error.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

type ctxKey struct{}

// WithIdentity attaches id to ctx. The auth middleware does this once per
// authenticated request; handlers read it with FromContext.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the identity attached by WithIdentity, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
