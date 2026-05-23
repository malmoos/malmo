package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/malmo/malmo/internal/audit"
	"github.com/malmo/malmo/internal/auth"
	"github.com/malmo/malmo/internal/store"
)

// publicPaths lists routes the auth middleware lets through without a
// session. The first-run wizard, login, and the bootstrap probe — and the
// OpenAPI / docs surface, which is useful during development and harmless
// (it's just the schema). Everything else requires a valid session.
var publicPaths = map[string]bool{
	"/api/v1/setup":      true,
	"/api/v1/login":      true,
	"/api/v1/auth/state": true,
	// huma exposes these by default; leave them public so curl/devtools work.
	"/openapi.json": true,
	"/openapi.yaml": true,
	"/docs":         true,
}

// authMiddleware rejects unauthenticated requests with 401 except for the
// public allowlist above. On success, it attaches the resolved Identity and
// client IP to the request context.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := audit.WithClientIP(r.Context(), clientIP(r))
		if isPublic(r) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		token := auth.TokenFromRequest(r)
		id, err := s.auth.Validate(token)
		if err != nil {
			writeUnauthenticated(w)
			return
		}
		ctx = auth.WithIdentity(ctx, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// clientIP extracts the request's originating IP. X-Forwarded-For first hop
// takes precedence (set by Caddy in production); falls back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be "client, proxy1, proxy2"; take the first token.
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isPublic(r *http.Request) bool {
	if publicPaths[r.URL.Path] {
		return true
	}
	// huma serves the docs HTML at /docs and asset paths under it.
	return strings.HasPrefix(r.URL.Path, "/docs/")
}

func writeUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"status":401,"title":"Unauthenticated"}`))
}

// --- DTOs ----------------------------------------------------------------

type UserDTO struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"`
}

func userDTO(u store.User) UserDTO {
	return UserDTO{
		ID: u.ID, Username: u.Username, Role: u.Role,
		CreatedAt: u.CreatedAt.Unix(),
	}
}

// --- registration --------------------------------------------------------

func (s *Server) registerAuth(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "auth-state", Method: "GET", Path: "/api/v1/auth/state",
		Summary: "Probe whether the box has any users yet (public)",
	}, s.authState)

	huma.Register(api, huma.Operation{
		OperationID: "setup", Method: "POST", Path: "/api/v1/setup",
		Summary: "Bootstrap the first admin (public; allowed only on empty box)",
	}, s.setup)

	huma.Register(api, huma.Operation{
		OperationID: "login", Method: "POST", Path: "/api/v1/login",
		Summary: "Authenticate against PAM via host-agent; mint a session",
	}, s.login)

	huma.Register(api, huma.Operation{
		OperationID: "logout", Method: "POST", Path: "/api/v1/logout",
		Summary: "Revoke the current session",
	}, s.logout)

	huma.Register(api, huma.Operation{
		OperationID: "me", Method: "GET", Path: "/api/v1/me",
		Summary: "Identity of the current session",
	}, s.me)

	huma.Register(api, huma.Operation{
		OperationID: "list-audit", Method: "GET", Path: "/api/v1/audit",
		Summary: "Paginated audit log; admins see all rows, members see their own",
	}, s.listAudit)
}

// --- handlers ------------------------------------------------------------

func (s *Server) authState(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		HasUsers bool `json:"has_users"`
	}
}, error) {
	has, err := s.store.HasAnyUser()
	if err != nil {
		return nil, huma.Error500InternalServerError("store read failed", err)
	}
	out := &struct {
		Body struct {
			HasUsers bool `json:"has_users"`
		}
	}{}
	out.Body.HasUsers = has
	return out, nil
}

