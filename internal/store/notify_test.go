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

// --- read surface (slice 0026) -------------------------------------------

func seedUser(t *testing.T, s *Store, id, role string) {
	t.Helper()
	if err := s.CreateUser(User{ID: id, Username: id, Role: role, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
}

// userNotification builds a personal (audience=user) notification owned by
// userID — the shape a future app/account source produces.
func userNotification(dedupKey, userID string) notify.Notification {
	n := newNotification(dedupKey)
	n.Audience = notify.AudienceUser
	n.UserID = userID
	return n
}

// raiseAndID raises n and returns the active row's id (the read surface needs
// it to mark-read/dismiss a specific notification).
func raiseAndID(t *testing.T, s *Store, n notify.Notification) int64 {
	t.Helper()
	if err := s.RaiseNotification(n); err != nil {
		t.Fatalf("raise %s: %v", n.DedupKey, err)
	}
	var id int64
	if err := s.db.QueryRow(
		`SELECT id FROM notifications WHERE dedup_key=? AND dismissed_at IS NULL`,
		n.DedupKey).Scan(&id); err != nil {
		t.Fatalf("lookup %s: %v", n.DedupKey, err)
	}
	return id
}

func TestListNotificationsForRecipient_AudienceScoping(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	seedUser(t, s, "u_mem", RoleMember)
	seedUser(t, s, "u_other", RoleMember)

	// A box-wide (admins) notification, a personal one for u_mem, and a personal
	// one owned by the admin.
	if err := s.RaiseNotification(newNotification("health:data-drive-missing")); err != nil {
		t.Fatalf("raise admins: %v", err)
	}
	if err := s.RaiseNotification(userNotification("app:foo:u_mem", "u_mem")); err != nil {
		t.Fatalf("raise user: %v", err)
	}
	if err := s.RaiseNotification(userNotification("app:bar:u_admin", "u_admin")); err != nil {
		t.Fatalf("raise admin-own: %v", err)
	}

	// Admin sees the box-wide row + their own personal row, NOT u_mem's.
	admin, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true})
	if err != nil {
		t.Fatalf("list admin: %v", err)
	}
	if !dedupSet(admin, "health:data-drive-missing", "app:bar:u_admin") {
		t.Errorf("admin sees %v, want {health:data-drive-missing, app:bar:u_admin}", dedups(admin))
	}

	// Member sees only their own personal row — never the box-wide one.
	mem, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_mem", IsAdmin: false})
	if err != nil {
		t.Fatalf("list member: %v", err)
	}
	if !dedupSet(mem, "app:foo:u_mem") {
		t.Errorf("member sees %v, want {app:foo:u_mem}", dedups(mem))
	}

	// An unrelated member sees nothing.
	other, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_other", IsAdmin: false})
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("unrelated member sees %v, want none", dedups(other))
	}
}

// The 'members' broadcast audience (the transparency variant, slice 0028) is
// visible to every member and to no admin — the mirror of the 'admins'
// audience. Admins receive the actionable copy instead, so they must not also
// see the member transparency row.
func TestListNotificationsForRecipient_MembersAudience(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	seedUser(t, s, "u_mem", RoleMember)

	box := newNotification("health:data-drive-missing")
	if err := s.RaiseNotification(box); err != nil {
		t.Fatalf("raise admins: %v", err)
	}
	members := newNotification("health:data-drive-missing:member")
	members.Audience = notify.AudienceMembers
	members.Variant = notify.VariantTransparency
	if err := s.RaiseNotification(members); err != nil {
		t.Fatalf("raise members: %v", err)
	}

	// Member sees the broadcast members row, never the admins row.
	mem, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_mem", IsAdmin: false})
	if err != nil {
		t.Fatalf("list member: %v", err)
	}
	if !dedupSet(mem, "health:data-drive-missing:member") {
		t.Errorf("member sees %v, want {health:data-drive-missing:member}", dedups(mem))
	}

	// Admin sees the admins row, never the members transparency row.
	admin, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true})
	if err != nil {
		t.Fatalf("list admin: %v", err)
	}
	if !dedupSet(admin, "health:data-drive-missing") {
		t.Errorf("admin sees %v, want {health:data-drive-missing} only (not the member row)", dedups(admin))
	}

	// The member's badge counts the broadcast row.
	if c, _ := s.CountUnreadNotifications("u_mem", false); c != 1 {
		t.Errorf("member unread = %d, want 1 (the members broadcast)", c)
	}
}

