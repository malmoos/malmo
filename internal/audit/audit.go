// Package audit provides the single write path for the append-only audit log
// (LOGGING.md # Write path). One function: Record. On INSERT failure it logs
// and returns — callers never see the error.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/malmoos/malmo/internal/auth"
	"github.com/malmoos/malmo/internal/store"
)

// v1 action vocabulary (LOGGING.md # Write path).
const (
	ActionSetupComplete   = "setup.complete"
	ActionSetupFailure    = "setup.failure"
	ActionLoginSuccess    = "login.success"
	ActionLoginFailure    = "login.failure"
	ActionLoginLockout    = "login.lockout"
	ActionLogout          = "logout"
	ActionAppInstall      = "app.install"
	ActionAppUninstall    = "app.uninstall"
	ActionAppCustomCreate = "app.custom.create"

	// User management actions (USERS_AND_GROUPS.md).
	ActionUserCreate         = "user.create"
	ActionUserRoleChange     = "user.role.change"
	ActionUserDelete         = "user.delete"
	ActionUserPasswordReset  = "user.password.reset"
	ActionUserPasswordChange = "user.password.change"

	// Recovery-code redemption (AUTH.md # Using the recovery code).
	ActionRecoverSuccess = "recover.success"
	ActionRecoverFailure = "recover.failure"

	// Elevation window (USERS_AND_GROUPS.md # Elevation in the UI).
	ActionElevateSuccess = "auth.elevate.success"
	ActionElevateFailure = "auth.elevate.failure"

	// Health issue transitions (HEALTH.md # Persistence, LOGGING.md).
	ActionHealthIssueRaised  = "health.issue.raised"
	ActionHealthIssueCleared = "health.issue.cleared"

	// Outgoing-mail providers (SERVICE_PROVISIONING.md # BYO outgoing mail).
	// CRUD is elevation-class; test sends real mail through the credential, so
	// it audits too. Rebind is the per-app binding change.
	ActionMailProviderCreate = "mail.provider.create"
	ActionMailProviderUpdate = "mail.provider.update"
	ActionMailProviderDelete = "mail.provider.delete"
	ActionMailProviderTest   = "mail.provider.test"
	ActionAppMailRebind      = "app.mail.rebind"
)

// Target describes the object the action acts on. Both fields are optional.
type Target struct {
	Kind string // "app" | "user" | …
	ID   string // slug, user_id, etc.
}

// EventStore is the persistence surface audit needs. Declared here so the
// audit package doesn't depend on store's full API (consumer-side interface,
// CLAUDE.md).
type EventStore interface {
	InsertAuditEvent(store.AuditEvent) error
}

// Recorder writes audit events. Construct once via New and inject into
// handlers that need to emit audit records.
type Recorder struct {
	store EventStore
}

// New returns a Recorder backed by the given store.
func New(s EventStore) *Recorder {
	return &Recorder{store: s}
}

// Record writes one audit event derived from ctx (actor identity + client IP)
// and the supplied arguments. If the INSERT fails, it logs at Error level and
// returns — callers must not inspect or propagate this failure.
func (r *Recorder) Record(ctx context.Context, action string, target Target, metadata map[string]any, success bool) {
	id, hasIdentity := auth.FromContext(ctx)
	ip, _ := ClientIPFromContext(ctx)

	evt := store.AuditEvent{
		TS:         time.Now().UnixMilli(),
		Action:     action,
		TargetKind: target.Kind,
		TargetID:   target.ID,
		SourceIP:   ip,
		Success:    success,
	}

	if hasIdentity {
		evt.ActorUserID = id.User.ID
		evt.ActorRole = id.User.Role
	} else {
		evt.ActorRole = "system"
	}

	if len(metadata) > 0 {
		b, err := json.Marshal(metadata)
		if err != nil {
			slog.Warn("audit metadata marshal failed", "action", action, "err", err)
		} else {
			evt.Metadata = string(b)
		}
	}

	if err := r.store.InsertAuditEvent(evt); err != nil {
		slog.Error("audit insert failed",
			"action", action,
			"actor_user_id", evt.ActorUserID,
			"target_kind", target.Kind,
			"target_id", target.ID,
			"err", err)
	}
}

type ipKey struct{}

// WithClientIP attaches the client IP to ctx. Called by authMiddleware so all
// downstream handlers (and audit.Record) can read it without knowing about HTTP.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipKey{}, ip)
}

// ClientIPFromContext returns the client IP attached by WithClientIP.
func ClientIPFromContext(ctx context.Context) (string, bool) {
	ip, ok := ctx.Value(ipKey{}).(string)
	return ip, ok && ip != ""
}
