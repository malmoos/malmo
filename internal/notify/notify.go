// Package notify is the routing + derivation layer for the dashboard
// notification center (NOTIFICATIONS.md). A notification is *derived* from an
// event that already exists elsewhere — this slice wires the first source:
// HEALTH issue raise/clear. The package does not invent a parallel taxonomy;
// it maps a bounded, code-registered allowlist of health issue IDs to a
// persisted, prunable notification.
//
// Scope of this slice (the write seam only): on a health-issue *transition*
// the Notifier emits one notification (coalesced by dedup_key) routed to
// admins; on clear it marks the notification resolved. The bell UI, the
// list/read/dismiss API, the SSE event kinds, per-recipient read state, the
// member transparency variant, and off-box transports are all deferred to
// later slices (see docs/progress/0025-health-notifications.md).
package notify

import (
	"log/slog"
	"time"

	"github.com/malmo/malmo/internal/health"
)

// Category groups notifications in the inbox (NOTIFICATIONS.md # The
// notification model). Only the categories this slice can produce are defined;
// updates / security / account / app land with their sources.
type Category string

const (
	CategoryStorage Category = "storage"
	CategorySystem  Category = "system"
)

// Severity reuses HEALTH's vocabulary plus info (NOTIFICATIONS.md # Severity).
// For health-derived notifications it is copied verbatim from the issue —
// severity is a property of the source event, never reassigned here.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Source kinds (NOTIFICATIONS.md # The notification model). Only health_issue
// is wired in this slice.
const SourceHealthIssue = "health_issue"

// Audience and variant (NOTIFICATIONS.md # Routing). Health-derived
// notifications are box-wide → admins, actionable variant. The member
// transparency variant is deferred.
const (
	AudienceAdmins    = "admins"
	AudienceUser      = "user"
	VariantActionable = "actionable"
)

// Notification is the typed record persisted in the brain's SQLite
// `notifications` table (NOTIFICATIONS.md # The notification model). It is
// mutable and prunable — distinct from the append-only audit log. The
// read_at / dismissed_at columns exist on the table for forward compatibility
// but are not modeled here: per-recipient read state is a later slice.
type Notification struct {
	ID          int64
	TS          int64 // unix epoch ms — most-recent occurrence
	Category    Category
	Severity    Severity
	SourceKind  string // SourceHealthIssue
	SourceID    string // the originating issue ID
	DedupKey    string // coalescing key — one active notification per key
	Audience    string // AudienceAdmins | AudienceUser
	UserID      string // set only when Audience == AudienceUser
	Variant     string // VariantActionable
	Summary     string
	Body        string
	ActionLabel string // "" → no action link
	ActionRoute string
	ResolvedAt  int64 // 0 = active; set when the underlying condition clears
}

// NotificationStore is the persistence surface notify needs. Declared here so
// the package doesn't depend on store's full API (consumer-side interface,
// CLAUDE.md). The store implements it; notify does not import store.
type NotificationStore interface {
	// RaiseNotification coalesces by dedup_key: if an active (non-dismissed)
	// notification with the same key exists it is refreshed (ts/severity/body
	// bumped, resolved_at cleared), otherwise a new row is inserted.
	RaiseNotification(Notification) error
	// ResolveNotification marks the active notification for dedupKey resolved.
	// No-op when no active row matches (idempotent).
	ResolveNotification(dedupKey string, at time.Time) error
}

// healthRule binds one health issue ID to how it notifies. This is the
// curated, code-registered allowlist (NOTIFICATIONS.md # The notification
// list) — bounded on purpose, the same discipline as HEALTH's issue taxonomy.
// An issue ID absent from this map never reaches the bell (e.g.
// health-report-malformed and store-write-failed are deliberately excluded).
type healthRule struct {
	category    Category
	actionLabel string // "" → no action link
	actionRoute string
}

