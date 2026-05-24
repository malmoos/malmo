//go:build !(linux && cgo)

// Package pamverifier implements PasswordVerifier using the system PAM stack.
// This stub is compiled on non-Linux or CGO-disabled builds so that the package
// is always importable; the real implementation requires linux + cgo.
package pamverifier

import "errors"

// PAMVerifier is a stub on non-Linux or CGO-disabled builds.
// Real PAM is only available on Linux with CGO enabled.
type PAMVerifier struct {
	Service string
}

func (p *PAMVerifier) Verify(_, _ string) (bool, error) {
	return false, errors.New("PAMVerifier is not available on this platform (requires linux + cgo)")
}