func TestListNotificationsForRecipient_ExcludesDismissed(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	id := raiseAndID(t, s, newNotification("health:data-drive-missing"))

	if err := s.DismissNotification(id, "u_admin", time.UnixMilli(1000)); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	def, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(def) != 0 {
		t.Errorf("default list shows dismissed row: %v", dedups(def))
	}

	inc, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true, IncludeDismissed: true})
	if err != nil {
		t.Fatalf("list include-dismissed: %v", err)
	}
	if len(inc) != 1 {
		t.Errorf("include-dismissed list = %d rows, want 1", len(inc))
	}
}

func TestListNotificationsForRecipient_Cursor(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	for _, k := range []string{"health:a", "health:b", "health:c"} {
		if err := s.RaiseNotification(newNotification(k)); err != nil {
			t.Fatalf("raise %s: %v", k, err)
		}
	}

	page1, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true, Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 = %d rows, want 2", len(page1))
	}
	// Newest-first: ids descending.
	if page1[0].ID <= page1[1].ID {
		t.Errorf("page1 not newest-first: %d then %d", page1[0].ID, page1[1].ID)
	}

	page2, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true, Limit: 2, AfterID: page1[1].ID})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 = %d rows, want 1 (the remaining oldest)", len(page2))
	}
	if page2[0].ID >= page1[1].ID {
		t.Errorf("cursor leaked: page2 id %d not < %d", page2[0].ID, page1[1].ID)
	}
}

func TestCountUnreadNotifications(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	id1 := raiseAndID(t, s, newNotification("health:a"))
	_ = raiseAndID(t, s, newNotification("health:b"))

	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 2 {
		t.Fatalf("initial unread = %d, want 2", c)
	}

	if err := s.MarkNotificationRead(id1, "u_admin", time.UnixMilli(1000)); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 1 {
		t.Errorf("after one read, unread = %d, want 1", c)
	}

	// A member with no visible notifications has a zero badge (no admins access).
	if c, _ := s.CountUnreadNotifications("u_mem_absent", false); c != 0 {
		t.Errorf("member with no rows: unread = %d, want 0", c)
	}
}

func TestMarkNotificationRead_PreservesFirstRead(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	id := raiseAndID(t, s, newNotification("health:a"))

	if err := s.MarkNotificationRead(id, "u_admin", time.UnixMilli(1000)); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if err := s.MarkNotificationRead(id, "u_admin", time.UnixMilli(5000)); err != nil {
		t.Fatalf("second read: %v", err)
	}

	got, err := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ReadAt != 1000 {
		t.Fatalf("read_at = %v, want first-read 1000 preserved", got)
	}
}

// Dismiss is per-recipient: one admin dismissing a box-wide notification does
// not remove it from another admin's inbox (NOTIFICATIONS.md # Read/unread).
func TestDismissNotification_PerRecipient(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin1", RoleAdmin)
	seedUser(t, s, "u_admin2", RoleAdmin)
	id := raiseAndID(t, s, newNotification("health:data-drive-missing"))

	if err := s.DismissNotification(id, "u_admin1", time.UnixMilli(1000)); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	one, _ := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin1", IsAdmin: true})
	if len(one) != 0 {
		t.Errorf("admin1 still sees dismissed row: %v", dedups(one))
	}
	two, _ := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin2", IsAdmin: true})
	if len(two) != 1 {
		t.Errorf("admin2 lost the row to admin1's dismiss: %v", dedups(two))
	}
	if c, _ := s.CountUnreadNotifications("u_admin2", true); c != 1 {
		t.Errorf("admin2 badge = %d, want 1 (unaffected)", c)
	}
}

