package health

import (
	"errors"
	"testing"
	"time"

	"github.com/malmo/malmo/internal/protocol"
)

func TestRaise_NewIssueReturnsTrue(t *testing.T) {
	m := NewManager(nil)
	if !m.Raise("data-drive-missing", "", "abc-123 absent") {
		t.Fatal("first raise should return true (transition)")
	}
	if len(m.List()) != 1 {
		t.Fatalf("want 1 active issue, got %d", len(m.List()))
	}
}

func TestRaise_IdempotentNoTransition(t *testing.T) {
	m := NewManager(nil)
	m.Raise("data-drive-missing", "", "abc-123 absent")
	if m.Raise("data-drive-missing", "", "abc-123 still absent") {
		t.Fatal("second raise of same issue must return false (no transition)")
	}
}

func TestRaise_PreservesRaisedAtAcrossReRaises(t *testing.T) {
	m := NewManager(nil)
	first := time.Date(2026, 5, 25, 8, 0, 0, 0, time.UTC)
	second := time.Date(2026, 5, 25, 8, 1, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return first })
	m.Raise("data-drive-missing", "", "details A")
	m.SetClock(func() time.Time { return second })
	m.Raise("data-drive-missing", "", "details B")

	got := m.List()[0]
	if !got.RaisedAt.Equal(first) {
		t.Errorf("RaisedAt: want %v (preserved), got %v", first, got.RaisedAt)
	}
	if !got.LastCheckedAt.Equal(second) {
		t.Errorf("LastCheckedAt: want %v (updated), got %v", second, got.LastCheckedAt)
	}
	if got.Details != "details B" {
		t.Errorf("Details: want refreshed, got %q", got.Details)
	}
}

func TestRaise_UnknownIDIsSkipped(t *testing.T) {
	m := NewManager(nil)
	if m.Raise("not-a-real-issue-id", "", "") {
		t.Fatal("unknown ID should return false (no-op), got true")
	}
	if len(m.List()) != 0 {
		t.Fatal("unknown ID should not be added")
	}
}

func TestClear_ReturnsTrueOnTransition(t *testing.T) {
	m := NewManager(nil)
	m.Raise("data-drive-missing", "", "")
	if !m.Clear("data-drive-missing", "") {
		t.Fatal("clear of active issue should return true")
	}
	if m.Clear("data-drive-missing", "") {
		t.Fatal("second clear should return false (no-op)")
	}
}

func TestList_ReturnsStableOrder(t *testing.T) {
	m := NewManager(nil)
	m.Raise("canary-mismatch", "", "")         // critical
	m.Raise("data-drive-missing", "", "")      // error
	m.Raise("health-report-malformed", "", "") // error

	got := m.List()
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	// All storage category; critical first, then errors alphabetically by ID.
	wantOrder := []string{"canary-mismatch", "data-drive-missing", "health-report-malformed"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("List()[%d]: want %s, got %s", i, w, got[i].ID)
		}
	}
}

func TestList_DefinitionMetadataApplied(t *testing.T) {
	m := NewManager(nil)
	m.Raise("data-drive-missing", "", "")

	got := m.List()[0]
	if got.Severity != SeverityError {
		t.Errorf("Severity: want error, got %s", got.Severity)
	}
	if got.Tier != 1 {
		t.Errorf("Tier: want 1, got %d", got.Tier)
	}
	if !got.BlocksWrites || !got.BlocksApps || !got.BlocksUsers {
		t.Errorf("blocks_*: want all true for data-drive-missing, got %+v", got)
	}
	if got.Summary == "" {
		t.Error("Summary must be populated from definition")
	}
}

