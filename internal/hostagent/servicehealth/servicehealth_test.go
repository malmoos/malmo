package servicehealth

import (
	"reflect"
	"testing"

	"github.com/malmo/malmo/internal/protocol"
)

// TestRead_ReportsOnlyNonActiveUnits verifies the reporter emits one
// service-down finding per non-active unit (carrying the unit name as
// instance_key and its raw state in details) and skips active units.
func TestRead_ReportsOnlyNonActiveUnits(t *testing.T) {
	r := &Reporter{
		units: []string{"docker.service", "caddy.service", "smbd.service"},
		isActive: func(unit string) (bool, string) {
			if unit == "caddy.service" {
				return false, "failed"
			}
			return true, "active"
		},
	}

	got := r.Read()
	want := []protocol.Finding{
		{ID: "service-down", InstanceKey: "caddy.service", Details: "caddy.service is failed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read: got %v, want %v", got, want)
	}
}

// TestRead_AllActiveReturnsNil verifies a healthy box produces no findings —
// the reporter reports instantaneous state, so "everything up" is an empty
// (nil) result, not an error.
func TestRead_AllActiveReturnsNil(t *testing.T) {
	r := &Reporter{
		units:    CoreUnits,
		isActive: func(string) (bool, string) { return true, "active" },
	}

	got := r.Read()
	if got != nil {
		t.Errorf("all-active: want nil findings, got %v", got)
	}
}

// TestRead_OneFindingPerDownUnit verifies the per-unit instance_key scoping:
// two down units yield two distinct service-down findings, so docker-down and
// caddy-down are separate issues in the brain's registry rather than collapsing
// into one.
func TestRead_OneFindingPerDownUnit(t *testing.T) {
	r := &Reporter{
		units:    []string{"docker.service", "caddy.service"},
		isActive: func(string) (bool, string) { return false, "inactive" },
	}

	got := r.Read()
	if len(got) != 2 {
		t.Fatalf("want one finding per down unit (2), got %d: %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, f := range got {
		if f.ID != "service-down" {
			t.Errorf("finding id: want service-down, got %q", f.ID)
		}
		if f.InstanceKey == "" {
			t.Errorf("finding must carry the unit as instance_key, got %+v", f)
		}
		seen[f.InstanceKey] = true
	}
	if !seen["docker.service"] || !seen["caddy.service"] {
		t.Errorf("want a finding for each down unit, got keys %v", seen)
	}
}

// TestNew_WatchesCoreUnits guards that the real constructor watches the
// documented core-unit allowlist and never includes host-agent itself (a dead
// host-agent can't report on its own state).
func TestNew_WatchesCoreUnits(t *testing.T) {
	r := New()
	if !reflect.DeepEqual(r.units, CoreUnits) {
		t.Errorf("New().units: got %v, want CoreUnits %v", r.units, CoreUnits)
	}
	for _, u := range r.units {
		if u == "host-agent.service" {
			t.Error("host-agent must not be in the allowlist — it can't report on itself")
		}
	}
}