func TestMarkAllNotificationsRead(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	for _, k := range []string{"health:a", "health:b", "health:c"} {
		if err := s.RaiseNotification(newNotification(k)); err != nil {
			t.Fatalf("raise %s: %v", k, err)
		}
	}

	if err := s.MarkAllNotificationsRead("u_admin", true, time.UnixMilli(2000)); err != nil {
		t.Fatalf("mark all: %v", err)
	}
	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 0 {
		t.Errorf("unread after mark-all = %d, want 0", c)
	}
	// Read, not dismissed: the rows stay in the inbox, just no longer unread.
	got, _ := s.ListNotificationsForRecipient(NotificationFilter{UserID: "u_admin", IsAdmin: true})
	if len(got) != 3 {
		t.Fatalf("list after mark-all = %d rows, want 3 (read ≠ removed)", len(got))
	}
	for _, n := range got {
		if n.ReadAt == 0 {
			t.Errorf("%s still unread after mark-all", n.DedupKey)
		}
	}
}

// Mark-all-read goes through the same visibility clause as list/count, so a
// member's mark-all must reach their broadcast 'members' rows (and their own
// 'user' rows) while never touching 'admins' rows. Symmetry guard for the
// member branch of the clause across all three read queries (slice 0028).
func TestMarkAllNotificationsRead_MemberAudience(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	seedUser(t, s, "u_mem", RoleMember)

	members := newNotification("health:data-drive-missing:member")
	members.Audience = notify.AudienceMembers
	if err := s.RaiseNotification(members); err != nil {
		t.Fatalf("raise members: %v", err)
	}
	own := userNotification("app:foo:u_mem", "u_mem")
	if err := s.RaiseNotification(own); err != nil {
		t.Fatalf("raise user: %v", err)
	}
	// An admins row the member must not be able to read away.
	if err := s.RaiseNotification(newNotification("health:canary-mismatch")); err != nil {
		t.Fatalf("raise admins: %v", err)
	}

	if c, _ := s.CountUnreadNotifications("u_mem", false); c != 2 {
		t.Fatalf("member unread before mark-all = %d, want 2", c)
	}
	if err := s.MarkAllNotificationsRead("u_mem", false, time.UnixMilli(2000)); err != nil {
		t.Fatalf("mark all: %v", err)
	}
	if c, _ := s.CountUnreadNotifications("u_mem", false); c != 0 {
		t.Errorf("member unread after mark-all = %d, want 0 (members + own rows read)", c)
	}
	// The admin still sees the admins row as unread — the member's mark-all did
	// not reach it.
	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 1 {
		t.Errorf("admin unread = %d, want 1 (untouched by member's mark-all)", c)
	}
}

// A re-raise (coalesce) is a fresh occurrence and must re-surface: the prior
// per-recipient read state is cleared so the badge lights up again.
func TestRaiseNotification_ReRaiseClearsReadState(t *testing.T) {
	s := open(t)
	seedUser(t, s, "u_admin", RoleAdmin)
	id := raiseAndID(t, s, newNotification("health:data-drive-missing"))

	if err := s.MarkNotificationRead(id, "u_admin", time.UnixMilli(1000)); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 0 {
		t.Fatalf("after read, unread = %d, want 0", c)
	}

	// Issue clears, then flaps back: re-raise coalesces onto the same row.
	if err := s.ResolveNotification("health:data-drive-missing", time.UnixMilli(1500)); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := s.RaiseNotification(newNotification("health:data-drive-missing")); err != nil {
		t.Fatalf("re-raise: %v", err)
	}

	if c, _ := s.CountUnreadNotifications("u_admin", true); c != 1 {
		t.Errorf("after re-raise, unread = %d, want 1 (re-surfaced)", c)
	}
}

func TestGetNotification_NotFound(t *testing.T) {
	s := open(t)
	if _, err := s.GetNotification(999); err != ErrNotFound {
		t.Fatalf("GetNotification(missing) err = %v, want ErrNotFound", err)
	}
}

// --- small assertions helpers ---

func dedups(ns []notify.Notification) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.DedupKey
	}
	return out
}

func dedupSet(ns []notify.Notification, want ...string) bool {
	if len(ns) != len(want) {
		return false
	}
	have := map[string]bool{}
	for _, n := range ns {
		have[n.DedupKey] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}