// setup creates the first admin. Two ordering invariants matter here:
// (1) brain commits first, so the empty-table guard fences any concurrent
// caller atomically (store.CreateFirstAdmin uses a transaction); (2) the
// recovery code is generated *before* the user row is written so the row
// carries its hash from the start. If host-agent SetPassword fails after the
// brain has committed, we roll the brain row back so /v1/setup can be
// retried — the brain is the durable side, host-agent is reconstructible.
func (s *Server) setup(ctx context.Context, in *struct {
	Body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
}) (*struct {
	SetCookie string `header:"Set-Cookie"`
	Body      struct {
		User         UserDTO `json:"user"`
		RecoveryCode string  `json:"recovery_code"`
	}
}, error) {
	username := strings.TrimSpace(in.Body.Username)
	password := in.Body.Password
	if username == "" || password == "" {
		return nil, huma.Error422UnprocessableEntity("username and password are required")
	}

	recoveryCode, recoveryHash, err := newRecoveryCode()
	if err != nil {
		return nil, huma.Error500InternalServerError("recovery code", err)
	}

	u := store.User{
		ID: newID(), Username: username, Role: store.RoleAdmin,
		RecoveryHash: recoveryHash, CreatedAt: time.Now(),
	}
	if err := s.store.CreateFirstAdmin(u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, huma.Error409Conflict("setup has already completed; use /api/v1/login")
		}
		return nil, huma.Error500InternalServerError("create admin", err)
	}

	if err := s.host.SetPassword(ctx, username, password); err != nil {
		// Roll back so /v1/setup can be retried instead of being permanently
		// wedged by a half-completed bootstrap.
		_ = s.store.DeleteUser(u.ID)
		return nil, huma.Error502BadGateway("host-agent set-password failed", err)
	}

	sess, err := s.auth.Issue(u.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("issue session", err)
	}

	out := &struct {
		SetCookie string `header:"Set-Cookie"`
		Body      struct {
			User         UserDTO `json:"user"`
			RecoveryCode string  `json:"recovery_code"`
		}
	}{}
	out.SetCookie = s.auth.Cookie(sess.Token).String()
	out.Body.User = userDTO(u)
	// AUTH.md # Recovery: recovery code is shown exactly once. The brain
	// stores only the hash; this is the user's single chance to record it.
	out.Body.RecoveryCode = recoveryCode
	s.auditor.Record(ctx, audit.ActionSetupComplete, audit.Target{Kind: "user", ID: u.ID}, nil, true)
	return out, nil
}

func (s *Server) login(ctx context.Context, in *struct {
	Body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
}) (*struct {
	SetCookie string `header:"Set-Cookie"`
	Body      struct {
		User UserDTO `json:"user"`
	}
}, error) {
	username := strings.TrimSpace(in.Body.Username)
	password := in.Body.Password
	if username == "" || password == "" {
		return nil, huma.Error401Unauthorized("invalid credentials")
	}

	// Look up the user first; verify password regardless of the result so
	// timing doesn't leak whether the username exists.
	u, lookupErr := s.store.GetUserByUsername(username)
	valid, vErr := s.host.VerifyPassword(ctx, username, password)
	if vErr != nil {
		return nil, huma.Error502BadGateway("host-agent verify failed", vErr)
	}
	if lookupErr != nil || !valid {
		s.auditor.Record(ctx, audit.ActionLoginFailure, audit.Target{}, map[string]any{"username": username}, false)
		return nil, huma.Error401Unauthorized("invalid credentials")
	}

	sess, err := s.auth.Issue(u.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("issue session", err)
	}

	// Attach identity to ctx so the audit row carries the actor.
	idCtx := auth.WithIdentity(ctx, auth.Identity{User: u, Session: sess})
	s.auditor.Record(idCtx, audit.ActionLoginSuccess, audit.Target{Kind: "user", ID: u.ID}, nil, true)

	out := &struct {
		SetCookie string `header:"Set-Cookie"`
		Body      struct {
			User UserDTO `json:"user"`
		}
	}{}
	out.SetCookie = s.auth.Cookie(sess.Token).String()
	out.Body.User = userDTO(u)
	return out, nil
}

