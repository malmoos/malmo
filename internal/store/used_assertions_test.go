package store

import (
	"errors"
	"testing"
	"time"
)

func TestUseAssertionJTI_SingleUse(t *testing.T) {
	s := open(t)
	now := time.Unix(1_750_000_000, 0)
	exp := now.Add(60 * time.Second)

	if err := s.UseAssertionJTI("jti-1", exp, now); err != nil {
		t.Fatalf("first use: %v", err)
	}
	// Replaying the same jti while it is still in the ledger is a conflict.
	if err := s.UseAssertionJTI("jti-1", exp, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay: err = %v, want ErrConflict", err)
	}
	// A different jti is fine.
	if err := s.UseAssertionJTI("jti-2", exp, now); err != nil {
		t.Fatalf("second jti: %v", err)
	}
}

// Past-expiry rows are pruned on each write, so the ledger stays bounded and an
// expired jti could in principle be reused — harmless, since the assertion is
// already rejected on its own expiry before reaching this ledger.
func TestUseAssertionJTI_PrunesExpired(t *testing.T) {
	s := open(t)
	t0 := time.Unix(1_750_000_000, 0)
	if err := s.UseAssertionJTI("jti-old", t0.Add(60*time.Second), t0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Advance well past expiry: the next write prunes jti-old, so re-inserting it
	// no longer conflicts.
	later := t0.Add(10 * time.Minute)
	if err := s.UseAssertionJTI("jti-old", later.Add(60*time.Second), later); err != nil {
		t.Fatalf("post-prune reuse: err = %v, want nil", err)
	}
}
