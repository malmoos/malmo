package hostagent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/molmaos/molma/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

// FakeVerifier implements PasswordVerifier using the Agent's own in-memory
// bcrypt map. It holds a pointer back to the Agent so it reads from the same
// map that setPassword writes to — no separate map, no synchronization gap.
//
// Design choice: FakeVerifier reads Agent.passwords (via Agent.mu) rather than
// duplicating the map on the verifier. The alternative (putting the map on
// FakeVerifier and having setPassword/deleteUser delegate writes through an
// extended interface) would require a wider interface just to satisfy the fake,
// which violates the "no premature abstraction" rule. Pointer-back is simpler
// and has fewer crossing wires.
type FakeVerifier struct {
	a *Agent
}

// NewFakeVerifier returns a FakeVerifier wired to the given Agent.
// Call after New() so both share the same Agent instance.
func NewFakeVerifier(a *Agent) *FakeVerifier {
	return &FakeVerifier{a: a}
}

func (f *FakeVerifier) Verify(user, password string) (bool, error) {
	f.a.mu.Lock()
	hash, ok := f.a.passwords[user]
	f.a.mu.Unlock()
	if !ok {
		return false, nil
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil, nil
}

// FakePublisher implements Publisher using the Agent's in-memory published map.
// It records names in the same map that the publish/unpublish handlers maintain
// as a write-through cache — no separate state, no synchronization gap.
//
// Used by cmd/host-agent (the fake binary). Matches current fake behavior:
// returns "<slug>.local" as the name and reports "established" immediately.
type FakePublisher struct {
	hostSuffix string
}

// NewFakePublisher returns a FakePublisher. hostSuffix should be
// protocol.AppHostSuffix (".local") in both dev and test contexts; an empty
// string defaults to it.
func NewFakePublisher(hostSuffix string) *FakePublisher {
	if hostSuffix == "" {
		hostSuffix = protocol.AppHostSuffix
	}
	return &FakePublisher{hostSuffix: hostSuffix}
}

func (f *FakePublisher) Publish(slug string) (string, error) {
	if slug == "" {
		return "", fmt.Errorf("fakepublisher: slug is required")
	}
	return slug + f.hostSuffix, nil
}

func (f *FakePublisher) Unpublish(_ string) error {
	return nil
}

// FakeHealthSource implements HealthSource with a settable findings list,
// used by cmd/host-agent and by brain integration tests that need to seed
// specific findings (e.g. "data-drive-missing") and assert the brain raises
// the matching health issue.
type FakeHealthSource struct {
	mu       sync.Mutex
	findings []protocol.Finding
}

// NewFakeHealthSource returns an empty source (storage looks healthy).
func NewFakeHealthSource() *FakeHealthSource {
	return &FakeHealthSource{}
}

// Set replaces the current findings list. Pass nil to clear (which the
// handler reports as an empty list, i.e. "storage looks healthy").
func (f *FakeHealthSource) Set(findings []protocol.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings = append(f.findings[:0:0], findings...)
}

func (f *FakeHealthSource) Read() (protocol.StorageHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.Finding, len(f.findings))
	copy(out, f.findings)
	return protocol.StorageHealth{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Findings:  out,
	}, nil
}

// FakeServiceReporter implements ServiceReporter with a settable findings list,
// used by brain integration tests that need to seed specific service-down
// findings and assert the brain raises the matching state-category issue.
// cmd/host-agent (the fake binary) does not wire one by default — dev has no
// systemd units to watch.
type FakeServiceReporter struct {
	mu       sync.Mutex
	findings []protocol.Finding
}

// NewFakeServiceReporter returns an empty reporter (all services healthy).
func NewFakeServiceReporter() *FakeServiceReporter {
	return &FakeServiceReporter{}
}

// Set replaces the current findings list. Pass nil to clear (all services up).
func (f *FakeServiceReporter) Set(findings []protocol.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings = append(f.findings[:0:0], findings...)
}

func (f *FakeServiceReporter) Read() []protocol.Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.Finding, len(f.findings))
	copy(out, f.findings)
	return out
}

// FakeClockReporter implements ClockReporter with a settable findings list, used
// by brain integration tests that need to seed a clock-not-synced finding and
// assert the brain raises the matching network-category issue. cmd/host-agent
// (the fake binary) does not wire one by default — dev has no chrony to query.
type FakeClockReporter struct {
	mu       sync.Mutex
	findings []protocol.Finding
}

// NewFakeClockReporter returns an empty reporter (clock healthy).
func NewFakeClockReporter() *FakeClockReporter {
	return &FakeClockReporter{}
}

// Set replaces the current findings list. Pass nil to clear (clock synced).
func (f *FakeClockReporter) Set(findings []protocol.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings = append(f.findings[:0:0], findings...)
}

