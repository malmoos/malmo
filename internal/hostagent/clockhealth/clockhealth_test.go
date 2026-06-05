package clockhealth

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// realSyncedTracking is verbatim `chronyc tracking` output from a healthy,
// synced host — the parser must tolerate the full field set, not just the two
// lines it reads.
const realSyncedTracking = `Reference ID    : C0248F82 (ntp1.example.net)
Stratum         : 2
Ref time (UTC)  : Fri Jun 05 12:34:56 2026
System time     : 0.000012345 seconds slow of NTP time
Last offset     : -0.000004567 seconds
RMS offset      : 0.000008901 seconds
Frequency       : 12.345 ppm slow
Residual freq   : +0.001 ppm
Skew            : 0.123 ppm
Root delay      : 0.012345678 seconds
Root dispersion : 0.001234567 seconds
Update interval : 64.5 seconds
Leap status     : Normal
`

// realUnsyncedTracking is verbatim output from a host whose chrony has never
// reached a source: epoch Ref time and "Not synchronised" leap status.
const realUnsyncedTracking = `Reference ID    : 00000000 ()
Stratum         : 0
Ref time (UTC)  : Thu Jan 01 00:00:00 1970
System time     : 0.000000000 seconds fast of NTP time
Last offset     : +0.000000000 seconds
RMS offset      : 0.000000000 seconds
Frequency       : 0.000 ppm slow
Residual freq   : +0.000 ppm
Skew            : 0.000 ppm
Root delay      : 1.000000000 seconds
Root dispersion : 1.000000000 seconds
Update interval : 0.0 seconds
Leap status     : Not synchronised
`

func TestParseTracking_RealSynced(t *testing.T) {
	got := parseTracking(realSyncedTracking)
	if !got.ok {
		t.Fatal("want ok=true for real synced output")
	}
	wantRef := time.Date(2026, time.June, 5, 12, 34, 56, 0, time.UTC)
	if !got.refTime.Equal(wantRef) {
		t.Errorf("refTime: got %v, want %v", got.refTime, wantRef)
	}
	// 0.000004567s ≈ 4.567µs, taken absolute from the "-" sign.
	if got.offset < 4*time.Microsecond || got.offset > 5*time.Microsecond {
		t.Errorf("offset: got %v, want ~4.567µs", got.offset)
	}
}

func TestParseTracking_RealUnsynced(t *testing.T) {
	got := parseTracking(realUnsyncedTracking)
	if !got.ok {
		t.Fatal("want ok=true (fields present) for real unsynced output")
	}
	if got.refTime.Year() != 1970 {
		t.Errorf("refTime year: got %d, want 1970 (epoch)", got.refTime.Year())
	}
	if got.offset != 0 {
		t.Errorf("offset: got %v, want 0", got.offset)
	}
}

