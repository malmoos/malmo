package store

import (
	"testing"
	"time"

	"github.com/malmo/malmo/internal/notify"
)

func newNotification(dedupKey string) notify.Notification {
	return notify.Notification{
		TS:          time.Now().UTC().UnixMilli(),
		Category:    notify.CategoryStorage,
		Severity:    notify.SeverityCritical,
		SourceKind:  notify.SourceHealthIssue,
		SourceID:    "data-drive-missing",
		DedupKey:    dedupKey,
		Audience:    notify.AudienceAdmins,
		Variant:     notify.VariantActionable,
		Summary:     "Your data drive isn't connected.",
		Body:        "abc-123 absent",
		ActionLabel: "Open Storage",
		ActionRoute: "/settings/storage",
	}
}

func TestRaiseNotification_CreatesRow(t *testing.T) {
	s := open(t)
	if err := s.RaiseNotification(newNotification("health:data-drive-missing")); err != nil {
		t.Fatalf("raise: %v", err)
	}
	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 notification, got %d", len(got))
	}
	n := got[0]
	if n.SourceID != "data-drive-missing" || n.DedupKey != "health:data-drive-missing" {
		t.Errorf("source/dedup = {%q,%q}", n.SourceID, n.DedupKey)
	}
	if n.Category != notify.CategoryStorage || n.Severity != notify.SeverityCritical {
		t.Errorf("category/severity = {%q,%q}", n.Category, n.Severity)
	}
	if n.Audience != notify.AudienceAdmins || n.Variant != notify.VariantActionable {
		t.Errorf("routing = {%q,%q}", n.Audience, n.Variant)
	}
	if n.ActionLabel != "Open Storage" || n.ActionRoute != "/settings/storage" {
		t.Errorf("action = {%q,%q}", n.ActionLabel, n.ActionRoute)
	}
	if n.ResolvedAt != 0 {
		t.Errorf("resolved_at = %d, want 0 (active)", n.ResolvedAt)
	}
}

// Re-raising the same dedup_key coalesces: one row, refreshed in place — not a
// second row (NOTIFICATIONS.md # One notification per raise).
func TestRaiseNotification_CoalescesByDedupKey(t *testing.T) {
	s := open(t)
	first := newNotification("health:data-drive-missing")
	first.TS = 1000
	first.Body = "first"
	if err := s.RaiseNotification(first); err != nil {
		t.Fatalf("first raise: %v", err)
	}
	// Vary every column the coalescing UPDATE refreshes — not just ts/body — so
	// a regression that dropped severity/summary/action from the UPDATE clause
	// is caught (with identical values it would be invisible).
	second := newNotification("health:data-drive-missing")
	second.TS = 2000
	second.Body = "second"
	second.Severity = notify.SeverityError
	second.Summary = "refreshed summary"
	second.ActionLabel = "Retry"
	second.ActionRoute = "/settings/storage/retry"
	if err := s.RaiseNotification(second); err != nil {
		t.Fatalf("second raise: %v", err)
	}

	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 coalesced row, got %d", len(got))
	}
	n := got[0]
	if n.TS != 2000 || n.Body != "second" {
		t.Errorf("ts/body not refreshed: ts=%d body=%q, want 2000/second", n.TS, n.Body)
	}
	if n.Severity != notify.SeverityError || n.Summary != "refreshed summary" {
		t.Errorf("severity/summary not refreshed: {%q,%q}, want {error,refreshed summary}", n.Severity, n.Summary)
	}
	if n.ActionLabel != "Retry" || n.ActionRoute != "/settings/storage/retry" {
		t.Errorf("action not refreshed: {%q,%q}, want {Retry,/settings/storage/retry}", n.ActionLabel, n.ActionRoute)
	}
}

func TestRaiseNotification_DistinctKeysDistinctRows(t *testing.T) {
	s := open(t)
	if err := s.RaiseNotification(newNotification("health:data-drive-missing")); err != nil {
		t.Fatalf("raise 1: %v", err)
	}
	if err := s.RaiseNotification(newNotification("health:canary-mismatch")); err != nil {
		t.Fatalf("raise 2: %v", err)
	}
	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows for 2 dedup keys, got %d", len(got))
	}
}

func TestResolveNotification_SetsResolvedAt(t *testing.T) {
	s := open(t)
	if err := s.RaiseNotification(newNotification("health:data-drive-missing")); err != nil {
		t.Fatalf("raise: %v", err)
	}
	at := time.Date(2026, 5, 29, 15, 0, 0, 0, time.UTC)
	if err := s.ResolveNotification("health:data-drive-missing", at); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row (resolved, not deleted), got %d", len(got))
	}
	if got[0].ResolvedAt != at.UnixMilli() {
		t.Errorf("resolved_at = %d, want %d", got[0].ResolvedAt, at.UnixMilli())
	}
}

func TestResolveNotification_MissingKeyNoOp(t *testing.T) {
	s := open(t)
	if err := s.ResolveNotification("health:does-not-exist", time.Now()); err != nil {
		t.Fatalf("resolve of missing key should be a no-op, got: %v", err)
	}
	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 rows, got %d", len(got))
	}
}

// A re-raise after a resolve un-resolves the existing row (flap) — still one
// row, resolved_at cleared, ts bumped. This is the cross-restart / drive-flap
// path the dedup_key defends.
func TestRaiseNotification_ReRaiseAfterResolveUnresolves(t *testing.T) {
	s := open(t)
	n := newNotification("health:data-drive-missing")
	n.TS = 1000
	if err := s.RaiseNotification(n); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if err := s.ResolveNotification("health:data-drive-missing", time.UnixMilli(1500)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	reraise := newNotification("health:data-drive-missing")
	reraise.TS = 2000
	if err := s.RaiseNotification(reraise); err != nil {
		t.Fatalf("re-raise: %v", err)
	}
	got, err := s.ListNotifications()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row after flap, got %d", len(got))
	}
	if got[0].ResolvedAt != 0 {
		t.Errorf("resolved_at = %d, want 0 (un-resolved by re-raise)", got[0].ResolvedAt)
	}
	if got[0].TS != 2000 {
		t.Errorf("ts = %d, want 2000 (bumped by re-raise)", got[0].TS)
	}
}
