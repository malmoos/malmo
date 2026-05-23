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
