package store

import (
	"errors"
	"testing"
)

func TestBoxMetaRoundtrip(t *testing.T) {
	s := open(t)

	if _, err := s.GetBoxMeta(BoxMetaBoxID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unset key: err = %v, want ErrNotFound", err)
	}

	if err := s.SetBoxMeta(BoxMetaBoxID, "cindy-fox"); err != nil {
		t.Fatalf("set box_id: %v", err)
	}
	if err := s.SetBoxMeta(BoxMetaBootstrapSecretHash, "deadbeef"); err != nil {
		t.Fatalf("set hash: %v", err)
	}

	got, err := s.GetBoxMeta(BoxMetaBoxID)
	if err != nil {
		t.Fatalf("get box_id: %v", err)
	}
	if got != "cindy-fox" {
		t.Errorf("box_id = %q, want cindy-fox", got)
	}
	got, err = s.GetBoxMeta(BoxMetaBootstrapSecretHash)
	if err != nil {
		t.Fatalf("get hash: %v", err)
	}
	if got != "deadbeef" {
		t.Errorf("hash = %q, want deadbeef", got)
	}
}

// TestTelemetryConsentDefaultsOff verifies the unset key is false, not an error
// (TELEMETRY.md # Locked: off by default), and that opt-in/opt-out round-trips.
func TestTelemetryConsentDefaultsOff(t *testing.T) {
	s := open(t)

	got, err := s.TelemetryConsent()
	if err != nil {
		t.Fatalf("unset consent: %v", err)
	}
	if got {
		t.Error("unset telemetry consent = true, want false (off by default)")
	}

	if err := s.SetTelemetryConsent(true); err != nil {
		t.Fatalf("set consent on: %v", err)
	}
	if got, err = s.TelemetryConsent(); err != nil || !got {
		t.Errorf("consent after opt-in = %v (err %v), want true", got, err)
	}

	if err := s.SetTelemetryConsent(false); err != nil {
		t.Fatalf("set consent off: %v", err)
	}
	if got, err = s.TelemetryConsent(); err != nil || got {
		t.Errorf("consent after opt-out = %v (err %v), want false", got, err)
	}
}

// TestFirstRunCompleteMarker verifies the wizard-complete flag defaults false
// and latches true (FIRST_RUN.md # Phase 3).
func TestFirstRunCompleteMarker(t *testing.T) {
	s := open(t)

	got, err := s.FirstRunComplete()
	if err != nil {
		t.Fatalf("unset first-run: %v", err)
	}
	if got {
		t.Error("unset first_run_complete = true, want false")
	}

	if err := s.SetFirstRunComplete(); err != nil {
		t.Fatalf("set first-run complete: %v", err)
	}
	if got, err = s.FirstRunComplete(); err != nil || !got {
		t.Errorf("first_run_complete after set = %v (err %v), want true", got, err)
	}
}

// TestBoxMetaUpsert confirms SetBoxMeta overwrites in place rather than erroring
// on a duplicate key — the idempotent persist the brain relies on across boots.
func TestBoxMetaUpsert(t *testing.T) {
	s := open(t)
	if err := s.SetBoxMeta(BoxMetaBoxID, "cindy-fox"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := s.SetBoxMeta(BoxMetaBoxID, "rocky-owl"); err != nil {
		t.Fatalf("second set: %v", err)
	}
	got, err := s.GetBoxMeta(BoxMetaBoxID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "rocky-owl" {
		t.Errorf("box_id = %q, want rocky-owl (upsert should overwrite)", got)
	}
}