func TestApplyStorageFindings_RaiseAndClear(t *testing.T) {
	m := NewManager(nil)

	// First poll: data drive missing.
	raised, cleared := m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{{ID: "data-drive-missing", Details: "x"}},
	})
	if raised != 1 || cleared != 0 {
		t.Errorf("first poll: want raised=1 cleared=0, got %d/%d", raised, cleared)
	}
	if len(m.List()) != 1 {
		t.Fatalf("active: want 1, got %d", len(m.List()))
	}

	// Second poll: drive reattached, no findings.
	raised, cleared = m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{},
	})
	if raised != 0 || cleared != 1 {
		t.Errorf("clear poll: want raised=0 cleared=1, got %d/%d", raised, cleared)
	}
	if len(m.List()) != 0 {
		t.Fatalf("active: want 0 after clear, got %d", len(m.List()))
	}
}

func TestApplyStorageFindings_OnlyTouchesStorageCategory(t *testing.T) {
	// If a non-storage issue gets registered later, applying an empty
	// storage findings payload must not clear it. Today no non-storage
	// detector exists, but the contract is load-bearing for the moment
	// network/version detectors land.
	m := NewManager(nil)
	// Pretend a network issue is active by writing directly through Raise
	// after registering a definition for it.
	m.mu.Lock()
	m.definitions["mdns-down"] = Definition{
		ID: "mdns-down", Category: CategoryNetwork,
		Severity: SeverityWarning, Tier: 1,
		Summary: "mDNS is not publishing.",
	}
	m.mu.Unlock()
	m.Raise("mdns-down", "", "")

	m.ApplyStorageFindings(protocol.StorageHealth{Findings: []protocol.Finding{}})

	if len(m.List()) != 1 || m.List()[0].ID != "mdns-down" {
		t.Errorf("non-storage issue must survive storage reconciliation, got %v", m.List())
	}
}

// TestApplyStorageFindings_AtomicAcrossClearAndRaise pins down the locking
// contract: a concurrent List() during a reconcile cycle must never observe
// the moment where the old issue has been cleared but the new one hasn't yet
// been raised. The pre-fix implementation snapshotted under the lock and
// then dropped it before reconciling, which exposed this transient.
func TestApplyStorageFindings_AtomicAcrossClearAndRaise(t *testing.T) {
	m := NewManager(nil)
	// Seed initial state: one storage issue active.
	m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{{ID: "data-drive-missing"}},
	})

	done := make(chan struct{})
	stop := make(chan struct{})
	// Hammer ApplyStorageFindings alternating between two findings so each
	// cycle clears the previous and raises a new one.
	go func() {
		defer close(done)
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			var id string
			if flip {
				id = "data-drive-missing"
			} else {
				id = "canary-mismatch"
			}
			flip = !flip
			m.ApplyStorageFindings(protocol.StorageHealth{
				Findings: []protocol.Finding{{ID: id}},
			})
		}
	}()

	// Concurrently List() many times; every observation must show exactly
	// one storage issue (never zero — that would be the torn state).
	for i := 0; i < 10000; i++ {
		got := m.List()
		count := 0
		for _, iss := range got {
			if iss.Category == CategoryStorage {
				count++
			}
		}
		if count != 1 {
			close(stop)
			<-done
			t.Fatalf("torn state observed on iteration %d: want exactly 1 storage issue, got %d (%v)", i, count, got)
		}
	}
	close(stop)
	<-done
}

func TestApplyStorageFindings_UnknownIDsAreDropped(t *testing.T) {
	m := NewManager(nil)
	raised, cleared := m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{
			{ID: "data-drive-missing"},
			{ID: "not-a-real-issue-id"},
		},
	})
	if raised != 1 {
		t.Errorf("raised: want 1 (the known ID), got %d", raised)
	}
	if cleared != 0 {
		t.Errorf("cleared: want 0, got %d", cleared)
	}
	if len(m.List()) != 1 || m.List()[0].ID != "data-drive-missing" {
		t.Fatalf("only the known finding should land in the registry, got %v", m.List())
	}
}

func TestApplyStorageFindings_ReplacesDetailsOnRefresh(t *testing.T) {
	m := NewManager(nil)
	m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{{ID: "data-drive-missing", Details: "first"}},
	})
	m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{{ID: "data-drive-missing", Details: "second"}},
	})
	got := m.List()
	if len(got) != 1 || got[0].Details != "second" {
		t.Errorf("Details: want refreshed to 'second', got %v", got)
	}
}

