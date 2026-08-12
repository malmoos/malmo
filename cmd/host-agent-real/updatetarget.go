package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/malmoos/malmo/internal/hostagent"
	"github.com/malmoos/malmo/internal/hostagent/brainlaunch"
	"github.com/malmoos/malmo/internal/hostagent/relmanifest"
	"github.com/malmoos/malmo/internal/hostagent/updatetarget"
)

// startUpdateTarget starts the loop that learns this box's control-plane target
// and applies it, and returns a stop function.
//
// **This file is the one place the update-target seam is consumed.** The profile
// decides where the answer comes from (updateTargetSource, build-tagged) and
// whether the box may act on it alone; everything after that — the compare, the
// validation, the window, the apply, the failure handling — is one loop shared by
// both profiles. A second copy of any of it, per profile, is what UPDATES.md # 8
// means by "we only build it once".
func startUpdateTarget(brainCfg brainlaunch.Config, a *hostagent.Agent, poller *relmanifest.Poller) func() {
	src, autoApply, name, err := updateTargetSource(poller)
	if err != nil {
		// Refuse; do not fall back. The box was pointed somewhere deliberately
		// and we cannot honour it, so running the loop against the fleet default
		// would quietly move a pinned box onto stable (updateconfig.go). No loop
		// means the box keeps serving whatever it is already running.
		slog.Error("update target is not usable; this box will not update itself", "err", err)
		return func() {}
	}

	window, windowFrom := updateWindow()

	loop := &updatetarget.Loop{
		Source: src,
		Current: updatetarget.LedgerPair{
			Dir: brainCfg.ControlPlaneDir,
			// What this box shipped with, used until an update writes a ledger.
			// The same value brainlaunch used to launch the brain, so the loop
			// compares against the ref actually running.
			BrainDefault: brainCfg.Image,
		},
		Applier:   agentApplier{a},
		Repos:     repositories(),
		Window:    window,
		AutoApply: autoApply,
		Profile:   name,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	slog.Info("update target loop started", "profile", name, "window", window.String(), "from", windowFrom)
	return cancel
}

// repositories is where this box expects its control-plane images to come from.
// A pinned digest is only as trustworthy as the repository it is pulled from, so
// a source that named the right digest shape in the wrong place is refused
// (updatetarget.Validate).
//
// Overridable because the CI boot proof serves both images from a registry
// inside the guest, and because a box under test may be pointed elsewhere.
func repositories() updatetarget.Repositories {
	r := updatetarget.DefaultRepositories
	if v := os.Getenv("MALMO_UPDATE_BRAIN_REPO"); v != "" {
		r.Brain = v
	}
	if v := os.Getenv("MALMO_UPDATE_UI_REPO"); v != "" {
		r.UI = v
	}
	return r
}

// agentApplier adapts the agent's job surface to the loop's Applier seam. The
// loop wants a job id and an error; the agent hands back the whole record.
//
// Going through the agent rather than straight to cpupdate is deliberate: it is
// what puts a target-driven update under the same single job lock as an
// admin-triggered one (hostagent.Agent.StartUpdate).
type agentApplier struct{ a *hostagent.Agent }

func (ap agentApplier) StartUpdate(brainRef, uiRef string) (string, error) {
	job, err := ap.a.StartUpdate(brainRef, uiRef)
	if err != nil {
		return "", err
	}
	return job.ID, nil
}