// TestRaiseReason_PlainEnglish locks the banner detail to plain English — it
// reaches the dashboard via GET /api/v1/health, so no Go-duration "7h0m0s".
func TestRaiseReason_PlainEnglish(t *testing.T) {
	epoch := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, time.June, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		age, offset time.Duration
		refTime     time.Time
		want        string
	}{
		{"never synced", 56 * time.Hour, 0, epoch, "clock has never synced"},
		{"stale sync", 7 * time.Hour, 0, recent, "last synced about 7 hours ago"},
		{"large offset", time.Minute, 12 * time.Second, recent, "off by about 12 seconds"},
		{"both", 7 * time.Hour, 13 * time.Second, recent, "last synced about 7 hours ago; off by about 13 seconds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := raiseReason(c.age, c.offset, c.refTime); got != c.want {
				t.Errorf("raiseReason: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseTracking_Garbage(t *testing.T) {
	if got := parseTracking("not chrony output at all\n"); got.ok {
		t.Errorf("want ok=false for unparseable output, got %+v", got)
	}
}

// trackingOutput renders a minimal `chronyc tracking` payload with the given ref
// time and offset, formatted exactly as chrony prints it — so the Read tests
// round-trip through the real parser.
func trackingOutput(refTime time.Time, offsetSeconds float64) string {
	return fmt.Sprintf("Reference ID    : C0248F82 (ntp1.example.net)\n"+
		"Ref time (UTC)  : %s\n"+
		"Last offset     : %.9f seconds\n"+
		"Leap status     : Normal\n",
		refTime.UTC().Format("Mon Jan 02 15:04:05 2006"), offsetSeconds)
}

func reporterAt(now time.Time, out string, runErr error) *Reporter {
	return &Reporter{
		now:         func() time.Time { return now },
		runTracking: func() (string, error) { return out, runErr },
		interval:    sampleInterval,
	}
}

func TestRead_HealthyReturnsNil(t *testing.T) {
	now := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	ref := now.Add(-5 * time.Minute) // synced 5 min ago
	r := reporterAt(now, trackingOutput(ref, -0.000004), nil)
	if got := r.Read(); got != nil {
		t.Errorf("synced-and-low-offset: want nil findings, got %v", got)
	}
}

func TestRead_RaisesOnStaleSync(t *testing.T) {
	now := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	ref := now.Add(-7 * time.Hour) // last sync 7h ago > 6h threshold
	r := reporterAt(now, trackingOutput(ref, 0.0001), nil)
	got := r.Read()
	if len(got) != 1 || got[0].ID != "clock-not-synced" {
		t.Fatalf("stale sync: want one clock-not-synced finding, got %v", got)
	}
	if got[0].InstanceKey != "" {
		t.Errorf("clock-not-synced is box-wide; want empty instance_key, got %q", got[0].InstanceKey)
	}
}

func TestRead_RaisesOnLargeOffset(t *testing.T) {
	now := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	ref := now.Add(-1 * time.Minute)                      // recently synced…
	r := reporterAt(now, trackingOutput(ref, -12.5), nil) // …but 12.5s off > 10s
	got := r.Read()
	if len(got) != 1 || got[0].ID != "clock-not-synced" {
		t.Fatalf("large offset: want one clock-not-synced finding, got %v", got)
	}
}

func TestRead_BoundaryOffsetIsHealthy(t *testing.T) {
	now := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	ref := now.Add(-1 * time.Minute)
	r := reporterAt(now, trackingOutput(ref, 10.0), nil) // exactly 10s → not > 10s
	if got := r.Read(); got != nil {
		t.Errorf("offset == threshold must not raise, got %v", got)
	}
}

// TestRead_CachesWithinInterval pins the 5-minute re-query cadence (TIME.md):
// two Reads inside the interval exec chronyc once; the second returns the cache.
func TestRead_CachesWithinInterval(t *testing.T) {
	cur := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	ref := cur.Add(-7 * time.Hour)
	calls := 0
	r := &Reporter{
		now:         func() time.Time { return cur },
		runTracking: func() (string, error) { calls++; return trackingOutput(ref, 0), nil },
		interval:    sampleInterval,
	}
	r.Read()
	cur = cur.Add(2 * time.Minute) // still inside the 5-min window
	if got := r.Read(); len(got) != 1 {
		t.Fatalf("cached read: want the prior finding, got %v", got)
	}
	if calls != 1 {
		t.Errorf("want chronyc queried once within the interval, got %d", calls)
	}
}

func TestRead_ResamplesAfterInterval(t *testing.T) {
	cur := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	calls := 0
	out := trackingOutput(cur.Add(-7*time.Hour), 0) // first sample: stale → finding
	r := &Reporter{
		now:         func() time.Time { return cur },
		runTracking: func() (string, error) { calls++; return out, nil },
		interval:    sampleInterval,
	}
	if got := r.Read(); len(got) != 1 {
		t.Fatalf("first sample: want a finding, got %v", got)
	}
	cur = cur.Add(6 * time.Minute)                   // past the interval → re-query
	out = trackingOutput(cur.Add(-1*time.Minute), 0) // now healthy
	if got := r.Read(); got != nil {
		t.Fatalf("after recovery + re-sample: want nil, got %v", got)
	}
	if calls != 2 {
		t.Errorf("want chronyc re-queried after the interval, got %d calls", calls)
	}
}

// TestRead_FailOpenOnExecError verifies an exec error never raises a clock issue
// (chrony being down is service-down's job) and never panics.
func TestRead_FailOpenOnExecError(t *testing.T) {
	now := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	r := reporterAt(now, "", errors.New("chronyc: command not found"))
	if got := r.Read(); got != nil {
		t.Errorf("exec error must fail open (nil findings), got %v", got)
	}
}

// TestRead_ParseErrorHoldsPriorSample verifies an unparseable sample never
// clears an active finding (it holds the prior one) but still advances the timer
// so it backs off to the 5-min cadence rather than hot-looping chronyc.
func TestRead_ParseErrorHoldsPriorSample(t *testing.T) {
	cur := time.Date(2026, time.June, 5, 12, 40, 0, 0, time.UTC)
	out := trackingOutput(cur.Add(-7*time.Hour), 0) // first sample: stale → finding
	calls := 0
	r := &Reporter{
		now:         func() time.Time { return cur },
		runTracking: func() (string, error) { calls++; return out, nil },
		interval:    sampleInterval,
	}
	if got := r.Read(); len(got) != 1 {
		t.Fatalf("first sample: want a finding, got %v", got)
	}
	cur = cur.Add(6 * time.Minute)        // past the interval → re-query
	out = "garbage that does not parse\n" // chrony answered but unparseable
	if got := r.Read(); len(got) != 1 || got[0].ID != "clock-not-synced" {
		t.Fatalf("parse error must hold the prior finding, got %v", got)
	}
	if calls != 2 {
		t.Errorf("want chronyc re-queried after the interval, got %d calls", calls)
	}
	// Timer advanced: a third Read inside the new window must not re-exec.
	cur = cur.Add(1 * time.Minute)
	if got := r.Read(); len(got) != 1 {
		t.Fatalf("cached read after parse error: want held finding, got %v", got)
	}
	if calls != 2 {
		t.Errorf("parse error must advance the timer (no hot-loop), got %d calls", calls)
	}
}

func TestNew_UsesSpecCadence(t *testing.T) {
	if New().interval != 5*time.Minute {
		t.Errorf("New().interval: got %v, want 5m (TIME.md cadence)", New().interval)
	}
}