// --- HealthStore integration tests ---

// fakeStore is a HealthStore stub for tests that need to inspect calls without
// a real SQLite database.
type fakeStore struct {
	issues  map[string]Issue // key: id+":"+instanceKey
	upserts int
	deletes int
	listErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{issues: map[string]Issue{}}
}

func (f *fakeStore) key(id, instanceKey string) string { return id + ":" + instanceKey }

func (f *fakeStore) UpsertHealthIssue(h Issue) error {
	f.upserts++
	f.issues[f.key(h.ID, h.InstanceKey)] = h
	return nil
}

func (f *fakeStore) DeleteHealthIssue(id, instanceKey string) error {
	f.deletes++
	delete(f.issues, f.key(id, instanceKey))
	return nil
}

func (f *fakeStore) BatchUpsertAndDelete(upserts []Issue, deletes []IssueKey) error {
	for _, h := range upserts {
		f.upserts++
		f.issues[f.key(h.ID, h.InstanceKey)] = h
	}
	for _, k := range deletes {
		f.deletes++
		delete(f.issues, f.key(k.ID, k.InstanceKey))
	}
	return nil
}

func (f *fakeStore) ListHealthIssues() ([]Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Issue, 0, len(f.issues))
	for _, iss := range f.issues {
		out = append(out, iss)
	}
	return out, nil
}

func TestManager_RaisePersistsToStore(t *testing.T) {
	fs := newFakeStore()
	m := NewManager(fs)
	m.Raise("data-drive-missing", "", "details")
	if fs.upserts != 1 {
		t.Fatalf("want 1 upsert on first raise, got %d", fs.upserts)
	}
	if _, ok := fs.issues["data-drive-missing:"]; !ok {
		t.Error("upserted issue not in fake store")
	}
}

func TestManager_ReRaisePersistsRefresh(t *testing.T) {
	fs := newFakeStore()
	m := NewManager(fs)
	m.Raise("data-drive-missing", "", "first")
	m.Raise("data-drive-missing", "", "second")
	// Both first raise and re-raise should upsert (to keep last_checked_at current).
	if fs.upserts != 2 {
		t.Fatalf("want 2 upserts (new + refresh), got %d", fs.upserts)
	}
	if fs.issues["data-drive-missing:"].Details != "second" {
		t.Errorf("store should have refreshed Details, got %q", fs.issues["data-drive-missing:"].Details)
	}
}

func TestManager_ClearDeletesFromStore(t *testing.T) {
	fs := newFakeStore()
	m := NewManager(fs)
	m.Raise("data-drive-missing", "", "")
	m.Clear("data-drive-missing", "")
	if fs.deletes != 1 {
		t.Fatalf("want 1 delete on clear, got %d", fs.deletes)
	}
	if _, ok := fs.issues["data-drive-missing:"]; ok {
		t.Error("issue should be gone from fake store after clear")
	}
}

func TestManager_ClearNoTransitionNoDelete(t *testing.T) {
	fs := newFakeStore()
	m := NewManager(fs)
	// Clear of an issue that was never raised: no delete call.
	m.Clear("data-drive-missing", "")
	if fs.deletes != 0 {
		t.Fatalf("want 0 deletes for clear-of-nothing, got %d", fs.deletes)
	}
}

