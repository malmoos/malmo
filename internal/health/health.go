// Package health is the brain's typed-issue registry per HEALTH.md.
//
// An Issue is a fact the brain holds: detection creates it, kept until the
// underlying condition clears, surfaced via GET /api/v1/health and (later)
// the SSE event channel. The taxonomy is registered in code — IDs are stable
// strings, severity/category/tier/blocks_* flags are bound to the ID at
// registration, not redeclared per raise.
//
// SQLite persistence: the Manager writes through a HealthStore on every
// raise/clear so issues survive brain restarts. Pass nil for tests that don't
// need persistence.
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
	CategoryStorage  Category = "storage"
	CategoryState    Category = "state"
	CategoryNetwork  Category = "network"
	CategoryVersion  Category = "version"
	CategoryCapacity Category = "capacity"
)

// Definition binds an issue ID to its static metadata. Registered at brain
// startup; the registry is the contract.
//
// NoPersist marks internal issues that must never be written to SQLite — used
// for issues that exist precisely because the store itself is broken. Raise
// and Clear skip the store for any definition with NoPersist: true.
type Definition struct {
	ID           string
	Category     Category
	Severity     Severity
	Tier         int
	BlocksWrites bool
	BlocksApps   bool
	BlocksUsers  bool
	Summary      string
	NoPersist    bool
}

// Issue is the JSON shape returned by GET /api/v1/health and the in-memory
// record the manager holds. Mirrors HEALTH.md # Issue shape; the Actions
// list is deferred to a follow-up (the dashboard needs it before this earns
// its keep).
type Issue struct {
	ID            string    `json:"id"`
	InstanceKey   string    `json:"instance_key,omitempty"`
	Category      Category  `json:"category"`
	Severity      Severity  `json:"severity"`
	Tier          int       `json:"tier"`
	BlocksWrites  bool      `json:"blocks_writes"`
	BlocksApps    bool      `json:"blocks_apps"`
	BlocksUsers   bool      `json:"blocks_users"`
	Summary       string    `json:"summary"`
	Details       string    `json:"details,omitempty"`
	RaisedAt      time.Time `json:"raised_at"`
	LastCheckedAt time.Time `json:"last_checked_at"`
}

// IssueKey uniquely identifies one issue instance. Used in BatchUpsertAndDelete
// to avoid exporting the internal issueKey map type.
type IssueKey struct {
	ID          string
	InstanceKey string
}

// HealthStore persists health issues across brain restarts. The interface is
// consumer-side (CLAUDE.md): health does not import store.
type HealthStore interface {
	// UpsertHealthIssue inserts or replaces one issue row (used by Raise).
	UpsertHealthIssue(Issue) error
	// DeleteHealthIssue removes one issue row (used by Clear).
	DeleteHealthIssue(id, instanceKey string) error
	// BatchUpsertAndDelete runs upserts and deletes in a single transaction.
	// Used by ApplyStorageFindings so a crash mid-reconcile can't leave SQLite
	// torn between old and new state.
	BatchUpsertAndDelete(upserts []Issue, deletes []IssueKey) error
	// ListHealthIssues returns all rows; used only at brain startup (LoadFromStore).
	ListHealthIssues() ([]Issue, error)
}

// Manager holds the live issue set. Thread-safe.
type Manager struct {
	mu          sync.Mutex
	definitions map[string]Definition
	active      map[issueKey]Issue
	now         func() time.Time
	store       HealthStore // nil means no persistence (tests)
}

type issueKey struct {
	id          string
	instanceKey string
}

// NewManager returns a Manager pre-loaded with the v1 storage definitions
// (HEALTH.md # Storage). Pass a non-nil store to persist issues across restarts;
// pass nil for in-memory-only use (tests, dev builds without SQLite).
func NewManager(store HealthStore) *Manager {
	m := &Manager{
		definitions: map[string]Definition{},
		active:      map[issueKey]Issue{},
		now:         func() time.Time { return time.Now().UTC() },
		store:       store,
	}
	for _, d := range builtinDefinitions() {
		m.definitions[d.ID] = d
	}
	return m
}

// LoadFromStore restores persisted issues into the in-memory registry at boot.
// Call once after NewManager, before the brain starts serving requests. If the
// store is nil or returns no rows, the registry starts empty (same as before
// persistence existed). On store error the brain logs and continues — degraded
// is better than refusing to start. Unknown IDs (issue renamed or removed in
// a brain upgrade/downgrade) are skipped and logged; rows remain in SQLite
// until the next Clear or a future cleanup sweep.
func (m *Manager) LoadFromStore() error {
	if m.store == nil {
		return nil
	}
	issues, err := m.store.ListHealthIssues()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var unknown []string
	for _, iss := range issues {
		if _, known := m.definitions[iss.ID]; !known {
			unknown = append(unknown, iss.ID)
			continue
		}
		k := issueKey{id: iss.ID, instanceKey: iss.InstanceKey}
		m.active[k] = iss
	}
	if len(unknown) > 0 {
		slog.Warn("health: skipped unknown issue IDs on load; rows left in store until next Clear",
			"ids", unknown)
	}
	return nil
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
	transitioned := m.raiseLocked(id, instanceKey, details)
	def := m.definitions[id]
	if m.store != nil && !def.NoPersist {
		if iss, ok := m.active[issueKey{id: id, instanceKey: instanceKey}]; ok {
			if err := m.store.UpsertHealthIssue(iss); err != nil {
				slog.Error("health: store upsert failed", "id", id, "err", err)
				m.raiseLocked("store-write-failed", "", err.Error())
			} else {
				m.clearLocked("store-write-failed", "")
			}
		}
	}
	return transitioned
}

