package main

import (
	"context"
	"testing"

	"github.com/malmo/malmo/internal/audit"
	"github.com/malmo/malmo/internal/health"
	"github.com/malmo/malmo/internal/store"
)

// fakeEventStore captures audit rows so the per-issue emission can be asserted
// without a real SQLite store. Satisfies audit.EventStore.
type fakeEventStore struct {
	events []store.AuditEvent
}

func (f *fakeEventStore) InsertAuditEvent(e store.AuditEvent) error {
	f.events = append(f.events, e)
	return nil
}

// TestEmitHealthTransitions_OneRecordPerIssue is the direct test of this
// slice's headline behavior: one audit record per transitioned issue, each
// targeting {kind: health_issue, id: <id>} with a system actor — not a bulk
// count record.
func TestEmitHealthTransitions_OneRecordPerIssue(t *testing.T) {
	fs := &fakeEventStore{}
	auditor := audit.New(fs)

	raised := []health.IssueKey{{ID: "data-drive-missing"}, {ID: "canary-mismatch"}}
	cleared := []health.IssueKey{{ID: "mergerfs-assembly-failed"}}

	emitHealthTransitions(context.Background(), auditor, raised, cleared)

	if len(fs.events) != 3 {
		t.Fatalf("want 3 audit records (2 raised + 1 cleared), got %d", len(fs.events))
	}

	// Raised records come first, in the order passed, each targeting its issue.
	for i, k := range raised {
		e := fs.events[i]
		if e.Action != audit.ActionHealthIssueRaised {
			t.Errorf("event %d: action = %q, want %q", i, e.Action, audit.ActionHealthIssueRaised)
		}
		if e.TargetKind != "health_issue" || e.TargetID != k.ID {
			t.Errorf("event %d: target = {%q,%q}, want {health_issue,%q}", i, e.TargetKind, e.TargetID, k.ID)
		}
		if !e.Success {
			t.Errorf("event %d: success = false, want true", i)
		}
		// No identity in ctx → system actor (no false attribution to a user).
		if e.ActorRole != "system" || e.ActorUserID != "" {
			t.Errorf("event %d: actor = {%q,%q}, want {system,<empty>}", i, e.ActorRole, e.ActorUserID)
		}
	}

	// The cleared record uses the clear action and targets the dropped issue.
	c := fs.events[2]
	if c.Action != audit.ActionHealthIssueCleared || c.TargetID != "mergerfs-assembly-failed" {
		t.Errorf("cleared event = {%q,%q}, want {%q,mergerfs-assembly-failed}",
			c.Action, c.TargetID, audit.ActionHealthIssueCleared)
	}
}

// TestEmitHealthTransitions_NoTransitionsNoRecords pins the steady-state case:
// a poll that changes nothing must not write any audit rows.
func TestEmitHealthTransitions_NoTransitionsNoRecords(t *testing.T) {
	fs := &fakeEventStore{}
	emitHealthTransitions(context.Background(), audit.New(fs), nil, nil)
	if len(fs.events) != 0 {
		t.Fatalf("want 0 records for no transitions, got %d", len(fs.events))
	}
}
