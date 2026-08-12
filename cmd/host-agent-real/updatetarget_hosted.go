//go:build hosted

package main

import (
	"os"

	"github.com/malmoos/malmo/internal/hostagent/relmanifest"
	"github.com/malmoos/malmo/internal/hostagent/updatetarget"
	"github.com/malmoos/malmo/internal/profile"
)

// updateTargetSource is the **hosted** half of the update-target seam: the box
// reads its target from the cloud over its existing outbound path (UPDATES.md
// # 8.1 — the box asks, nothing connects in), and applies it without a prompt
// (# 8.2 — we operate the box, so an unpatched one is our liability).
//
// The URL is configuration, not a constant. MALMO_UPDATE_TARGET_URL points a box
// at an alternative source, which is how a release is proven on one box before
// the fleet gets it, and how the boot proof drives this loop against a server
// inside the guest. Empty means the control plane's public endpoint.
//
// The release-manifest poller is unused here and is always nil on this build:
// a hosted box has one opinion about its target, and a signed public broadcast
// would be a second.
func updateTargetSource(*relmanifest.Poller) (src updatetarget.Source, autoApply bool, name string) {
	return updatetarget.HTTPSource{URL: os.Getenv("MALMO_UPDATE_TARGET_URL")}, true, string(profile.Hosted)
}
