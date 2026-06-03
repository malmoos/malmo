//go:build linux && cgo && pamtest

package pamverifier

import (
	"testing"
)

// TestPAMVerifier_RealAuthentication exercises PAMVerifier against the real
// system PAM stack. It requires:
//   - /etc/pam.d/molma-test installed (copy dev/pam/molma → /etc/pam.d/molma-test)
//   - A test user "molma-pamtest" with a known password, provisioned by the
//     nspawn harness (useradd + chpasswd inside the container).
//   - The binary running as root (pam_unix.so requires privilege).
//
// This test is intentionally skipped in normal runs. Pass -tags pamtest to
// include it; the nspawn lane is the intended runner (see TESTING.md # Fast lane).
//
// TODO (nspawn lane wiring):
//   - Add nspawn test fixture: useradd molma-pamtest && chpasswd <<< "molma-pamtest:TestPass123"
//   - Install dev/pam/molma as /etc/pam.d/molma-test inside the container.
//   - Run: go test -tags pamtest -run TestPAMVerifier ./internal/hostagent/pamverifier/ as root.
func TestPAMVerifier_RealAuthentication(t *testing.T) {
	const (
		service  = "molma-test"
		user     = "molma-pamtest"
		password = "TestPass123"
	)

	v := &PAMVerifier{Service: service}

	ok, err := v.Verify(user, password)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if !ok {
		t.Error("Verify returned false for correct password")
	}

	// Wrong password must return false, not error.
	ok, err = v.Verify(user, "wrongpassword")
	if err != nil {
		t.Fatalf("Verify(wrong) returned error: %v", err)
	}
	if ok {
		t.Error("Verify returned true for wrong password")
	}
}