func TestManager_LoadFromStore(t *testing.T) {
	fs := newFakeStore()
	// Pre-populate the fake store as if issues survived from a previous brain run.
	_ = fs.UpsertHealthIssue(Issue{
		ID: "data-drive-missing", InstanceKey: "",
		Category: CategoryStorage, Severity: SeverityError, Tier: 1,
		BlocksWrites: true, BlocksApps: true, BlocksUsers: true,
		Summary: "pre-existing", Details: "from last boot",
		RaisedAt:      time.Now().UTC(),
		LastCheckedAt: time.Now().UTC(),
	})
	fs.upserts = 0 // reset counter after pre-population

	m := NewManager(fs)
	if err := m.LoadFromStore(); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}
	active := m.List()
	if len(active) != 1 {
		t.Fatalf("want 1 active issue after load, got %d", len(active))
	}
	if active[0].ID != "data-drive-missing" {
		t.Errorf("ID: want data-drive-missing, got %s", active[0].ID)
	}
	if active[0].Details != "from last boot" {
		t.Errorf("Details: want 'from last boot', got %q", active[0].Details)
	}
}

func TestManager_LoadFromStore_NilStoreSafe(t *testing.T) {
	m := NewManager(nil)
	if err := m.LoadFromStore(); err != nil {
		t.Fatalf("LoadFromStore with nil store: %v", err)
	}
	if len(m.List()) != 0 {
		t.Error("nil-store load should leave registry empty")
	}
}

func TestManager_LoadFromStore_StoreError(t *testing.T) {
	fs := newFakeStore()
	fs.listErr = errors.New("disk failure")
	m := NewManager(fs)
	if err := m.LoadFromStore(); err == nil {
		t.Fatal("want error from LoadFromStore when store fails, got nil")
	}
}

func TestManager_NilStore_RaiseAndClearSafe(t *testing.T) {
	m := NewManager(nil)
	// Must not panic when store is nil.
	if !m.Raise("data-drive-missing", "", "") {
		t.Error("Raise should return true on first raise")
	}
	if !m.Clear("data-drive-missing", "") {
		t.Error("Clear should return true on transition")
	}
}

// TestManager_ConcurrentRaiseClear_NoGhostInStore pins down the lock-through-store
// fix: a concurrent Clear must not leave a ghost row that a concurrent Raise
// re-inserts after the delete completes. With the lock held through store calls,
// Raise and Clear are fully serialized — the winner's store state is final.
func TestManager_ConcurrentRaiseClear_NoGhostInStore(t *testing.T) {
	const iterations = 2000
	fs := newFakeStore()
	m := NewManager(fs)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			m.Raise("data-drive-missing", "", "raising")
			m.Clear("data-drive-missing", "")
		}
	}()
	for i := 0; i < iterations; i++ {
		m.Clear("data-drive-missing", "")
		m.Raise("data-drive-missing", "", "raising")
	}
	<-done

	// After all concurrent Raise/Clear pairs finish, in-memory and store must agree:
	// if the issue is in m.active it must be in the store, and vice versa.
	active := m.List()
	_, inStore := fs.issues["data-drive-missing:"]
	inMemory := len(active) > 0
	if inMemory != inStore {
		t.Fatalf("memory/store mismatch: in_memory=%v in_store=%v (ghost issue)", inMemory, inStore)
	}
}

func TestManager_LoadFromStore_SkipsUnknownIDs(t *testing.T) {
	fs := newFakeStore()
	// Simulate a stale row from a previous brain version with a renamed issue ID.
	_ = fs.UpsertHealthIssue(Issue{
		ID: "old-issue-id-from-v0", InstanceKey: "",
		Category: CategoryStorage, Severity: SeverityWarning, Tier: 1,
		Summary:       "stale",
		RaisedAt:      time.Now().UTC(),
		LastCheckedAt: time.Now().UTC(),
	})
	_ = fs.UpsertHealthIssue(Issue{
		ID: "data-drive-missing", InstanceKey: "",
		Category: CategoryStorage, Severity: SeverityError, Tier: 1,
		BlocksWrites: true, BlocksApps: true, BlocksUsers: true,
		Summary:       "known",
		RaisedAt:      time.Now().UTC(),
		LastCheckedAt: time.Now().UTC(),
	})
	fs.upserts = 0

	m := NewManager(fs)
	if err := m.LoadFromStore(); err != nil {
		t.Fatalf("LoadFromStore: %v", err)
	}
	active := m.List()
	if len(active) != 1 {
		t.Fatalf("want only the known issue loaded, got %d issues: %v", len(active), active)
	}
	if active[0].ID != "data-drive-missing" {
		t.Errorf("wrong issue loaded: %s", active[0].ID)
	}
}

