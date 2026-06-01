package hostagent

import (
	"fmt"
	"sync"
	"time"

	"github.com/malmo/malmo/internal/protocol"
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
