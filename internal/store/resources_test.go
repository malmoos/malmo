package store

import "testing"

func TestResourceLimitsRoundtripUpsertAndCascade(t *testing.T) {
	s := open(t)
	if err := s.Create(sample("a", "alpha")); err != nil {
		t.Fatalf("create: %v", err)
	}

	// No row yet → zero value (uncapped), not an error.
	got, err := s.GetResourceLimits("a")
	if err != nil {
		t.Fatalf("get (absent): %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("absent limits = %+v, want zero", got)
	}

	// Set then read back.
	want := ResourceLimits{MemoryBytes: 512 << 20, NanoCPUs: 1_500_000_000}
	if err := s.SetResourceLimits("a", want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = s.GetResourceLimits("a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != want {
		t.Fatalf("roundtrip = %+v, want %+v", got, want)
	}

	// A second Set upserts the same row (PRIMARY KEY on instance_id).
	want2 := ResourceLimits{MemoryBytes: 1 << 30, NanoCPUs: 0}
	if err := s.SetResourceLimits("a", want2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = s.GetResourceLimits("a")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got != want2 {
		t.Fatalf("after upsert = %+v, want %+v", got, want2)
	}

	// Deleting the instance cascades the limits row away.
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := s.GetResourceLimits("a"); !got.IsZero() {
		t.Fatalf("limits survived instance delete: %+v", got)
	}
}

// SetResourceLimits on an unknown instance must fail the FK (foreign_keys=ON):
// a limit policy can only exist for a real instance.
func TestSetResourceLimitsUnknownInstanceFails(t *testing.T) {
	s := open(t)
	if err := s.SetResourceLimits("ghost", ResourceLimits{MemoryBytes: 1 << 20}); err == nil {
		t.Fatal("set on unknown instance must fail the foreign key")
	}
}

func TestResourceLimitsIsZero(t *testing.T) {
	for _, tc := range []struct {
		rl   ResourceLimits
		zero bool
	}{
		{ResourceLimits{}, true},
		{ResourceLimits{MemoryBytes: 1}, false},
		{ResourceLimits{NanoCPUs: 1}, false},
		{ResourceLimits{MemoryBytes: 1, NanoCPUs: 1}, false},
	} {
		if got := tc.rl.IsZero(); got != tc.zero {
			t.Errorf("%+v.IsZero() = %v, want %v", tc.rl, got, tc.zero)
		}
	}
}