// logout revokes the cookie-bound session if any. Idempotent: hitting it
// without a session, or twice, both return 200 with a Clear-Cookie header.
func (s *Server) logout(ctx context.Context, _ *struct{}) (*struct {
	SetCookie string `header:"Set-Cookie"`
}, error) {
	if id, ok := auth.FromContext(ctx); ok {
		_ = s.auth.Revoke(id.Session.Token)
		s.auditor.Record(ctx, audit.ActionLogout, audit.Target{Kind: "user", ID: id.User.ID}, nil, true)
	}
	out := &struct {
		SetCookie string `header:"Set-Cookie"`
	}{}
	out.SetCookie = s.auth.ClearCookie().String()
	return out, nil
}

func (s *Server) me(ctx context.Context, _ *struct{}) (*struct{ Body UserDTO }, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		// Defensive — middleware should have already 401'd.
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	return &struct{ Body UserDTO }{Body: userDTO(id.User)}, nil
}

// AuditEventDTO is the wire representation of one audit_events row.
type AuditEventDTO struct {
	ID          int64  `json:"id"`
	TS          int64  `json:"ts"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	ActorRole   string `json:"actor_role"`
	Action      string `json:"action"`
	TargetKind  string `json:"target_kind,omitempty"`
	TargetID    string `json:"target_id,omitempty"`
	SourceIP    string `json:"source_ip,omitempty"`
	Success     bool   `json:"success"`
	Metadata    string `json:"metadata,omitempty"`
}

func auditEventDTO(e store.AuditEvent) AuditEventDTO {
	return AuditEventDTO{
		ID: e.ID, TS: e.TS,
		ActorUserID: e.ActorUserID, ActorRole: e.ActorRole,
		Action: e.Action, TargetKind: e.TargetKind, TargetID: e.TargetID,
		SourceIP: e.SourceIP, Success: e.Success, Metadata: e.Metadata,
	}
}

const maxAuditLimit = 200

func (s *Server) listAudit(ctx context.Context, in *struct {
	Limit   int   `query:"limit" default:"50"`
	AfterID int64 `query:"after_id"`
}) (*struct {
	Body struct {
		Events []AuditEventDTO `json:"events"`
	}
}, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > maxAuditLimit {
		limit = maxAuditLimit
	}

	f := store.AuditFilter{AfterID: in.AfterID, Limit: limit}
	// Members may only see their own events; admins see all.
	if !id.IsAdmin() {
		f.ActorUserID = id.User.ID
	}

	rows, err := s.store.ListAuditEvents(f)
	if err != nil {
		return nil, huma.Error500InternalServerError("audit query failed", err)
	}

	out := &struct {
		Body struct {
			Events []AuditEventDTO `json:"events"`
		}
	}{}
	out.Body.Events = []AuditEventDTO{}
	for _, e := range rows {
		out.Body.Events = append(out.Body.Events, auditEventDTO(e))
	}
	return out, nil
}

// requireAdmin returns 403 when the acting identity is missing or not an admin.
// Call as the first line of every admin-only handler.
func requireAdmin(ctx context.Context) error {
	id, ok := auth.FromContext(ctx)
	if !ok || !id.IsAdmin() {
		return huma.Error403Forbidden("admin role required")
	}
	return nil
}

// --- helpers -------------------------------------------------------------

// newID returns a random opaque identifier for new user rows. We don't reuse
// the username because rename support is on the roadmap and the FK chain
// (sessions, future audit) is happier with stable IDs.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "u_" + hex.EncodeToString(b)
}

// newRecoveryCode returns a fresh recovery code (12 random bytes hex-encoded
// to 24 chars) and its bcrypt hash. AUTH.md # Recovery treats the code as a
// password equivalent — same hash family is sufficient.
func newRecoveryCode() (code, hash string, err error) {
	b := make([]byte, 12)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	code = hex.EncodeToString(b)
	h, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return code, string(h), nil
}
