package api

import (
	"testing"

	"github.com/malmoos/malmo/internal/audit"
	"github.com/malmoos/malmo/internal/profile"
	"github.com/malmoos/malmo/internal/store"
)

// Hosted-profile /setup divergence (issue #275). The hosted box bootstraps its
// first admin through the portal-to-box SSO handshake (sso_test.go), so /setup is
// disabled there; the appliance keeps its open-on-empty-box /setup. The SSO
// handshake itself is covered in sso_test.go.

// hostedHarness is a harness resolved to the hosted profile with a box-id but no
// portal key — enough for tests that only care about the profile (e.g. the
// auth-state profile probe). SSO tests use ssoHarness (sso_test.go), which also
// seeds a verification key.
func hostedHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, func(s *Server) {
		s.SetEnvironment(profile.Hosted, "cindy-fox", nil)
	})
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

// On hosted, POST /setup is disabled (403) and audited — the owner bootstraps via
// SSO, and an open /setup would be a second unauthenticated path to the founding
// admin.
func TestHostedSetup_Disabled(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		s.SetEnvironment(profile.Hosted, "cindy-fox", nil)
	})
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 403 {
		t.Fatalf("hosted setup = %d; want 403", resp.StatusCode)
	}
	n, err := h.st.UserCount()
	if err != nil {
		t.Fatalf("user count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no users after rejected hosted setup; got %d", n)
	}
	assertSetupFailureAudited(t, h)
}

// Appliance /setup is unchanged: open on an empty box and never surfaces box_id.
func TestApplianceSetup_OpenAndOmitsBoxID(t *testing.T) {
	h := newHarness(t) // zero-valued env ⇒ appliance
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("appliance setup = %d; want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		User UserDTO `json:"user"`
	}](t, resp)
	if body.User.BoxID != "" {
		t.Errorf("appliance surfaced box_id = %q; want empty", body.User.BoxID)
	}
}
