package store

import (
	"errors"
	"testing"
	"time"
)

// Confirm challenges for the hosted re-auth round-trip (issue #469). The whole
// safety of that path rests on the row being spent exactly once and aging out,
// so both are asserted here rather than only at the API layer.

func seedChallengeUser(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateFirstAdmin(sampleUser("u1", "andrei", RoleAdmin)); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
}

func TestElevationChallenge_SingleUse(t *testing.T) {
	s := open(t)
	seedChallengeUser(t, s)
	now := time.Unix(1_750_000_000, 0)

	if err := s.CreateElevationChallenge("c1", "u1", now.Add(5*time.Minute), now); err != nil {
		t.Fatalf("create: %v", err)
	}
	userID, err := s.UseElevationChallenge("c1", now)
	if err != nil {
		t.Fatalf("first use: %v", err)
	}
	if userID != "u1" {
		t.Fatalf("user id = %q; want u1", userID)
	}
	// Spending it deletes it, so a replay finds nothing.
	if _, err := s.UseElevationChallenge("c1", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay: err = %v, want ErrNotFound", err)
	}
}

func TestElevationChallenge_ExpiredIsNotSpendable(t *testing.T) {
	s := open(t)
	seedChallengeUser(t, s)
	now := time.Unix(1_750_000_000, 0)

	if err := s.CreateElevationChallenge("c1", "u1", now.Add(5*time.Minute), now); err != nil {
		t.Fatalf("create: %v", err)
	}
	later := now.Add(6 * time.Minute)
	if _, err := s.UseElevationChallenge("c1", later); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired use: err = %v, want ErrNotFound", err)
	}
}

// Past-expiry rows are pruned on the next mint, so the table stays bounded to
// roughly the in-flight set without a sweeper.
func TestElevationChallenge_PrunesExpired(t *testing.T) {
	s := open(t)
	seedChallengeUser(t, s)
	now := time.Unix(1_750_000_000, 0)
	if err := s.CreateElevationChallenge("old", "u1", now.Add(5*time.Minute), now); err != nil {
		t.Fatalf("create old: %v", err)
	}
	later := now.Add(1 * time.Hour)
	if err := s.CreateElevationChallenge("new", "u1", later.Add(5*time.Minute), later); err != nil {
		t.Fatalf("create new: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM elevation_challenges`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d; want 1 (the expired one pruned)", n)
	}
}

// Deleting a user takes their unspent challenges with them — a challenge must
// never outlive the account it was minted for.
func TestElevationChallenge_CascadesWithUser(t *testing.T) {
	s := open(t)
	seedChallengeUser(t, s)
	now := time.Unix(1_750_000_000, 0)
	if err := s.CreateElevationChallenge("c1", "u1", now.Add(5*time.Minute), now); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteUser("u1"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := s.UseElevationChallenge("c1", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after user delete: err = %v, want ErrNotFound", err)
	}
}