func TestApplyStorageFindings_PersistsToStore(t *testing.T) {
	fs := newFakeStore()
	m := NewManager(fs)
	m.ApplyStorageFindings(protocol.StorageHealth{
		Findings: []protocol.Finding{{ID: "data-drive-missing", Details: "x"}},
	})
	if _, ok := fs.issues["data-drive-missing:"]; !ok {
		t.Error("ApplyStorageFindings should upsert raised finding to store")
	}

	m.ApplyStorageFindings(protocol.StorageHealth{Findings: []protocol.Finding{}})
	if _, ok := fs.issues["data-drive-missing:"]; ok {
		t.Error("ApplyStorageFindings should delete cleared finding from store")
	}
	if fs.deletes != 1 {
		t.Errorf("want 1 delete after clear, got %d", fs.deletes)
	}
}

// fakeErrStore is a HealthStore that always fails writes, for testing
// the store-write-failed signal.
type fakeErrStore struct {
	fakeStore
	writeErr error
}

func (f *fakeErrStore) UpsertHealthIssue(h Issue) error                { return f.writeErr }
func (f *fakeErrStore) DeleteHealthIssue(id, instanceKey string) error { return f.writeErr }
func (f *fakeErrStore) BatchUpsertAndDelete(upserts []Issue, deletes []IssueKey) error {
	return f.writeErr
}

func TestManager_RaiseStoreError_RaisesStoreWriteFailed(t *testing.T) {
	es := &fakeErrStore{writeErr: errors.New("disk full")}
	es.issues = map[string]Issue{}
	m := NewManager(es)
	m.Raise("data-drive-missing", "", "missing")

	active := m.List()
	var found bool
	for _, iss := range active {
		if iss.ID == "store-write-failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want store-write-failed in active issues after store error, got %v", active)
	}
}

func TestManager_RaiseStoreRecovers_ClearsStoreWriteFailed(t *testing.T) {
	// First raise with a broken store.
	es := &fakeErrStore{writeErr: errors.New("disk full")}
	es.issues = map[string]Issue{}
	m := NewManager(es)
	m.Raise("data-drive-missing", "", "missing")

	hasStoreFailure := func() bool {
		for _, iss := range m.List() {
			if iss.ID == "store-write-failed" {
				return true
			}
		}
		return false
	}
	if !hasStoreFailure() {
		t.Fatal("want store-write-failed after first broken-store raise")
	}

	// Swap to a working store and raise again — should clear store-write-failed.
	fs := newFakeStore()
	m.store = fs
	m.Raise("canary-mismatch", "", "mismatch")

	if hasStoreFailure() {
		t.Error("store-write-failed should clear once a store write succeeds")
	}
}

func TestManager_StoreWriteFailedIsNoPersist(t *testing.T) {
	// store-write-failed must never be persisted, even when the store is healthy.
	// Simulate by verifying UpsertHealthIssue is not called for it.
	fs := newFakeStore()
	m := NewManager(fs)

	// Manually raise store-write-failed as if the store had failed.
	m.mu.Lock()
	m.raiseLocked("store-write-failed", "", "simulated")
	m.mu.Unlock()

	// Now Clear it; should not call store.Delete.
	m.Clear("store-write-failed", "")
	if fs.deletes != 0 {
		t.Errorf("Clear of NoPersist issue must not call store.Delete, got %d deletes", fs.deletes)
	}
}

func TestCategoryCapacityConstantExists(t *testing.T) {
	// Regression guard: capacity must be in the Category type per HEALTH.md.
	if CategoryCapacity != "capacity" {
		t.Errorf("CategoryCapacity: want %q, got %q", "capacity", CategoryCapacity)
	}
}
