package updatetarget

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/malmoos/malmo/internal/hostagent/controlplane"
)

// PollInterval is how often the box asks its source what it should be running.
//
// It is deliberately **shorter than the update window**. The window is an hour
// wide, so an hourly poll could land at 02:58 and again at 04:01 and step
// straight over it, and the box would then wait a full day for a target it
// already knew about. Four ticks per window is enough that jitter, a slow
// source, or a clock adjustment cannot swallow all of them.
const PollInterval = 15 * time.Minute

// pollJitter spreads each tick, for the same reason relmanifest's poll does:
// boxes cluster after a provider maintenance window or a regional power cut,
// and the whole fleet is pointed at one endpoint.
const pollJitter = 2 * time.Minute

// Applier starts the control-plane update transaction. Consumer-side interface
// (CLAUDE.md # Go code discipline); the provider is *hostagent.Agent, which
// starts the same job POST /v1/jobs/system-update starts.
//
// **It must go through host-agent's job lock, not straight to cpupdate.** An
// admin-triggered update and a target-driven one are the same dangerous
// operation, and two of them at once on one box is the thing that lock exists to
// prevent. A refusal here ("a job is already running") is an ordinary outcome,
// not a failure of this loop.
type Applier interface {
	// StartUpdate begins an update to the given refs and returns the job id. The
	// job runs in the background: a nil error means the transaction started, not
	// that it succeeded.
	StartUpdate(brainRef, uiRef string) (jobID string, err error)
}

// RunningPair reports the control-plane images the box is running right now, so
// the loop can tell "the target moved" from "nothing to do".
type RunningPair interface {
	Running() (brain, ui string, err error)
}

// LedgerPair reads the running pair from the box's own declaration: the ledger
// for the brain, the staged compose for the UI (UPDATES.md # 8.3 — the two files
// that carry the pair). It is the same pair cpupdate reverts to, read from the
// same place, so the loop's comparison and the transaction's cannot drift.
type LedgerPair struct {
	// Dir is MALMO_CONTROL_PLANE_DIR.
	Dir string
	// BrainDefault is the ref this box shipped with (MALMO_BRAIN_IMAGE), used
	// before the first update has written a ledger.
	BrainDefault string
}

// Running reports the declared pair.
func (l LedgerPair) Running() (brain, ui string, err error) {
	brain, _ = controlplane.ResolveBrainImage(l.Dir, l.BrainDefault)
	ui, err = controlplane.ReadUIImage(l.Dir)
	if err != nil {
		return "", "", err
	}
	return brain, ui, nil
}

// Loop is the **single consumer of the Source seam**: it asks, compares,
// validates, and applies inside the window. Both profiles run this one loop; all
// that differs is the Source they are given and whether they may apply without
// asking.
type Loop struct {
	// Source answers what this box should be running.
	Source Source
	// Current reports what it is running.
	Current RunningPair
	// Applier starts the transaction. Nil means the loop can only observe,
	// which is also what AutoApply=false means.
	Applier Applier
	// Repos is the expected home of each image; the zero value means
	// DefaultRepositories.
	Repos Repositories
	// Window is when an update may start; the zero value means DefaultWindow.
	Window Window
	// AutoApply is the # 8.2 difference between the profiles. Hosted is true:
	// we operate the box, so it patches itself and the tenant is told
	// afterwards. Appliance is false: the control plane is admin-prompted
	// (# 3), so the box learns its target and waits for a human.
	AutoApply bool
	// Profile is the environment profile this loop runs on (ENVIRONMENT.md),
	// carried only so the logs say which one made a decision.
	Profile string
	// Interval between polls; zero means PollInterval.
	Interval time.Duration
	// Now and After exist so a test can drive a week of polling in
	// milliseconds. Both default to the real clock.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time

	// lastQuiet dedupes the steady-state messages. The overwhelmingly common
	// tick is "the target is what I am already running", and that must cost
	// nothing and say nothing.
	lastQuiet string
	// attempted is the target version this loop last started an update for, and
	// the window occurrence it did it in. One attempt per window per version: a
	// failed update reverts the box, so the next tick sees the same difference
	// again, and without this the box would retry a bad target every quarter of
	// an hour until the window closed.
	attempted struct {
		version string
		window  time.Time
	}
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) window() Window {
	if l.Window == (Window{}) {
		return DefaultWindow
	}
	return l.Window
}

func (l *Loop) repos() Repositories {
	if l.Repos == (Repositories{}) {
		return DefaultRepositories
	}
	return l.Repos
}

