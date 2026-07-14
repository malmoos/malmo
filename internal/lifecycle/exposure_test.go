package lifecycle

// Per-app exposure → Caddy route policy (#306). These lock the "one central route
// builder is the safety boundary" invariant (ENVIRONMENT.md # Public-by-default):
// every hosted app route strips the Cookie header (so the box's Domain-scoped
// forward-auth cookie never reaches an app upstream), a restricted app is
// forward-auth gated, and the appliance route stays the plain reverse_proxy.

import (
	"context"
	"testing"

	"github.com/malmoos/malmo/internal/profile"
	"github.com/malmoos/malmo/internal/store"
)

func installWhoami(t *testing.T, e *testEnv) store.Instance {
	t.Helper()
	e.writeCatalogApp(t, "whoami", whoamiCompose, whoamiManifest(testDigest))
	e.docker.digests[testImage] = testDigest
	inst, err := e.m.Install(context.Background(), "whoami", Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return inst
}

// Appliance: the route must be byte-for-byte the plain reverse_proxy — no strip,
// no gate — and the app is created public.
func TestInstall_Appliance_PlainRoute(t *testing.T) {
	e := newTestEnv(t)
	inst := installWhoami(t, e)

	if row, _ := e.store.Get(inst.ID); row.Exposure != store.ExposurePublic {
		t.Fatalf("appliance exposure = %q, want public", row.Exposure)
	}
	cfg := e.caddy.config(inst.ID)
	if cfg.StripCookie {
		t.Error("appliance route must not strip Cookie")
	}
	if cfg.ForwardAuth != nil {
		t.Error("appliance route must not be forward-auth gated")
	}
}

// Hosted: a fresh install defaults to restricted (the #306 flip), so its route
// strips the Cookie header and is wrapped in the forward_auth gate pointed at the
// brain verify endpoint.
func TestInstall_Hosted_DefaultRestrictedStripsAndGates(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	inst := installWhoami(t, e)

	if row, _ := e.store.Get(inst.ID); row.Exposure != store.ExposureRestricted {
		t.Fatalf("hosted default exposure = %q, want restricted", row.Exposure)
	}
	cfg := e.caddy.config(inst.ID)
	if !cfg.StripCookie {
		t.Fatal("hosted route must strip Cookie so the forward-auth cookie never reaches the app")
	}
	if cfg.ForwardAuth == nil {
		t.Fatal("restricted app must be forward-auth gated")
	}
	fa := cfg.ForwardAuth
	if fa.Upstream != "malmo-brain:8080" {
		t.Errorf("verify upstream = %q, want malmo-brain:8080", fa.Upstream)
	}
	if fa.VerifyPath != profile.ForwardAuthVerifyPath {
		t.Errorf("verify path = %q, want %q", fa.VerifyPath, profile.ForwardAuthVerifyPath)
	}
	if fa.LoginURL != "https://cindy-fox.malmo.network/" {
		t.Errorf("login URL = %q, want the box dashboard root", fa.LoginURL)
	}
	if len(fa.CopyHeaders) == 0 {
		t.Error("expected identity CopyHeaders forwarded to the app on allow")
	}
}

// The load-bearing invariant: NO hosted app route ever forwards Cookie to an app
// upstream. Flipping to public drops the gate but keeps the strip — the
// forward-auth cookie is Domain-scoped to every "<slug>.<box-id>" subdomain, so a
// public app would otherwise receive it.
func TestSetExposure_HostedPublicStillStripsCookie(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	inst := installWhoami(t, e)

	if err := e.m.SetExposure(context.Background(), inst.ID, store.ExposurePublic); err != nil {
		t.Fatalf("set public: %v", err)
	}
	if row, _ := e.store.Get(inst.ID); row.Exposure != store.ExposurePublic {
		t.Fatalf("exposure = %q, want public", row.Exposure)
	}
	cfg := e.caddy.config(inst.ID)
	if !cfg.StripCookie {
		t.Fatal("a public hosted app must STILL strip the Cookie header")
	}
	if cfg.ForwardAuth != nil {
		t.Fatal("a public app must not be forward-auth gated")
	}

	// Flip back to restricted re-applies the gate.
	if err := e.m.SetExposure(context.Background(), inst.ID, store.ExposureRestricted); err != nil {
		t.Fatalf("set restricted: %v", err)
	}
	if cfg := e.caddy.config(inst.ID); cfg.ForwardAuth == nil {
		t.Fatal("flipping back to restricted must re-apply the forward-auth gate")
	}
}

// SetExposure on a stopped app persists desired state but touches no route (a
// stopped app shows only a splash); the change is picked up at the next start.
func TestSetExposure_Stopped_PersistsOnly(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	inst := installWhoami(t, e)
	if err := e.m.Stop(context.Background(), inst.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	e.caddy.calls = nil

	if err := e.m.SetExposure(context.Background(), inst.ID, store.ExposurePublic); err != nil {
		t.Fatalf("set public: %v", err)
	}
	if row, _ := e.store.Get(inst.ID); row.Exposure != store.ExposurePublic {
		t.Fatalf("exposure = %q, want public", row.Exposure)
	}
	if e.caddy.called("AddRoute") {
		t.Error("SetExposure on a stopped app must not re-apply a route")
	}
}

// SetBrainUpstream overrides the forward_auth verify dial; an empty value keeps
// the current one so a misconfiguration can't blank the upstream.
func TestSetBrainUpstream(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	e.m.SetBrainUpstream("brain.internal:9999")
	inst := installWhoami(t, e)
	if fa := e.caddy.config(inst.ID).ForwardAuth; fa == nil || fa.Upstream != "brain.internal:9999" {
		t.Fatalf("forward-auth upstream = %+v, want brain.internal:9999", fa)
	}

	e.m.SetBrainUpstream("") // empty must not blank the override
	if err := e.m.SetExposure(context.Background(), inst.ID, store.ExposureRestricted); err != nil {
		t.Fatalf("set exposure: %v", err)
	}
	if fa := e.caddy.config(inst.ID).ForwardAuth; fa == nil || fa.Upstream != "brain.internal:9999" {
		t.Fatalf("empty SetBrainUpstream blanked the upstream: %+v", fa)
	}
}

func TestSetExposure_MissingInstance(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	if err := e.m.SetExposure(context.Background(), "nope", store.ExposureRestricted); err == nil {
		t.Fatal("SetExposure on a missing instance must error")
	}
}

// A restart (reconcile) must rebuild the route from stored exposure — it must not
// drop the Cookie strip or the forward-auth gate of a restricted hosted app.
func TestReconcile_Hosted_ReassertKeepsGate(t *testing.T) {
	e := newTestEnv(t)
	e.m.SetEnvironment(profile.Hosted, "cindy-fox")
	inst := installWhoami(t, e)

	// Drift: SQLite says running, Docker has no containers — reconcile restarts it.
	e.docker.psManaged = map[string]bool{}
	e.caddy.calls = nil

	if err := e.m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !e.caddy.called("AddRoute") {
		t.Fatalf("reconcile must reassert the route: %v", e.caddy.calls)
	}
	cfg := e.caddy.config(inst.ID)
	if !cfg.StripCookie || cfg.ForwardAuth == nil {
		t.Fatalf("a restart must not drop the gate or strip: %+v", cfg)
	}
}
