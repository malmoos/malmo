package notify

import (
	"errors"
	"testing"
	"time"

	"github.com/malmo/malmo/internal/health"
)

// fakeStore captures Raise/Resolve calls so the derivation logic can be tested
// without SQLite. The coalescing SQL itself is tested in internal/store.
type fakeStore struct {
	raised    []Notification
	resolved  []string // dedup keys
	resolveAt []time.Time
	raiseErr  error
}

func (f *fakeStore) RaiseNotification(n Notification) error {
	if f.raiseErr != nil {
		return f.raiseErr
	}
	f.raised = append(f.raised, n)
	return nil
}

func (f *fakeStore) ResolveNotification(dedupKey string, at time.Time) error {
	f.resolved = append(f.resolved, dedupKey)
	f.resolveAt = append(f.resolveAt, at)
	return nil
}

func issue(id, instanceKey string) health.Issue {
	return health.Issue{
		ID:          id,
		InstanceKey: instanceKey,
		Category:    health.CategoryStorage,
		Severity:    health.SeverityCritical,
		Summary:     "Your data drive isn't connected.",
		Details:     "abc-123 absent",
	}
}

func newNotifier(s NotificationStore) *Notifier {
	n := New(s)
	n.SetClock(func() time.Time { return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) })
	return n
}

func TestHealthRaised_AllowlistedProducesNotification(t *testing.T) {
	fs := &fakeStore{}
	newNotifier(fs).HealthRaised(issue("data-drive-missing", ""))

	if len(fs.raised) != 1 {
		t.Fatalf("want 1 raised notification, got %d", len(fs.raised))
	}
	n := fs.raised[0]
	if n.SourceKind != SourceHealthIssue || n.SourceID != "data-drive-missing" {
		t.Errorf("source = {%q,%q}, want {%q,data-drive-missing}", n.SourceKind, n.SourceID, SourceHealthIssue)
	}
	if n.DedupKey != "health:data-drive-missing" {
		t.Errorf("dedup_key = %q, want health:data-drive-missing", n.DedupKey)
	}
	if n.Category != CategoryStorage {
		t.Errorf("category = %q, want storage", n.Category)
	}
	if n.Severity != SeverityCritical {
		t.Errorf("severity = %q, want critical (copied from issue)", n.Severity)
	}
	if n.Audience != AudienceAdmins || n.Variant != VariantActionable {
		t.Errorf("routing = {%q,%q}, want {admins,actionable}", n.Audience, n.Variant)
	}
	if n.ActionLabel != "Open Storage" || n.ActionRoute != "/settings/storage" {
		t.Errorf("action = {%q,%q}, want {Open Storage,/settings/storage}", n.ActionLabel, n.ActionRoute)
	}
	if n.Summary != "Your data drive isn't connected." || n.Body != "abc-123 absent" {
		t.Errorf("summary/body = {%q,%q}", n.Summary, n.Body)
	}
	if n.TS != time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("ts = %d, want clock value", n.TS)
	}
}

func TestHealthRaised_SystemCategoryIssue(t *testing.T) {
	fs := &fakeStore{}
	newNotifier(fs).HealthRaised(issue("canary-mismatch", ""))
	if len(fs.raised) != 1 {
		t.Fatalf("want 1 notification, got %d", len(fs.raised))
	}
	if fs.raised[0].Category != CategorySystem {
		t.Errorf("category = %q, want system", fs.raised[0].Category)
	}
}

// health-report-malformed is a storage-category issue deliberately omitted from
// the notification allowlist (NOTIFICATIONS.md). A naive "all non-network
// storage issues notify" rule would wrongly include it; the explicit allowlist
// must not.
func TestHealthRaised_NotAllowlisted_NoNotification(t *testing.T) {
	for _, id := range []string{"health-report-malformed", "store-write-failed", "made-up-id"} {
		fs := &fakeStore{}
		newNotifier(fs).HealthRaised(issue(id, ""))
		if len(fs.raised) != 0 {
			t.Errorf("%s: want 0 notifications (not allowlisted), got %d", id, len(fs.raised))
		}
	}
}

func TestHealthRaised_InstanceKeyInDedupKey(t *testing.T) {
	fs := &fakeStore{}
	newNotifier(fs).HealthRaised(issue("data-drive-missing", "inst-abc"))
	if len(fs.raised) != 1 {
		t.Fatalf("want 1 notification, got %d", len(fs.raised))
	}
	if got := fs.raised[0].DedupKey; got != "health:data-drive-missing:inst-abc" {
		t.Errorf("dedup_key = %q, want health:data-drive-missing:inst-abc", got)
	}
}

func TestHealthCleared_AllowlistedResolves(t *testing.T) {
	fs := &fakeStore{}
	newNotifier(fs).HealthCleared("data-drive-missing", "")
	if len(fs.resolved) != 1 || fs.resolved[0] != "health:data-drive-missing" {
		t.Fatalf("resolved = %v, want [health:data-drive-missing]", fs.resolved)
	}
	if fs.resolveAt[0] != time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC) {
		t.Errorf("resolve at = %v, want clock value", fs.resolveAt[0])
	}
}

func TestHealthCleared_NotAllowlisted_NoResolve(t *testing.T) {
	fs := &fakeStore{}
	newNotifier(fs).HealthCleared("health-report-malformed", "")
	if len(fs.resolved) != 0 {
		t.Fatalf("want 0 resolve calls for non-allowlisted issue, got %d", len(fs.resolved))
	}
}

// A store error must be swallowed (logged, not propagated): the bell is a
// floor, never a gate (NOTIFICATIONS.md). HealthRaised returns nothing, so we
// just assert it doesn't panic and the fake recorded no successful raise.
func TestHealthRaised_StoreErrorSwallowed(t *testing.T) {
	fs := &fakeStore{raiseErr: errors.New("db down")}
	newNotifier(fs).HealthRaised(issue("data-drive-missing", "")) // must not panic
	if len(fs.raised) != 0 {
		t.Fatalf("raise should have errored before capture, got %d", len(fs.raised))
	}
}