// healthRules is the v1 allowlist for HEALTH-derived notifications. It covers
// the registered storage/state issue IDs that NOTIFICATIONS.md lists. Three
// have a live detector today (storageverify emits data-drive-missing,
// data-drive-wrong, canary-mismatch); data-drive-readonly and
// mergerfs-assembly-failed are pre-registered ahead of their detectors,
// mirroring HEALTH's own pre-registration pattern — harmless, and they notify
// the moment their detector lands. The rest of the spec's allowlist (disk-full,
// brain-db-corrupt, SMART warnings, update outcomes, security audit actions,
// app lifecycle) lands as those sources are implemented.
var healthRules = map[string]healthRule{
	// Storage — to Admin (NOTIFICATIONS.md # Storage).
	"data-drive-missing":  {category: CategoryStorage, actionLabel: "Open Storage", actionRoute: "/settings/storage"},
	"data-drive-wrong":    {category: CategoryStorage, actionLabel: "Open Storage", actionRoute: "/settings/storage"},
	"data-drive-readonly": {category: CategoryStorage, actionLabel: "Open Storage", actionRoute: "/settings/storage"},
	// System / state — to Admin (NOTIFICATIONS.md # System / state).
	"canary-mismatch":          {category: CategorySystem, actionLabel: "Open Storage", actionRoute: "/settings/storage"},
	"mergerfs-assembly-failed": {category: CategorySystem, actionLabel: "Open Storage", actionRoute: "/settings/storage"},
}

// Notifier derives notifications from events and writes them through the store.
// Construct once via New. Like audit.Recorder, it never propagates store
// errors — a failed notification is logged and swallowed, never blocking the
// triggering operation (NOTIFICATIONS.md: the bell is a floor, not a gate).
type Notifier struct {
	store NotificationStore
	now   func() time.Time
}

// New returns a Notifier backed by the given store.
func New(s NotificationStore) *Notifier {
	return &Notifier{store: s, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock swaps the time source — tests use this to assert TS without sleeping.
func (n *Notifier) SetClock(now func() time.Time) { n.now = now }

// HealthRaised emits a notification for a health issue that just transitioned
// to active. Issues not on the allowlist are ignored. Coalescing (one row per
// dedup_key) is the store's job; this method is only called on genuine
// raise transitions, so re-raise here means a real flap (cleared then raised
// again).
func (n *Notifier) HealthRaised(iss health.Issue) {
	rule, ok := healthRules[iss.ID]
	if !ok {
		return
	}
	notif := Notification{
		TS:          n.now().UnixMilli(),
		Category:    rule.category,
		Severity:    Severity(iss.Severity),
		SourceKind:  SourceHealthIssue,
		SourceID:    iss.ID,
		DedupKey:    healthDedupKey(iss.ID, iss.InstanceKey),
		Audience:    AudienceAdmins,
		Variant:     VariantActionable,
		Summary:     iss.Summary,
		Body:        iss.Details,
		ActionLabel: rule.actionLabel,
		ActionRoute: rule.actionRoute,
	}
	if err := n.store.RaiseNotification(notif); err != nil {
		slog.Error("notify: raise failed", "source_id", iss.ID, "err", err)
	}
}

// HealthCleared marks the notification for a cleared health issue resolved.
// Mirrors HealthRaised's allowlist gate so a clear of a non-notifying issue is
// a true no-op (no store round-trip). Resolving an issue that never produced a
// notification is harmless either way — the store call is idempotent.
func (n *Notifier) HealthCleared(id, instanceKey string) {
	if _, ok := healthRules[id]; !ok {
		return
	}
	if err := n.store.ResolveNotification(healthDedupKey(id, instanceKey), n.now()); err != nil {
		slog.Error("notify: resolve failed", "source_id", id, "err", err)
	}
}

// healthDedupKey is the coalescing key for a HEALTH-derived notification:
// "health:<id>" for box-wide issues, "health:<id>:<instanceKey>" for
// per-instance ones. Stable across raise and clear so a clear resolves exactly
// the notification its raise created.
func healthDedupKey(id, instanceKey string) string {
	if instanceKey == "" {
		return "health:" + id
	}
	return "health:" + id + ":" + instanceKey
}
