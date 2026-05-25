// Package health is the brain's typed-issue registry per HEALTH.md.
//
// An Issue is a fact the brain holds: detection creates it, kept until the
// underlying condition clears, surfaced via GET /api/v1/health and (later)
// the SSE event channel. The taxonomy is registered in code — IDs are stable
// strings, severity/category/tier/blocks_* flags are bound to the ID at
// registration, not redeclared per raise.
//
// v1 scope: in-memory only. SQLite persistence (health_issues table per
// HEALTH.md # Persistence) is a follow-up — see docs/progress/0019.
package health

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/malmo/malmo/internal/protocol"
)

// Severity is one of warning | error | critical per HEALTH.md.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Category is one of storage | state | network | version | capacity per HEALTH.md.
type Category string

const (
	CategoryStorage Category = "storage"
	CategoryState   Category = "state"
	CategoryNetwork Category = "network"
	CategoryVersion Category = "version"
)

// Definition binds an issue ID to its static metadata. Registered at brain
// startup; the registry is the contract.
type Definition struct {
	ID            string
	Category      Category
	Severity      Severity
	Tier          int
	BlocksWrites  bool
	BlocksApps    bool
	BlocksUsers   bool
	Summary       string
}

// Issue is the JSON shape returned by GET /api/v1/health and the in-memory
// record the manager holds. Mirrors HEALTH.md # Issue shape; the Actions
// list is deferred to a follow-up (the dashboard needs it before this earns
// its keep).
type Issue struct {
	ID             string    `json:"id"`
	InstanceKey    string    `json:"instance_key,omitempty"`
	Category       Category  `json:"category"`
	Severity       Severity  `json:"severity"`
	Tier           int       `json:"tier"`
	BlocksWrites   bool      `json:"blocks_writes"`
	BlocksApps     bool      `json:"blocks_apps"`
	BlocksUsers    bool      `json:"blocks_users"`
	Summary        string    `json:"summary"`
	Details        string    `json:"details,omitempty"`
	RaisedAt       time.Time `json:"raised_at"`
	LastCheckedAt  time.Time `json:"last_checked_at"`
}

// Manager holds the live issue set. Thread-safe.
type Manager struct {
	mu          sync.Mutex
	definitions map[string]Definition
	active      map[issueKey]Issue
	now         func() time.Time
}

type issueKey struct {
	id          string
	instanceKey string
}

// NewManager returns a Manager pre-loaded with the v1 storage definitions
// (HEALTH.md # Storage). Additional categories register as the brain learns
// them.
func NewManager() *Manager {
	m := &Manager{
		definitions: map[string]Definition{},
		active:      map[issueKey]Issue{},
		now:         func() time.Time { return time.Now().UTC() },
	}
	for _, d := range builtinDefinitions() {
		m.definitions[d.ID] = d
	}
	return m
}

// SetClock swaps the time source — tests use this to assert RaisedAt /
// LastCheckedAt without sleeping.
func (m *Manager) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// Raise asserts the named issue is currently true. Idempotent: re-raising an
// active issue updates LastCheckedAt and Details, leaves RaisedAt alone, and
// does not emit a duplicate audit event when one lands. instanceKey may be
// empty for box-wide issues.
//
// Returns true if this raise transitioned the issue from cleared to active
// (the caller's signal to emit an audit event / notification). Returns false
// for an unknown ID — callers that *might* pass an unregistered ID (e.g.
// reconciling findings from an external reporter) should detect false and log,
// since this method intentionally does not have enough context to attribute
// the source of the drop.
func (m *Manager) Raise(id, instanceKey, details string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.raiseLocked(id, instanceKey, details)
}

// Clear removes the issue if active. Returns true if a transition happened.
func (m *Manager) Clear(id, instanceKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clearLocked(id, instanceKey)
}

// raiseLocked is the lock-held core of Raise, callable from reconcilers that
// already hold m.mu (e.g. ApplyStorageFindings, which must reconcile the
// whole storage category atomically so List() never sees a torn state where
// old issues are cleared but new ones haven't been raised yet).
func (m *Manager) raiseLocked(id, instanceKey, details string) bool {
	def, ok := m.definitions[id]
	if !ok {
		return false
	}
	now := m.now()
	k := issueKey{id: id, instanceKey: instanceKey}
	if existing, ok := m.active[k]; ok {
		existing.LastCheckedAt = now
		existing.Details = details
		m.active[k] = existing
		return false
	}
	m.active[k] = Issue{
		ID:            def.ID,
		InstanceKey:   instanceKey,
		Category:      def.Category,
		Severity:      def.Severity,
		Tier:          def.Tier,
		BlocksWrites:  def.BlocksWrites,
		BlocksApps:    def.BlocksApps,
		BlocksUsers:   def.BlocksUsers,
		Summary:       def.Summary,
		Details:       details,
		RaisedAt:      now,
		LastCheckedAt: now,
	}
	return true
}

