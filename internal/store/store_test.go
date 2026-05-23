package store

import (
	"path/filepath"
	"testing"
	"time"
)

// open returns a fresh Store backed by a tmp-dir SQLite file. Each test gets
// its own DB; modernc.org/sqlite is fast enough that this beats sharing.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "malmo.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sample(id, slug string) Instance {
	return Instance{
		ID: id, ManifestID: "whoami", Name: "Whoami", Slug: slug,
		Version: "1.10", State: "installing",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malmo.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := s1.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Second open runs migrate again on a populated DB; must not error or
	// truncate data.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Get("a"); err != nil {
		t.Fatalf("row lost across reopen: %v", err)
	}
}

func TestCreateGetListDelete(t *testing.T) {
	s := open(t)
	if _, err := s.Get("missing"); err != ErrNotFound {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := s.Create(sample("b", "beta")); err != nil {
		t.Fatalf("create b: %v", err)
	}
	got, err := s.Get("a")
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if got.Slug != "alpha" || got.State != "installing" {
		t.Fatalf("get a = %+v", got)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	if _, err := s.Get("a"); err != ErrNotFound {
		t.Fatalf("Get(a) after delete = %v, want ErrNotFound", err)
	}
}

func TestSetStateOnMissingInstanceErrors(t *testing.T) {
	s := open(t)
	if err := s.SetState("nope", "running"); err != ErrNotFound {
		t.Fatalf("SetState(missing) = %v, want ErrNotFound", err)
	}
}

func TestSlugTaken(t *testing.T) {
	s := open(t)
	taken, err := s.SlugTaken("alpha")
	if err != nil || taken {
		t.Fatalf("SlugTaken(empty)=%v,%v", taken, err)
	}
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	taken, err = s.SlugTaken("alpha")
	if err != nil || !taken {
		t.Fatalf("SlugTaken(alpha)=%v,%v", taken, err)
	}
}

func TestSetInstanceImagesReplacesAtomically(t *testing.T) {
	s := open(t)
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	first := []InstanceImage{
		{Service: "web", Image: "nginx:1", Digest: "sha256:aaa"},
		{Service: "db", Image: "postgres:16", Digest: "sha256:bbb"},
	}
	if err := s.SetInstanceImages("a", first); err != nil {
		t.Fatalf("set first: %v", err)
	}
	// Replace with a smaller set — old rows must disappear.
	second := []InstanceImage{
		{Service: "web", Image: "nginx:2", Digest: "sha256:ccc"},
	}
	if err := s.SetInstanceImages("a", second); err != nil {
		t.Fatalf("set second: %v", err)
	}
	got, err := s.GetInstanceImages("a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 || got[0].Service != "web" || got[0].Image != "nginx:2" {
		t.Fatalf("after replace got %+v", got)
	}
}

func TestGetInstanceImagesOrderedByService(t *testing.T) {
	s := open(t)
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Insert out of order; expect sorted by service.
	if err := s.SetInstanceImages("a", []InstanceImage{
		{Service: "zeta", Image: "z", Digest: "sha256:z"},
		{Service: "alpha", Image: "a", Digest: "sha256:a"},
		{Service: "mike", Image: "m", Digest: "sha256:m"},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetInstanceImages("a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, w := range want {
		if got[i].Service != w {
			t.Fatalf("svc[%d]=%q want %q (full: %+v)", i, got[i].Service, w, got)
		}
	}
}

func TestDeleteCascadesToInstanceImages(t *testing.T) {
	s := open(t)
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetInstanceImages("a", []InstanceImage{
		{Service: "web", Image: "nginx", Digest: "sha256:x"},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := s.GetInstanceImages("a")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FK cascade failed: %+v", got)
	}
}

func sampleUser(id, username, role string) User {
	return User{
		ID: id, Username: username, Role: role,
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func TestUserCRUD(t *testing.T) {
	s := open(t)
	if has, err := s.HasAnyUser(); err != nil || has {
		t.Fatalf("HasAnyUser on fresh store = %v, %v; want false, nil", has, err)
	}
	if err := s.CreateUser(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if has, _ := s.HasAnyUser(); !has {
		t.Fatal("HasAnyUser after insert = false")
	}

	got, err := s.GetUser("u1")
	if err != nil || got.Username != "andrei" || got.Role != RoleAdmin {
		t.Fatalf("GetUser = %+v, %v", got, err)
	}
	if got, err := s.GetUserByUsername("andrei"); err != nil || got.ID != "u1" {
		t.Fatalf("GetUserByUsername = %+v, %v", got, err)
	}

	if _, err := s.GetUser("missing"); err != ErrNotFound {
		t.Fatalf("GetUser(missing) = %v, want ErrNotFound", err)
	}

	// Duplicate username -> ErrConflict.
	if err := s.CreateUser(sampleUser("u2", "andrei", RoleMember)); err != ErrConflict {
		t.Fatalf("CreateUser dup = %v, want ErrConflict", err)
	}

	// Invalid role rejected by CHECK constraint.
	if err := s.CreateUser(sampleUser("u3", "weirdo", "superuser")); err == nil {
		t.Fatal("CreateUser with bogus role = nil; want CHECK error")
	}

	if err := s.DeleteUser("u1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := s.DeleteUser("u1"); err != ErrNotFound {
		t.Fatalf("DeleteUser(missing) = %v, want ErrNotFound", err)
	}
}

func TestCreateFirstAdmin(t *testing.T) {
	s := open(t)
	if err := s.CreateFirstAdmin(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("first admin: %v", err)
	}
	// Second call must lose: someone already bootstrapped.
	if err := s.CreateFirstAdmin(sampleUser("u2", "cindy", RoleAdmin)); err != ErrConflict {
		t.Fatalf("second first-admin = %v, want ErrConflict", err)
	}
	// Role enforcement: member is not a "first admin".
	s2 := open(t)
	if err := s2.CreateFirstAdmin(sampleUser("u1", "andrei", RoleMember)); err == nil {
		t.Fatal("CreateFirstAdmin with member role = nil; want error")
	}
}

func TestSessionCRUD(t *testing.T) {
	s := open(t)
	if err := s.CreateUser(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Unix(1_700_000_100, 0)
	sess := Session{Token: "tok-1", UserID: "u1", CreatedAt: now, LastSeenAt: now}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := s.GetSession("tok-1")
	if err != nil || got.UserID != "u1" || !got.CreatedAt.Equal(now) {
		t.Fatalf("GetSession = %+v, %v", got, err)
	}

	later := now.Add(5 * time.Minute)
	if err := s.TouchSession("tok-1", later); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	got, _ = s.GetSession("tok-1")
	if !got.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen_at = %v, want %v", got.LastSeenAt, later)
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("created_at moved on touch: %v", got.CreatedAt)
	}

	if _, err := s.GetSession("ghost"); err != ErrNotFound {
		t.Fatalf("GetSession(ghost) = %v, want ErrNotFound", err)
	}

	if err := s.DeleteSession("tok-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession("tok-1"); err != ErrNotFound {
		t.Fatalf("after DeleteSession: %v", err)
	}
}

func TestDeleteUserCascadesSessions(t *testing.T) {
	s := open(t)
	if err := s.CreateUser(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Unix(1_700_000_100, 0)
	for _, tok := range []string{"t1", "t2", "t3"} {
		if err := s.CreateSession(Session{Token: tok, UserID: "u1", CreatedAt: now, LastSeenAt: now}); err != nil {
			t.Fatalf("CreateSession %s: %v", tok, err)
		}
	}
	if err := s.DeleteUser("u1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	for _, tok := range []string{"t1", "t2", "t3"} {
		if _, err := s.GetSession(tok); err != ErrNotFound {
			t.Fatalf("session %s survived user delete (err=%v); FK cascade broken", tok, err)
		}
	}
}

func TestDeleteSessionsForUser(t *testing.T) {
	s := open(t)
	if err := s.CreateUser(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("CreateUser u1: %v", err)
	}
	if err := s.CreateUser(sampleUser("u2", "cindy", RoleMember)); err != nil {
		t.Fatalf("CreateUser u2: %v", err)
	}
	now := time.Unix(1_700_000_100, 0)
	_ = s.CreateSession(Session{Token: "a1", UserID: "u1", CreatedAt: now, LastSeenAt: now})
	_ = s.CreateSession(Session{Token: "a2", UserID: "u1", CreatedAt: now, LastSeenAt: now})
	_ = s.CreateSession(Session{Token: "b1", UserID: "u2", CreatedAt: now, LastSeenAt: now})

	if err := s.DeleteSessionsForUser("u1"); err != nil {
		t.Fatalf("DeleteSessionsForUser: %v", err)
	}
	if _, err := s.GetSession("a1"); err != ErrNotFound {
		t.Fatalf("u1 session a1 = %v; want ErrNotFound", err)
	}
	if _, err := s.GetSession("b1"); err != nil {
		t.Fatalf("u2 session b1 collateral damage: %v", err)
	}
}

func TestSetMDNSName(t *testing.T) {
	s := open(t)
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetMDNSName("a", "alpha.local"); err != nil {
		t.Fatalf("set mdns: %v", err)
	}
	got, _ := s.Get("a")
	if got.MDNSName != "alpha.local" {
		t.Fatalf("mdns = %q", got.MDNSName)
	}
}
