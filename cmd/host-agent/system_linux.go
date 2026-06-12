//go:build linux

package main

import (
	"github.com/molmaos/molma/internal/hostagent"
	"github.com/molmaos/molma/internal/hostagent/procsource"
)

// newSystemSampler returns a real /proc-backed sampler on Linux so make dev
// shows live CPU and RAM rather than synthetic counters.
func newSystemSampler() hostagent.SystemSampler {
	return procsource.New()
}
