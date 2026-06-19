package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/malmoos/malmo/internal/audit"
	"github.com/malmoos/malmo/internal/profile"
	"github.com/malmoos/malmo/internal/store"
)

// The hosted /setup gate matrix (ENVIRONMENT.md # Admin bootstrap, issue #206).
// Appliance behavior is covered by TestAuthStateProgression; these exercise the
// hosted-profile divergence: the seeded one-time admin-bootstrap secret.

const hostedSecret = "bootstrap-abc123"

func hostedSecretHash() string {
	sum := sha256.Sum256([]byte(hostedSecret))
	return hex.EncodeToString(sum[:])
}

// hostedHarness is a harness provisioned as a hosted box with a seeded secret.
func hostedHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, func(s *Server) {
		s.SetEnvironment(profile.Hosted, "cindy-fox", hostedSecretHash())
	})
}

func assertNoUsers(t *testing.T, h *harness) {
	t.Helper()
	n, err := h.st.UserCount()
	if err != nil {
		t.Fatalf("user count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no users after rejected setup; got %d", n)
	}
}

func assertSetupFailureAudited(t *testing.T, h *harness) {
	t.Helper()
	events, err := h.st.ListAuditEvents(store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range events {
		if e.Action == audit.ActionSetupFailure && !e.Success {
			return
		}
	}
	t.Fatal("setup.failure audit event not found")
}

func TestHostedSetup_CorrectSecretCreatesAdmin(t *testing.T) {
	h := hostedHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": hostedSecret,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup with correct secret = %d; want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		User UserDTO `json:"user"`
	}](t, resp)
	if body.User.Username != "andrei" || body.User.Role != store.RoleAdmin {
		t.Fatalf("setup user = %+v", body.User)
	}
	if body.User.BoxID != "cindy-fox" {
		t.Errorf("box_id = %q; want cindy-fox surfaced on hosted setup", body.User.BoxID)
	}
}

// An out-of-band hand-off (cloud-console copy-paste) commonly carries trailing
// whitespace. ReadSeed trims the seeded secret before hashing, so /setup must
// trim the submitted value too — otherwise a correct secret 401s against the
// trimmed stored hash.
func TestHostedSetup_TrimsSurroundingWhitespace(t *testing.T) {
	h := hostedHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": "  " + hostedSecret + "\n",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup with whitespace-padded correct secret = %d; want 200", resp.StatusCode)
	}
}

func TestHostedSetup_WrongSecretRejectedAndAudited(t *testing.T) {
	h := hostedHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": "wrong",
	})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong secret = %d; want 401", resp.StatusCode)
	}
	assertNoUsers(t, h)
	assertSetupFailureAudited(t, h)
}

func TestHostedSetup_MissingSecretRejectedAndAudited(t *testing.T) {
	h := hostedHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 401 {
		t.Fatalf("missing secret = %d; want 401", resp.StatusCode)
	}
	assertNoUsers(t, h)
	assertSetupFailureAudited(t, h)
}

// A hosted box with no seed yet keeps /setup closed (503) — it must never fall
// back to the appliance's open-on-empty-box behavior.
func TestHostedSetup_NoSeedReturns503(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		s.SetEnvironment(profile.Hosted, "", "")
	})
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": "anything",
	})
	if resp.StatusCode != 503 {
		t.Fatalf("no-seed hosted setup = %d; want 503", resp.StatusCode)
	}
	assertNoUsers(t, h)
}

// The empty-box guard makes the gate naturally one-time: once the first admin
// exists, even the correct secret gets 409 (setup already completed).
func TestHostedSetup_OneTimeAfterFirstAdmin(t *testing.T) {
	h := hostedHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": hostedSecret,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("first setup = %d; want 200", resp.StatusCode)
	}
	resp = h.do("POST", "/api/v1/setup", map[string]string{
		"username": "cindy", "password": "doesntmatter", "bootstrap_secret": hostedSecret,
	})
	if resp.StatusCode != 409 {
		t.Fatalf("second setup = %d; want 409", resp.StatusCode)
	}
}

// Appliance ignores bootstrap_secret entirely and never surfaces box_id —
// byte-unchanged from pre-#206 behavior.
func TestApplianceSetup_IgnoresBootstrapSecretAndOmitsBoxID(t *testing.T) {
	h := newHarness(t) // zero-valued env ⇒ appliance
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2", "bootstrap_secret": "ignored",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("appliance setup = %d; want 200 (secret ignored)", resp.StatusCode)
	}
	body := decodeJSON[struct {
		User UserDTO `json:"user"`
	}](t, resp)
	if body.User.BoxID != "" {
		t.Errorf("appliance surfaced box_id = %q; want empty", body.User.BoxID)
	}
}
