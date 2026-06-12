package lifecycle

// Stop/Start scenarios (APP_LIFECYCLE.md # stop, start, uninstall). Each installs
// a real instance through the happy path, then drives Stop/Start against the
// fakes and asserts the persisted state, the Caddy route variant, and the guard
// errors for illegal transitions.

import (
	"context"
	"errors"
	"testing"

	"github.com/malmoos/malmo/internal/store"
)

// install a running whoami instance for the stop/start tests to act on.
func installRunning(t *testing.T, e *testEnv) store.Instance {
	t.Helper()
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.digests[testImage] = testDigest
	inst, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return inst
}

func TestStopThenStart(t *testing.T) {
	e := newTestEnv(t)
	inst := installRunning(t, e)

	// Stop: compose stop, state stopped, route flips to the stopped splash.
	if err := e.m.Stop(context.Background(), inst.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !e.docker.called("ComposeStop") {
		t.Fatalf("ComposeStop not called")
	}
	if row, _ := e.store.Get(inst.ID); row.State != "stopped" {
		t.Fatalf("state after stop = %q, want stopped", row.State)
	}
	if got := e.caddy.route(inst.ID); got != "splash:stopped" {
		t.Fatalf("route after stop = %q, want splash:stopped", got)
	}

	// Start: compose up, healthy, route flips back to the real upstream.
	if err := e.m.Start(context.Background(), inst.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if row, _ := e.store.Get(inst.ID); row.State != "running" {
		t.Fatalf("state after start = %q, want running", row.State)
	}
	if got := e.caddy.route(inst.ID); len(got) < 9 || got[:9] != "upstream:" {
		t.Fatalf("route after start = %q, want upstream:…", got)
	}
}

func TestStopGuardRejectsNonRunning(t *testing.T) {
	e := newTestEnv(t)
	inst := installRunning(t, e)
	if err := e.m.Stop(context.Background(), inst.ID); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	// Stopping an already-stopped app is an illegal transition.
	err := e.m.Stop(context.Background(), inst.ID)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second stop err = %v, want ErrNotRunning", err)
	}
}

func TestStartGuardRejectsNonStopped(t *testing.T) {
	e := newTestEnv(t)
	inst := installRunning(t, e)
	// The instance is running, so Start must reject.
	err := e.m.Start(context.Background(), inst.ID)
	if !errors.Is(err, ErrNotStopped) {
		t.Fatalf("start err = %v, want ErrNotStopped", err)
	}
}

func TestStartHealthFailureMarksFailed(t *testing.T) {
	e := newTestEnv(t)
	inst := installRunning(t, e)
	if err := e.m.Stop(context.Background(), inst.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// main_service never goes healthy on the way back up.
	e.docker.inspect = func(_, _ string) (bool, string, error) {
		return true, "unhealthy", nil
	}
	if err := e.m.Start(context.Background(), inst.ID); err == nil {
		t.Fatalf("start: expected health failure error")
	}
	if row, _ := e.store.Get(inst.ID); row.State != "failed" {
		t.Fatalf("state after failed start = %q, want failed", row.State)
	}
	if got := e.caddy.route(inst.ID); got != "splash:failed" {
		t.Fatalf("route after failed start = %q, want splash:failed", got)
	}
}