func (l *Loop) interval() time.Duration {
	if l.Interval > 0 {
		return l.Interval
	}
	return PollInterval
}

func (l *Loop) after(d time.Duration) <-chan time.Time {
	if l.After != nil {
		return l.After(d)
	}
	return time.After(d)
}

func (l *Loop) nextDelay() time.Duration {
	base := l.interval()
	j := pollJitter
	if j > base/2 {
		j = base / 2 // keep a short test interval from going negative
	}
	d := base + time.Duration(rand.Int63n(int64(2*j)+1)) - j
	if d < time.Second {
		d = time.Second
	}
	return d
}

// quiet logs msg only when this tick's steady state differs from the last one.
func (l *Loop) quiet(key, msg string, args ...any) {
	if l.lastQuiet == key {
		return
	}
	l.lastQuiet = key
	slog.Info(msg, args...)
}

// Tick runs one cycle: ask, compare, and apply if everything lines up. It never
// returns an error — there is no caller to hand one to, and none of the ways
// this can go wrong is a reason for the box to do anything but keep running what
// it runs.
func (l *Loop) Tick(ctx context.Context) {
	t, err := l.Source.Target(ctx)
	switch {
	case errors.Is(err, ErrNoTarget):
		// A source with nothing to offer is not a broken source, and it is
		// certainly not "there is nothing to run". Distinguishable from the
		// unreachable case below, which is the point.
		l.quiet("no-target", "update target: the source has no target; staying on the current version")
		return
	case err != nil:
		// Unreachable, a 500, an unparseable answer. Log it and carry on: a box
		// must never degrade because it could not ask.
		slog.Warn("update target: could not read the source; staying on the current version", "err", err)
		return
	}

	// Untrusted input, validated before anything is pulled. A refusal is loud
	// and repeated: an operator needs to see that a box has been declining its
	// target since Tuesday, and a source stuck on a bad answer is a fleet-wide
	// problem, not a quiet one.
	if err := t.Validate(l.repos()); err != nil {
		slog.Error("update target: refusing the answer; nothing pulled, box unchanged",
			"err", err, "brain", t.Version, "image", t.BrainImage)
		return
	}

	brain, ui, err := l.Current.Running()
	if err != nil {
		slog.Warn("update target: cannot read what this box is running", "err", err)
		return
	}
	if brain == t.BrainImage && ui == t.UIImage {
		// The overwhelmingly common case. No pull, no work, and one line the
		// first time it is true.
		l.quiet("current:"+t.Version, "update target: already on the target version", "brain", t.Version)
		return
	}

	if !l.AutoApply || l.Applier == nil {
		// Appliance: the box now knows its target and an admin decides
		// (UPDATES.md # 3). Surfacing that as a dashboard prompt is a separate
		// slice; this is where the fact enters the box.
		l.quiet("available:"+t.Version, "update target: a different control plane is available",
			"brain", t.Version, "image", t.BrainImage)
		return
	}

	now := l.now()
	w := l.window()
	if !w.Contains(now) {
		l.quiet("holding:"+t.Version, "update target: holding a new version for the update window",
			"brain", t.Version, "step", "waiting", "zone", now.Location().String())
		return
	}
	if occ := w.Occurrence(now); l.attempted.version == t.Version && !occ.After(l.attempted.window) {
		// Already tried this version tonight. If it failed it has already put
		// the box back, and the next window is soon enough to try again.
		return
	}

	l.attempted.version, l.attempted.window = t.Version, w.Occurrence(now)
	l.lastQuiet = ""
	jobID, err := l.Applier.StartUpdate(t.BrainImage, t.UIImage)
	if err != nil {
		// Includes the ordinary "a job is already running" refusal, which is why
		// this is a Warn and not an Error.
		slog.Warn("update target: could not start the update", "err", err, "brain", t.Version)
		return
	}
	slog.Info("update target: applying", "brain", t.Version, "image", t.BrainImage,
		"step", "accepted", "job_id", jobID)
}

// Run ticks until ctx is done: once at the start, then on the jittered interval.
//
// The first tick is immediate so a box that has been off for a week learns its
// target as soon as it is up, but it still only **applies** inside the window,
// so booting at noon does not restart the control plane at noon.
func (l *Loop) Run(ctx context.Context) {
	slog.Info("update target: polling", "interval", l.interval().String(), "profile", l.Profile)
	for {
		l.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-l.after(l.nextDelay()):
		}
	}
}
