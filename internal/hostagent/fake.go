package hostagent

import "golang.org/x/crypto/bcrypt"

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
