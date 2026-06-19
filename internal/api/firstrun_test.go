package api

import (
	"net/http"
	"testing"

	"github.com/malmoos/malmo/internal/profile"
)

// --- auth/state profile + first-run-complete probe -----------------------

// TestAuthState_ProfileField: the public bootstrap probe surfaces the resolved
// environment profile so the wizard can pick its profile-aware step set. The
// default (zero-valued env) is appliance; a hosted box reports hosted.
func TestAuthState_ProfileField(t *testing.T) {
	appliance := newHarness(t)
	body := decodeJSON[authStateBody](t, appliance.do("GET", "/api/v1/auth/state", nil))
	if body.Profile != string(profile.Appliance) {
		t.Errorf("appliance auth/state profile = %q; want %q", body.Profile, profile.Appliance)
	}

	hosted := hostedHarness(t)
	body = decodeJSON[authStateBody](t, hosted.do("GET", "/api/v1/auth/state", nil))
	if body.Profile != string(profile.Hosted) {
		t.Errorf("hosted auth/state profile = %q; want %q", body.Profile, profile.Hosted)
	}
}

// TestAuthState_FirstRunCompleteProgression: first_run_complete is false until
// the wizard latches it at Done — distinct from has_users, which flips at the
// admin step. The probe stays public throughout (it gates pre-auth routing).
func TestAuthState_FirstRunCompleteProgression(t *testing.T) {
	h := newHarness(t)

	body := decodeJSON[authStateBody](t, h.do("GET", "/api/v1/auth/state", nil))
	if body.FirstRunComplete {
		t.Fatal("fresh box reported first_run_complete=true")
	}

	// Creating the admin must NOT flip first_run_complete — the wizard is still
	// running (time zone, telemetry, Done remain).
	h.setupAdmin("alice", "pass1")
	body = decodeJSON[authStateBody](t, h.do("GET", "/api/v1/auth/state", nil))
	if !body.HasUsers {
		t.Fatal("after setup, has_users=false")
	}
	if body.FirstRunComplete {
		t.Fatal("first_run_complete flipped at the admin step; want false until Done")
	}

	// Done step latches it.
	if resp := h.do("POST", "/api/v1/first-run/complete", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete-first-run: want 204, got %d", resp.StatusCode)
	}
	body = decodeJSON[authStateBody](t, h.do("GET", "/api/v1/auth/state", nil))
	if !body.FirstRunComplete {
		t.Fatal("first_run_complete=false after Done")
	}
}

// --- telemetry consent ---------------------------------------------------

// TestSetTelemetryConsent_AdminPersists: an admin's opt-in/opt-out round-trips
// to the store (the future telemetry client gates on it). Off by default.
func TestSetTelemetryConsent_AdminPersists(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	if got, _ := h.st.TelemetryConsent(); got {
		t.Fatal("consent defaulted on; want off")
	}

	if resp := h.do("POST", "/api/v1/telemetry/consent", map[string]bool{"enabled": true}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("opt-in: want 204, got %d", resp.StatusCode)
	}
	if got, _ := h.st.TelemetryConsent(); !got {
		t.Fatal("consent not persisted after opt-in")
	}

	if resp := h.do("POST", "/api/v1/telemetry/consent", map[string]bool{"enabled": false}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("opt-out: want 204, got %d", resp.StatusCode)
	}
	if got, _ := h.st.TelemetryConsent(); got {
		t.Fatal("consent not cleared after opt-out")
	}
}

// TestSetTelemetryConsent_MemberForbidden: box-wide consent is admin-only.
func TestSetTelemetryConsent_MemberForbidden(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob001", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("POST", "/api/v1/telemetry/consent", map[string]bool{"enabled": true})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member telemetry consent: want 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSetTelemetryConsent_Unauthenticated: no session is a 401 (not public).
func TestSetTelemetryConsent_Unauthenticated(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/api/v1/telemetry/consent", map[string]bool{"enabled": true})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated telemetry consent: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- first-run-complete marker -------------------------------------------

// TestCompleteFirstRun_AdminLatchesIdempotently: the Done step latches the
// marker and is safe to call twice (a reloaded wizard re-POSTs).
func TestCompleteFirstRun_AdminLatchesIdempotently(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	for i := 0; i < 2; i++ {
		resp := h.do("POST", "/api/v1/first-run/complete", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("complete #%d: want 204, got %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if got, _ := h.st.FirstRunComplete(); !got {
		t.Fatal("first_run_complete not latched")
	}
}

// TestCompleteFirstRun_MemberForbidden: the marker is admin-only.
func TestCompleteFirstRun_MemberForbidden(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob001", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("POST", "/api/v1/first-run/complete", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member complete-first-run: want 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestCompleteFirstRun_Unauthenticated: no session is a 401 (not public).
func TestCompleteFirstRun_Unauthenticated(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/api/v1/first-run/complete", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated complete-first-run: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
