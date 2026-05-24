package hostagent

import (
	"fmt"

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
// returns "<slug>.malmo.local" as the name and reports "established" immediately.
type FakePublisher struct {
	hostSuffix string
}

// NewFakePublisher returns a FakePublisher. hostSuffix should be ".malmo.local"
// in both dev and test contexts.
func NewFakePublisher(hostSuffix string) *FakePublisher {
	if hostSuffix == "" {
		hostSuffix = ".malmo.local"
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
