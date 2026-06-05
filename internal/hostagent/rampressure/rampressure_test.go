package rampressure

import (
	"errors"
	"fmt"
	"testing"
)

// realIdlePSI is verbatim `/proc/pressure/memory` from a host with ample free
// memory: every average is zero. The parser must read the `some` line's avg60
// and ignore the `full` line and the trailing total counter.
const realIdlePSI = `some avg10=0.00 avg60=0.00 avg300=0.00 total=0
full avg10=0.00 avg60=0.00 avg300=0.00 total=0
`

// realThrashingPSI is verbatim output from a host under sustained memory
// pressure (swap thrashing): some avg60 well above the threshold, with full
// (all tasks stalled) also elevated. Only `some avg60` (34.27) drives the
// detector.
const realThrashingPSI = `some avg10=41.55 avg60=34.27 avg300=22.18 total=128374651
full avg10=29.10 avg60=23.84 avg300=15.02 total=98123400
`

func TestParseSomeAvg60_Idle(t *testing.T) {
	v, ok := parseSomeAvg60(realIdlePSI)
	if !ok {
		t.Fatal("want ok=true for real idle PSI output")
	}
	if v != 0.0 {
		t.Errorf("avg60: got %v, want 0", v)
	}
}

func TestParseSomeAvg60_Thrashing(t *testing.T) {
	v, ok := parseSomeAvg60(realThrashingPSI)
	if !ok {
		t.Fatal("want ok=true for real thrashing PSI output")
	}
	// Must read the some line (34.27), never the full line (23.84).
	if v < 34.2 || v > 34.3 {
		t.Errorf("avg60: got %v, want ~34.27 (the `some` line, not `full`)", v)
	}
}

func TestParseSomeAvg60_Garbage(t *testing.T) {
	if _, ok := parseSomeAvg60("not a psi file at all\n"); ok {
		t.Error("want ok=false for unparseable output")
	}
	// `full` present but `some` missing must also fail (we only trust some avg60).
	if _, ok := parseSomeAvg60("full avg10=5.0 avg60=5.0 avg300=5.0 total=1\n"); ok {
		t.Error("want ok=false when the some line is absent")
	}
}

// psiOutput renders a minimal /proc/pressure/memory payload with the given
// `some avg60`, formatted as the kernel prints it — so the Read tests round-trip
// through the real parser.
func psiOutput(someAvg60 float64) string {
	return fmt.Sprintf(
		"some avg10=%.2f avg60=%.2f avg300=%.2f total=12345\n"+
			"full avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		someAvg60, someAvg60, someAvg60)
}

func reporter(out string, readErr error) *Reporter {
	return &Reporter{read: func() (string, error) { return out, readErr }}
}

func TestRead_HealthyReturnsNil(t *testing.T) {
	if got := reporter(psiOutput(3.5), nil).Read(); got != nil {
		t.Errorf("low pressure: want nil findings, got %v", got)
	}
}

func TestRead_RaisesOnSustainedPressure(t *testing.T) {
	got := reporter(psiOutput(34.27), nil).Read()
	if len(got) != 1 || got[0].ID != "ram-pressure" {
		t.Fatalf("high pressure: want one ram-pressure finding, got %v", got)
	}
	if got[0].InstanceKey != "" {
		t.Errorf("ram-pressure is box-wide; want empty instance_key, got %q", got[0].InstanceKey)
	}
}

func TestRead_BoundaryIsHealthy(t *testing.T) {
	// Exactly at the threshold must not raise (raise is strictly above).
	if got := reporter(psiOutput(pressureThreshold), nil).Read(); got != nil {
		t.Errorf("avg60 == threshold must not raise, got %v", got)
	}
}

// TestRead_FailOpenOnReadError verifies a missing/unreadable PSI file (e.g.
// psi=1 absent from the kernel cmdline) never raises a ram-pressure issue and
// never panics — it fails open.
func TestRead_FailOpenOnReadError(t *testing.T) {
	if got := reporter("", errors.New("open /proc/pressure/memory: no such file")).Read(); got != nil {
		t.Errorf("read error must fail open (nil findings), got %v", got)
	}
}

// TestRead_FailOpenOnParseError verifies unparseable PSI content fails open
// rather than raising.
func TestRead_FailOpenOnParseError(t *testing.T) {
	if got := reporter("garbage\n", nil).Read(); got != nil {
		t.Errorf("parse error must fail open (nil findings), got %v", got)
	}
}
