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
	if err := s.SetBoxMeta(BoxMetaAssertionKey, "dGVzdGtleQ=="); err != nil {
		t.Fatalf("set assertion key: %v", err)
	}

	got, err := s.GetBoxMeta(BoxMetaBoxID)
	if err != nil {
		t.Fatalf("get box_id: %v", err)
	}
	if got != "cindy-fox" {
		t.Errorf("box_id = %q, want cindy-fox", got)
	}
	got, err = s.GetBoxMeta(BoxMetaAssertionKey)
	if err != nil {
		t.Fatalf("get assertion key: %v", err)
	}
	if got != "dGVzdGtleQ==" {
		t.Errorf("assertion key = %q, want dGVzdGtleQ==", got)
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
