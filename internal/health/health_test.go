package health

import (
	"testing"
	"time"

	"github.com/malmo/malmo/internal/protocol"
)

func TestRaise_NewIssueReturnsTrue(t *testing.T) {
	m := NewManager()
	if !m.Raise("data-drive-missing", "", "abc-123 absent") {
		t.Fatal("first raise should return true (transition)")
	}
	if len(m.List()) != 1 {
		t.Fatalf("want 1 active issue, got %d", len(m.List()))
	}
}

func TestRaise_IdempotentNoTransition(t *testing.T) {
	m := NewManager()
	m.Raise("data-drive-missing", "", "abc-123 absent")
	if m.Raise("data-drive-missing", "", "abc-123 still absent") {
		t.Fatal("second raise of same issue must return false (no transition)")
	}
}

func TestRaise_PreservesRaisedAtAcrossReRaises(t *testing.T) {
	m := NewManager()
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
	m := NewManager()
	if m.Raise("not-a-real-issue-id", "", "") {
		t.Fatal("unknown ID should return false (no-op), got true")
	}
	if len(m.List()) != 0 {
		t.Fatal("unknown ID should not be added")
	}
}

func TestClear_ReturnsTrueOnTransition(t *testing.T) {
	m := NewManager()
	m.Raise("data-drive-missing", "", "")
	if !m.Clear("data-drive-missing", "") {
		t.Fatal("clear of active issue should return true")
	}
	if m.Clear("data-drive-missing", "") {
		t.Fatal("second clear should return false (no-op)")
	}
}

func TestList_ReturnsStableOrder(t *testing.T) {
	m := NewManager()
	m.Raise("canary-mismatch", "", "")        // critical
	m.Raise("data-drive-missing", "", "")     // error
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
	m := NewManager()
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
	m := NewManager()

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
	m := NewManager()
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
	m := NewManager()
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
	m := NewManager()
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
	m := NewManager()
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
