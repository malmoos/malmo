package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
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
	"/api/v1/recover":    true,
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
		OperationID: "recover", Method: "POST", Path: "/api/v1/recover",
		Summary: "Redeem a recovery code to reset password and rotate the code (public)",
	}, s.recover)

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

	huma.Register(api, huma.Operation{
		OperationID: "elevate", Method: "POST", Path: "/api/v1/auth/elevate",
		Summary: "Re-verify password and enter the 5-minute elevation window (auth required)",
	}, s.elevate)
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
	if err := validateUsername(username); err != nil {
		return nil, err
	}

	recoveryCode, recoveryHash, err := newRecoveryCode()
	if err != nil {
		return nil, huma.Error500InternalServerError("recovery code", err)
	}

	u := store.User{
		ID: newID(), Username: username, Role: store.RoleAdmin,
		RecoveryHash: recoveryHash, CreatedAt: time.Now(),
	}
	meta := map[string]any{"username": username}
	if err := s.store.CreateFirstAdmin(u); err != nil {
		s.auditor.Record(ctx, audit.ActionSetupFailure, audit.Target{Kind: "user"}, meta, false)
		if errors.Is(err, store.ErrConflict) {
			return nil, huma.Error409Conflict("setup has already completed; use /api/v1/login")
		}
		return nil, huma.Error500InternalServerError("create admin", err)
	}

	if err := s.host.SetPassword(ctx, username, password); err != nil {
		// Roll back so /v1/setup can be retried instead of being permanently
		// wedged by a half-completed bootstrap. Best-effort host cleanup
		// closes the sliver where useradd succeeded but chpasswd failed —
		// without it the next /setup attempt would race a real Linux account
		// (`docs/progress/0017-host-agent-delete-user.md`). Idempotent on the
		// host side, so safe even when nothing was created.
		if delErr := s.host.DeleteUser(ctx, username); delErr != nil {
			slog.Error("rollback host delete-user failed", "username", username, "err", delErr)
		}
		if delErr := s.store.DeleteUser(u.ID); delErr != nil {
			// Silent failure here wedges the box permanently: the next
			// /setup attempt hits CreateFirstAdmin's 409 ("setup has
			// already completed") with no log trace. Mirror createUser.
			slog.Error("rollback setup user failed", "user_id", u.ID, "err", delErr)
		}
		s.auditor.Record(ctx, audit.ActionSetupFailure, audit.Target{Kind: "user"}, meta, false)
		return nil, huma.Error502BadGateway("host-agent set-password failed", err)
	}

	// Add the first admin to the sudo group (USERS_AND_GROUPS.md:32 — "The
	// first admin is added to `sudo` at account creation"). With the fake
	// host-agent this is a no-op record; with host-agent-real it shells out
	// to `gpasswd -a <user> sudo`. On failure, roll the brain row back so
	// /v1/setup stays retryable.
	if err := s.host.SetRole(ctx, username, store.RoleAdmin); err != nil {
		// Best-effort host cleanup: by this point UpsertPassword has created
		// the real Linux account, so a bare store rollback would leave an
		// orphan with a usable /etc/shadow entry and PAM would still
		// authenticate it (`docs/progress/0017-host-agent-delete-user.md`).
		if delErr := s.host.DeleteUser(ctx, username); delErr != nil {
			slog.Error("rollback host delete-user failed", "username", username, "err", delErr)
		}
		if delErr := s.store.DeleteUser(u.ID); delErr != nil {
			slog.Error("rollback setup user failed", "user_id", u.ID, "err", delErr)
		}
		s.auditor.Record(ctx, audit.ActionSetupFailure, audit.Target{Kind: "user"}, meta, false)
		return nil, huma.Error502BadGateway("host-agent set-role failed", err)
	}

	sess, err := s.auth.Issue(u.ID)
	if err != nil {
		s.auditor.Record(ctx, audit.ActionSetupFailure, audit.Target{Kind: "user", ID: u.ID}, meta, false)
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

// recover redeems a recovery code to reset the admin's password and rotate the
// code. It is public — the recovery code IS the credential. No session is
// issued; the user must log in normally after recovery.
//
// Ordering rationale (AUTH.md # Using the recovery code, CLAUDE.md "brain
// commits first"):
//
//  1. Look up the user + verify the supplied code (constant-time bcrypt even on
//     unknown username to avoid leaking which usernames have recovery codes).
//  2. Generate a new code+hash.
//  3. Store the new hash in the brain (brain commits first — it is the durable
//     side; host-agent is reconstructible).
//  4. Call host-agent SetPassword. If it fails, restore the old hash so the
//     next attempt can still use the same recovery code. Also revoke all
//     existing sessions so a stolen session doesn't outlive the password reset.
//  5. Return the new recovery code (shown once, like /setup).
//
// We do NOT restore old hash on session-revocation failure — DeleteSessionsForUser
// is a best-effort call; stale sessions age out naturally.
func (s *Server) recover(ctx context.Context, in *struct {
	Body struct {
		Username     string `json:"username"`
		RecoveryCode string `json:"recovery_code"`
		NewPassword  string `json:"new_password"`
	}
}) (*struct {
	Body struct {
		NewRecoveryCode string `json:"new_recovery_code"`
	}
}, error) {
	username := strings.TrimSpace(in.Body.Username)
	suppliedCode := in.Body.RecoveryCode
	newPassword := in.Body.NewPassword

	if username == "" || suppliedCode == "" || newPassword == "" {
		return nil, huma.Error422UnprocessableEntity("username, recovery_code, and new_password are required")
	}

	// Look up the user. We always run bcrypt.CompareHashAndPassword regardless
	// of whether the lookup succeeded, to avoid leaking which usernames exist
	// (mirrors the login handler's constant-time-ish approach).
	u, lookupErr := s.store.GetUserByUsername(username)

	// Compute bcrypt comparison target — use a dummy hash on unknown user so
	// the cost is paid either way.
	dummyHash := "$2a$10$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashToCheck := dummyHash
	if lookupErr == nil {
		hashToCheck = u.RecoveryHash
	}
	codeErr := bcrypt.CompareHashAndPassword([]byte(hashToCheck), []byte(suppliedCode))

	if lookupErr != nil || codeErr != nil {
		s.auditor.Record(ctx, audit.ActionRecoverFailure,
			audit.Target{},
			map[string]any{"username": username},
			false)
		return nil, huma.Error401Unauthorized("invalid recovery code")
	}

	// Generate fresh code + hash before touching any state.
	newCode, newHash, err := newRecoveryCode()
	if err != nil {
		return nil, huma.Error500InternalServerError("generate recovery code", err)
	}

	// Brain commits first — store the new hash so the brain is always the
	// durable record; host-agent state is reconstructible.
	if err := s.store.UpdateRecoveryHash(u.ID, newHash); err != nil {
		return nil, huma.Error500InternalServerError("update recovery hash", err)
	}

	// Call host-agent to reset the OS password. On failure, restore the old
	// hash so the user can retry with the same recovery code.
	if err := s.host.SetPassword(ctx, u.Username, newPassword); err != nil {
		// Best-effort rollback: restore old hash so the code is still valid.
		_ = s.store.UpdateRecoveryHash(u.ID, u.RecoveryHash)
		s.auditor.Record(ctx, audit.ActionRecoverFailure,
			audit.Target{Kind: "user", ID: u.ID},
			map[string]any{"step": "set_password"},
			false)
		return nil, huma.Error502BadGateway("host-agent set-password failed", err)
	}

	// Revoke all existing sessions — password has changed, stale sessions must
	// not outlive the reset (AUTH.md # Invalidation).
	_ = s.store.DeleteSessionsForUser(u.ID)

	s.auditor.Record(ctx, audit.ActionRecoverSuccess,
		audit.Target{Kind: "user", ID: u.ID},
		nil,
		true)

	out := &struct {
		Body struct {
			NewRecoveryCode string `json:"new_recovery_code"`
		}
	}{}
	// AUTH.md # Recovery: new code is shown exactly once. Brain stores only the
	// hash; this is the user's single chance to record it.
	out.Body.NewRecoveryCode = newCode
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

// elevate re-verifies the caller's password and marks the session elevated for
// ElevationWindow. Body: {password}. Returns {elevated_until: <unix>} on
// success. Audits both success and failure per the elevation-class rule
// (CLAUDE.md # Go code discipline).
func (s *Server) elevate(ctx context.Context, in *struct {
	Body struct {
		Password string `json:"password"`
	}
}) (*struct {
	Body struct {
		ElevatedUntil int64 `json:"elevated_until"`
	}
}, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	if in.Body.Password == "" {
		return nil, huma.Error422UnprocessableEntity("password is required")
	}

	valid, err := s.host.VerifyPassword(ctx, id.User.Username, in.Body.Password)
	if err != nil {
		return nil, huma.Error502BadGateway("host-agent verify failed", err)
	}
	if !valid {
		s.auditor.Record(ctx, audit.ActionElevateFailure,
			audit.Target{Kind: "user", ID: id.User.ID}, nil, false)
		return nil, huma.Error401Unauthorized("invalid password")
	}

	if err := s.auth.Elevate(id.Session.Token); err != nil {
		return nil, huma.Error500InternalServerError("elevate failed", err)
	}

	until := s.auth.Clock().Add(auth.ElevationWindow)
	s.auditor.Record(ctx, audit.ActionElevateSuccess,
		audit.Target{Kind: "user", ID: id.User.ID}, nil, true)

	out := &struct {
		Body struct {
			ElevatedUntil int64 `json:"elevated_until"`
		}
	}{}
	out.Body.ElevatedUntil = until.Unix()
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

// requireElevated returns 403 with title "elevation_required" when the session
// is not in the 5-minute elevation window (USERS_AND_GROUPS.md # Elevation in
// the UI). Call AFTER requireAdmin so members see admin_required, not this.
func requireElevated(ctx context.Context) error {
	id, ok := auth.FromContext(ctx)
	if !ok || !id.IsElevated() {
		return huma.NewError(http.StatusForbidden, "elevation_required")
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
