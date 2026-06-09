package protocol

import "testing"

// TestHealthCategoryWireValues pins the on-the-wire string for every
// HealthCategory constant. These cross the brain↔host-agent socket in
// SystemHealth and are matched by string in the brain's ApplyFindings reconcile
// scoping, so a rename here is a breaking protocol change — this guard makes an
// accidental one fail loudly rather than silently mis-routing findings. The
// full enum is pinned (including the reserved drives/resources/time domains) so
// downstream detectors land as pure follow-ups.
func TestHealthCategoryWireValues(t *testing.T) {
	cases := map[HealthCategory]string{
		HealthCategoryStorage:   "storage",
		HealthCategoryDrives:    "drives",
		HealthCategoryServices:  "services",
		HealthCategoryResources: "resources",
		HealthCategoryTime:      "time",
		HealthCategorySystem:    "system",
	}
	for c, want := range cases {
		if string(c) != want {
			t.Errorf("HealthCategory wire value: got %q, want %q", string(c), want)
		}
	}
}
