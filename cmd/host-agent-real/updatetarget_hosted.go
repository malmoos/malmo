//go:build hosted

package main

import (
	"log/slog"

	"github.com/malmoos/malmo/internal/hostagent/relmanifest"
	"github.com/malmoos/malmo/internal/hostagent/updatetarget"
	"github.com/malmoos/malmo/internal/profile"
)

// updateTargetSource is the **hosted** half of the update-target seam: the box
// reads its target from the cloud over its existing outbound path (UPDATES.md
// # 8.1 — the box asks, nothing connects in), and applies it without a prompt
// (# 8.2 — we operate the box, so an unpatched one is our liability).
//
// The URL is configuration, not a constant, and it is settable **per box at
// provision time** through the seed's update_target_url field (updateconfig.go,
// UPDATES.md # 8.4). That is how one box proves a release before the fleet gets
// it: a hosted box has no SSH, and the seed is the only channel a real box has
// for a per-box fact. MALMO_UPDATE_TARGET_URL stays underneath it as the local
// hand-edit the boot proof uses. Empty means the control plane's public endpoint.
//
// An unusable seeded target, and a seed that will not parse, are returned as an
// error rather than resolved away, so the caller can refuse instead of quietly
// reading the fleet default.
//
// The release-manifest poller is unused here and is always nil on this build:
// a hosted box has one opinion about its target, and a signed public broadcast
// would be a second.
func updateTargetSource(*relmanifest.Poller) (src updatetarget.Source, autoApply bool, name string, err error) {
	target, from, err := updateTargetURL()
	if err != nil {
		return nil, false, "", err
	}
	shown := target
	if shown == "" {
		shown = updatetarget.DefaultURL
	}
	if from == fromDefault {
		slog.Info("update target resolved", "url", shown, "from", from)
	} else {
		// Loud on purpose. "This box is not following the fleet" is the first
		// thing anyone needs to know when one box behaves unlike the rest, and
		// it has to be visible without knowing to go looking for it.
		slog.Warn("this box is not following the fleet update target", "url", shown, "from", from)
	}
	return updatetarget.HTTPSource{URL: target}, true, string(profile.Hosted), nil
}
