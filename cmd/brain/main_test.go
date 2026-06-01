package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/malmo/malmo/internal/audit"
	"github.com/malmo/malmo/internal/health"
	"github.com/malmo/malmo/internal/notify"
	"github.com/malmo/malmo/internal/protocol"
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

// fakeNotifStore captures notification raises/resolves so the cmd/brain
// dispatch can be asserted without SQLite. Satisfies notify.NotificationStore.
type fakeNotifStore struct {
	raised   []notify.Notification
	resolved []string
}

func (f *fakeNotifStore) RaiseNotification(n notify.Notification) error {
	f.raised = append(f.raised, n)
	return nil
}

func (f *fakeNotifStore) ResolveNotification(dedupKey string, _ time.Time) error {
	f.resolved = append(f.resolved, dedupKey)
	return nil
}

// TestEmitHealthNotifications_RaiseLooksUpIssueAndClearResolves is the wiring
// test for this slice: emitHealthNotifications resolves each raised key to its
// live Issue (via Manager.Get) and notifies, and resolves each cleared key by
// dedup_key. Allowlist filtering lives in notify and is tested there; here we
// pin the cmd/brain dispatch — Get-lookup, raise, and resolve.
func TestEmitHealthNotifications_RaiseLooksUpIssueAndClearResolves(t *testing.T) {
	mgr := health.NewManager(nil)
	// Raise two allowlisted issues so Get() returns them at dispatch time.
	mgr.Raise("data-drive-missing", "", "abc-123 absent")
	mgr.Raise("canary-mismatch", "", "checksum drift")

	fns := &fakeNotifStore{}
	notifier := notify.New(fns, nil)

	raised := []health.IssueKey{{ID: "data-drive-missing"}, {ID: "canary-mismatch"}}
	cleared := []health.IssueKey{{ID: "mergerfs-assembly-failed"}}

	emitHealthNotifications(notifier, mgr, raised, cleared)

	// Dispatch pins: each raised key is looked up via Manager.Get and produces an
	// admin notification carrying the live issue's data; the cleared key resolves
	// its problem dedup key. The richer raise/clear behavior (member transparency
	// and "all clear" follow-ups, which add more raised/resolved rows) is asserted
	// in internal/notify — here we only confirm the cmd/brain wiring.
	adminNotif := func(sourceID string) (notify.Notification, bool) {
		for _, n := range fns.raised {
			if n.SourceID == sourceID && n.Audience == notify.AudienceAdmins {
				return n, true
			}
		}
		return notify.Notification{}, false
	}

	ddm, ok := adminNotif("data-drive-missing")
	if !ok {
		t.Fatalf("no admin notification for data-drive-missing; raised = %v", fns.raised)
	}
	// The body is the live issue's Details — proves emitHealthNotifications resolved
	// the key through Manager.Get rather than synthesizing a stub.
	if ddm.Body != "abc-123 absent" {
		t.Errorf("data-drive-missing body = %q, want the live issue's details", ddm.Body)
	}
	if _, ok := adminNotif("canary-mismatch"); !ok {
		t.Errorf("no admin notification for canary-mismatch; raised = %v", fns.raised)
	}
	if !containsString(fns.resolved, "health:mergerfs-assembly-failed") {
		t.Errorf("resolved = %v, want it to include the cleared issue's problem key", fns.resolved)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// fakeStatusReader is a host client that reports a chosen agent_version (or an
// error) so the locus-C version check can be driven without a real host-agent.
// Satisfies agentStatusReader.
type fakeStatusReader struct {
	version string
	err     error
}

func (f fakeStatusReader) SystemStatus(context.Context) (protocol.SystemStatus, error) {
	if f.err != nil {
		return protocol.SystemStatus{}, f.err
	}
	return protocol.SystemStatus{AgentVersion: f.version}, nil
}

func versionActive(mgr *health.Manager) bool {
	_, ok := mgr.Get("version-mismatch", "")
	return ok
}

// TestCheckAgentVersion_MismatchRaises is the headline behavior (#37 Done-when):
// a reported agent_version that differs from the brain's expected version raises
// version-mismatch and writes exactly one raised audit record for it.
func TestCheckAgentVersion_MismatchRaises(t *testing.T) {
	mgr := health.NewManager(nil)
	fs := &fakeEventStore{}

	checkAgentVersion(context.Background(), fakeStatusReader{version: "9.9.9-other"},
		mgr, audit.New(fs), notify.New(&fakeNotifStore{}, nil))

	if !versionActive(mgr) {
		t.Fatal("want version-mismatch active after a mismatched agent version")
	}
	if len(fs.events) != 1 ||
		fs.events[0].Action != audit.ActionHealthIssueRaised ||
		fs.events[0].TargetID != "version-mismatch" {
		t.Fatalf("want one raised audit record for version-mismatch, got %+v", fs.events)
	}
}

// TestCheckAgentVersion_MatchClears: once raised, a subsequent matching version
// clears version-mismatch and writes the clear audit record.
func TestCheckAgentVersion_MatchClears(t *testing.T) {
	mgr := health.NewManager(nil)
	fs := &fakeEventStore{}
	auditor := audit.New(fs)
	notifier := notify.New(&fakeNotifStore{}, nil)

	checkAgentVersion(context.Background(), fakeStatusReader{version: "9.9.9-other"}, mgr, auditor, notifier)
	if !versionActive(mgr) {
		t.Fatal("setup: want version-mismatch active after a mismatch")
	}
	checkAgentVersion(context.Background(), fakeStatusReader{version: expectedAgentVersion}, mgr, auditor, notifier)
	if versionActive(mgr) {
		t.Fatal("want version-mismatch cleared after a matching version")
	}
	if len(fs.events) != 2 ||
		fs.events[1].Action != audit.ActionHealthIssueCleared ||
		fs.events[1].TargetID != "version-mismatch" {
		t.Fatalf("want a raise then a clear audit record, got %+v", fs.events)
	}
}

// TestCheckAgentVersion_MatchNoIssueIsNoop pins the steady happy path: a matching
// version with no active issue raises nothing and writes no audit row.
func TestCheckAgentVersion_MatchNoIssueIsNoop(t *testing.T) {
	mgr := health.NewManager(nil)
	fs := &fakeEventStore{}

	checkAgentVersion(context.Background(), fakeStatusReader{version: expectedAgentVersion},
		mgr, audit.New(fs), notify.New(&fakeNotifStore{}, nil))

	if versionActive(mgr) {
		t.Error("want no version-mismatch for a matching version")
	}
	if len(fs.events) != 0 {
		t.Errorf("want no audit records for a steady matching version, got %d", len(fs.events))
	}
}

// TestCheckAgentVersion_SteadyMismatchRefreshesWithoutReaudit: a persistent
// mismatch raises once; a second poll refreshes last_checked_at (HEALTH.md
// last-checked-always-fresh) without re-raising or writing a second audit row,
// and leaves raised_at untouched.
func TestCheckAgentVersion_SteadyMismatchRefreshesWithoutReaudit(t *testing.T) {
	mgr := health.NewManager(nil)
	clock := time.Unix(0, 0).UTC()
	mgr.SetClock(func() time.Time { return clock })
	fs := &fakeEventStore{}
	auditor := audit.New(fs)
	notifier := notify.New(&fakeNotifStore{}, nil)
	reader := fakeStatusReader{version: "9.9.9-other"}

	checkAgentVersion(context.Background(), reader, mgr, auditor, notifier)
	first, _ := mgr.Get("version-mismatch", "")

	clock = clock.Add(60 * time.Second)
	checkAgentVersion(context.Background(), reader, mgr, auditor, notifier)
	second, ok := mgr.Get("version-mismatch", "")
	if !ok {
		t.Fatal("want version-mismatch still active on the second poll")
	}
	if len(fs.events) != 1 {
		t.Fatalf("want exactly one raised audit across two mismatch polls, got %d", len(fs.events))
	}
	if !second.LastCheckedAt.After(first.LastCheckedAt) {
		t.Errorf("last_checked_at must advance on a no-transition poll: first %v second %v",
			first.LastCheckedAt, second.LastCheckedAt)
	}
	if !second.RaisedAt.Equal(first.RaisedAt) {
		t.Errorf("raised_at must not move on a re-raise: first %v second %v",
			first.RaisedAt, second.RaisedAt)
	}
}

// TestCheckAgentVersion_UnreachableLeavesStateUnchanged: a poll that can't reach
// host-agent must not clear an active version-mismatch (an error is not a match)
// and must write no audit record.
func TestCheckAgentVersion_UnreachableLeavesStateUnchanged(t *testing.T) {
	mgr := health.NewManager(nil)
	notifier := notify.New(&fakeNotifStore{}, nil)

	checkAgentVersion(context.Background(), fakeStatusReader{version: "9.9.9-other"},
		mgr, audit.New(&fakeEventStore{}), notifier)
	if !versionActive(mgr) {
		t.Fatal("setup: want version-mismatch active")
	}

	fs := &fakeEventStore{}
	checkAgentVersion(context.Background(), fakeStatusReader{err: errors.New("dial unix: connection refused")},
		mgr, audit.New(fs), notifier)
	if !versionActive(mgr) {
		t.Error("an unreachable host-agent must not clear version-mismatch")
	}
	if len(fs.events) != 0 {
		t.Errorf("want no audit records on an unreachable poll, got %d", len(fs.events))
	}
}

// A raised key with no live issue produces no notification. emitHealthNotifications
// looks the issue up via Manager.Get and only notifies when it's still active.
// (This is doubly safe: even without the ok guard, Get returns a zero-value
// Issue{ID:""} on a miss, which notify drops since "" isn't allowlisted — so
// this test pins the observable contract, not the guard in isolation. Issue is
// a value type, so there's no nil-deref to defend against.)
func TestEmitHealthNotifications_NoNotificationForInactiveKey(t *testing.T) {
	mgr := health.NewManager(nil) // empty — Get returns ok=false
	fns := &fakeNotifStore{}
	emitHealthNotifications(notify.New(fns, nil), mgr, []health.IssueKey{{ID: "data-drive-missing"}}, nil)
	if len(fns.raised) != 0 {
		t.Fatalf("want 0 raises for a key with no live issue, got %d", len(fns.raised))
	}
}