func (f *FakeClockReporter) Read() []protocol.Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.Finding, len(f.findings))
	copy(out, f.findings)
	return out
}

// FakeRAMReporter implements RAMReporter with a settable findings list, used by
// brain integration tests that need to seed a ram-pressure finding and assert
// the brain raises the matching capacity-category issue. cmd/host-agent (the
// fake binary) does not wire one by default — dev has no PSI to sample.
type FakeRAMReporter struct {
	mu       sync.Mutex
	findings []protocol.Finding
}

// NewFakeRAMReporter returns an empty reporter (pressure below threshold).
func NewFakeRAMReporter() *FakeRAMReporter {
	return &FakeRAMReporter{}
}

// Set replaces the current findings list. Pass nil to clear (pressure healthy).
func (f *FakeRAMReporter) Set(findings []protocol.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings = append(f.findings[:0:0], findings...)
}

func (f *FakeRAMReporter) Read() []protocol.Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.Finding, len(f.findings))
	copy(out, f.findings)
	return out
}

// FakeDiskReporter implements DiskReporter with settable free/total levels. The
// fake binary wires one with a plausible canned figure so the dev-loop install
// plan shows a non-zero free_bytes; brain integration tests set specific levels
// to assert the figure flows through to the install plan.
type FakeDiskReporter struct {
	mu          sync.Mutex
	free, total int64
}

// NewFakeDiskReporter returns a reporter with the given free/total byte levels.
func NewFakeDiskReporter(free, total int64) *FakeDiskReporter {
	return &FakeDiskReporter{free: free, total: total}
}

// Set replaces the reported free/total levels.
func (f *FakeDiskReporter) Set(free, total int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.free, f.total = free, total
}

func (f *FakeDiskReporter) DataDisk() (int64, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.free, f.total
}

// FakeGPUReporter implements GPUReporter with a settable report. The fake
// binary wires one reporting a synthetic Intel iGPU (present, vendor "intel",
// a fixed dev render GID) so the `gpu: true` override path is exercisable
// under make dev without real hardware; Set flips it to "no usable GPU" so
// the capacity-refusal path is testable too.
type FakeGPUReporter struct {
	mu  sync.Mutex
	gpu protocol.SystemGPU
}

// NewFakeGPUReporter returns a reporter serving the given report.
func NewFakeGPUReporter(gpu protocol.SystemGPU) *FakeGPUReporter {
	return &FakeGPUReporter{gpu: gpu}
}

// Set replaces the report. The zero value reports no usable GPU.
func (f *FakeGPUReporter) Set(gpu protocol.SystemGPU) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gpu = gpu
}

func (f *FakeGPUReporter) Read() protocol.SystemGPU {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gpu
}

// FakeRebootReporter implements RebootReporter with a settable findings list,
// used by brain integration tests that need to seed a reboot-required finding
// and assert the brain raises the matching system-category issue.
// cmd/host-agent (the fake binary) does not wire one by default — dev has no
// /var/run/reboot-required flag to stat.
type FakeRebootReporter struct {
	mu       sync.Mutex
	findings []protocol.Finding
}

// NewFakeRebootReporter returns an empty reporter (no reboot pending).
func NewFakeRebootReporter() *FakeRebootReporter {
	return &FakeRebootReporter{}
}

// Set replaces the current findings list. Pass nil to clear (no reboot pending).
func (f *FakeRebootReporter) Set(findings []protocol.Finding) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findings = append(f.findings[:0:0], findings...)
}

func (f *FakeRebootReporter) Read() []protocol.Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.Finding, len(f.findings))
	copy(out, f.findings)
	return out
}

// FakeLogSource implements LogSource with a synthetic line generator: it emits
// one plausible stdout line per tick (default ~1s), tagged with the container
// name, until the follow context is cancelled. cmd/host-agent (the fake binary)
// wires it so the dev-loop Logs tab shows a live, reconnecting stream without a
// real journald or Docker's journald log driver in place. Tests pass a short
// interval so frames arrive promptly.
type FakeLogSource struct {
	interval time.Duration
}

// NewFakeLogSource returns a source emitting one synthetic line per interval.
// A zero or negative interval defaults to one second.
func NewFakeLogSource(interval time.Duration) *FakeLogSource {
	if interval <= 0 {
		interval = time.Second
	}
	return &FakeLogSource{interval: interval}
}

func (f *FakeLogSource) Follow(ctx context.Context, container string) (<-chan protocol.JournalLine, error) {
	ch := make(chan protocol.JournalLine)
	go func() {
		defer close(ch)
		t := time.NewTicker(f.interval)
		defer t.Stop()
		var n int
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n++
				line := protocol.JournalLine{
					Ts:     time.Now().UTC().Format(time.RFC3339),
					Stream: "stdout",
					Line:   fmt.Sprintf("%s: synthetic log line %d", container, n),
				}
				select {
				case ch <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}