// clearLocked is the lock-held core of Clear. See raiseLocked.
func (m *Manager) clearLocked(id, instanceKey string) bool {
	k := issueKey{id: id, instanceKey: instanceKey}
	if _, ok := m.active[k]; !ok {
		return false
	}
	delete(m.active, k)
	return true
}

// List returns the active issues, sorted (Category, Severity desc, ID,
// InstanceKey) so callers and tests see a stable order.
func (m *Manager) List() []Issue {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Issue, 0, len(m.active))
	for _, iss := range m.active {
		out = append(out, iss)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].InstanceKey < out[j].InstanceKey
	})
	return out
}

// ApplyStorageFindings reconciles the live issue set against a fresh
// StorageHealth payload from host-agent: any storage-category issue not
// present in findings is cleared, every finding is raised (or refreshed).
// The clear+raise cycle runs under a single critical section so a
// concurrent List() never sees a transient "all clear" between the clears
// and the new raises.
//
// This is the entire bridge between the host-side reporter and the brain's
// issue model. Periodic polling of GET /v1/health/storage drives the
// reconciliation; transitions return as raised / cleared counts for the
// caller's audit-event hook. Findings whose ID is not in the registry are
// logged at warn and dropped — that's a reporter-vs-brain version skew the
// operator needs to see.
func (m *Manager) ApplyStorageFindings(sh protocol.StorageHealth) (raised, cleared int) {
	wantIDs := map[string]string{} // id → details
	for _, f := range sh.Findings {
		wantIDs[f.ID] = f.Details
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	unknownIDs := []string{}
	for id, details := range wantIDs {
		if _, known := m.definitions[id]; !known {
			unknownIDs = append(unknownIDs, id)
			continue
		}
		if m.raiseLocked(id, "", details) {
			raised++
		}
	}
	for k, iss := range m.active {
		if iss.Category != CategoryStorage {
			continue
		}
		if _, keep := wantIDs[k.id]; keep {
			continue
		}
		if m.clearLocked(k.id, k.instanceKey) {
			cleared++
		}
	}

	if len(unknownIDs) > 0 {
		slog.Warn("storage health: dropped unknown finding ids",
			"ids", unknownIDs)
	}
	return raised, cleared
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

// builtinDefinitions is the v1 typed-issue taxonomy. Mirrors the tables in
// HEALTH.md # Taxonomy. Slice #1 wires only storage findings end-to-end —
// the other categories are pre-registered so future detectors plug in.
func builtinDefinitions() []Definition {
	return []Definition{
		// Storage (HEALTH.md # Storage)
		{
			ID: "data-drive-missing", Category: CategoryStorage,
			Severity: SeverityError, Tier: 1,
			BlocksWrites: true, BlocksApps: true, BlocksUsers: true,
			Summary: "Your data drive isn't connected.",
		},
		{
			ID: "data-drive-wrong", Category: CategoryStorage,
			Severity: SeverityCritical, Tier: 2,
			BlocksWrites: true, BlocksApps: true, BlocksUsers: true,
			Summary: "A different drive is attached than the one malmo expects.",
		},
		// data-drive-readonly is pre-registered but not yet emitted by any
		// detector. The findmnt-based device-backing + mount-flags check that
		// will surface it is deferred (see docs/progress/0019 # What's next:
		// device-backing canary check). Definition lives here so the next
		// reporter slice plugs in without a registry edit.
		{
			ID: "data-drive-readonly", Category: CategoryStorage,
			Severity: SeverityCritical, Tier: 1,
			BlocksWrites: true, BlocksApps: true,
			Summary: "The data drive is mounted read-only (likely filesystem error).",
		},
		{
			ID: "canary-mismatch", Category: CategoryStorage,
			Severity: SeverityCritical, Tier: 2,
			BlocksWrites: true, BlocksApps: true,
			Summary: "Storage assembly succeeded but the canary check failed.",
		},
		{
			ID: "mergerfs-assembly-failed", Category: CategoryStorage,
			Severity: SeverityError, Tier: 2,
			BlocksWrites: true, BlocksApps: true,
			Summary: "malmo could not assemble the storage layout across your drives.",
		},
		// Synthetic — host-agent's FilesystemHealthSource emits this when the
		// report file is unparseable; the reporter emits it when its inputs are.
		{
			ID: "health-report-malformed", Category: CategoryStorage,
			Severity: SeverityError, Tier: 2,
			BlocksWrites: false, BlocksApps: false, BlocksUsers: false,
			Summary: "malmo's storage report is unreadable; storage state is unknown.",
		},
	}
}