// Clear removes the issue if active. Returns true if a transition happened.
func (m *Manager) Clear(id, instanceKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	transitioned := m.clearLocked(id, instanceKey)
	def := m.definitions[id]
	if m.store != nil && transitioned && !def.NoPersist {
		if err := m.store.DeleteHealthIssue(id, instanceKey); err != nil {
			slog.Error("health: store delete failed", "id", id, "err", err)
			m.raiseLocked("store-write-failed", "", err.Error())
		} else {
			m.clearLocked("store-write-failed", "")
		}
	}
	return transitioned
}

// Get returns the active issue for (id, instanceKey) and whether it is
// currently raised. Used by the notification emitter to read an issue's
// severity/summary at transition time — ApplyStorageFindings returns only the
// transitioned keys, not the full Issue. Returns ok=false for a cleared or
// never-raised issue.
func (m *Manager) Get(id, instanceKey string) (Issue, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	iss, ok := m.active[issueKey{id: id, instanceKey: instanceKey}]
	return iss, ok
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
// All SQLite writes go through a single BatchUpsertAndDelete call so a crash
// mid-reconcile can't leave the store torn between old and new state.
//
// This is the entire bridge between the host-side reporter and the brain's
// issue model. Periodic polling of GET /v1/health/storage drives the
// reconciliation; transitions return as the raised / cleared issue keys so
// the caller can emit one per-issue audit record (target {kind: health_issue,
// id: <id>}) rather than a bulk count. Findings whose ID is not in the
// registry are logged at warn and dropped — that's a reporter-vs-brain
// version skew the operator needs to see.
func (m *Manager) ApplyStorageFindings(sh protocol.StorageHealth) (raised, cleared []IssueKey) {
	// wantKeys uses the full issueKey (id + instanceKey) so that when Finding
	// grows an InstanceKey field, per-instance storage findings don't collapse.
	// Today all findings carry instanceKey="" (Finding has no InstanceKey field),
	// but the reconcile loop is correct for the future shape already.
	wantKeys := map[issueKey]string{} // key → details
	for _, f := range sh.Findings {
		wantKeys[issueKey{id: f.ID, instanceKey: ""}] = f.Details
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var toUpsert []Issue
	var toDelete []IssueKey
	unknownIDs := []string{}

	for k, details := range wantKeys {
		if _, known := m.definitions[k.id]; !known {
			unknownIDs = append(unknownIDs, k.id)
			continue
		}
		if m.raiseLocked(k.id, k.instanceKey, details) {
			raised = append(raised, IssueKey{ID: k.id, InstanceKey: k.instanceKey})
		}
		// Upsert on every poll tick (not just transitions) to keep last_checked_at
		// current in SQLite. At the current 60s cadence with a handful of findings
		// this is negligible. If the poll interval shortens materially, consider
		// moving last_checked_at updates to a lazy write (on List() or a background
		// sweep) to avoid per-tick writes for stable issues.
		if m.store != nil {
			if iss, ok := m.active[issueKey{id: k.id, instanceKey: k.instanceKey}]; ok {
				toUpsert = append(toUpsert, iss)
			}
		}
	}
	for k, iss := range m.active {
		if iss.Category != CategoryStorage {
			continue
		}
		if _, keep := wantKeys[k]; keep {
			continue
		}
		if m.clearLocked(k.id, k.instanceKey) {
			cleared = append(cleared, IssueKey{ID: k.id, InstanceKey: k.instanceKey})
			if m.store != nil {
				toDelete = append(toDelete, IssueKey{ID: k.id, InstanceKey: k.instanceKey})
			}
		}
	}

	if len(unknownIDs) > 0 {
		slog.Warn("storage health: dropped unknown finding ids",
			"ids", unknownIDs)
	}

	if m.store != nil && (len(toUpsert) > 0 || len(toDelete) > 0) {
		if err := m.store.BatchUpsertAndDelete(toUpsert, toDelete); err != nil {
			slog.Error("health: batch store write failed",
				"upserts", len(toUpsert), "deletes", len(toDelete), "err", err)
			m.raiseLocked("store-write-failed", "", err.Error())
		} else {
			m.clearLocked("store-write-failed", "")
		}
	}

	// Map iteration order is non-deterministic; sort so the per-issue audit
	// records (and tests) see a stable order, matching List()'s contract.
	sortIssueKeys(raised)
	sortIssueKeys(cleared)
	return raised, cleared
}

func sortIssueKeys(ks []IssueKey) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].ID != ks[j].ID {
			return ks[i].ID < ks[j].ID
		}
		return ks[i].InstanceKey < ks[j].InstanceKey
	})
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
		// Internal — raised when any store write fails persistently. NoPersist
		// because it exists precisely when persistence is broken: writing it to
		// SQLite would fail too. Surfaced on the dashboard so the operator knows
		// health issues won't survive a restart until the store recovers.
		{
			ID: "store-write-failed", Category: CategoryState,
			Severity: SeverityError, Tier: 2,
			BlocksWrites: false, BlocksApps: false, BlocksUsers: false,
			Summary:   "malmo can't save health state; issues won't survive a restart.",
			NoPersist: true,
		},
	}
}
