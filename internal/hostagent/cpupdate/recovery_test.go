package cpupdate

import (
	"context"
	"os"
	"testing"
)

// TestSnapshotFailureRestoresBrainOnDeadContext is the regression test for the
// one recovery path that was not detached (found reviewing #381).
//
// The window: the old brain container has been removed so its database can be
// copied, the snapshot then fails, and the caller's context is already dead —
// the 30-minute MaxDuration expiring mid-snapshot is the realistic way there.
// Recovery has to start the old brain again, and it cannot do that on a
// cancelled context: `docker` would refuse instantly and the box would be left
// with **no brain container at all** and no automatic way back.
//
// The Docker fake fails on a dead context the way exec.CommandContext does,
// which is what makes this test non-vacuous — a fake that ignored the context
// would pass with the fix removed.
func TestSnapshotFailureRestoresBrainOnDeadContext(t *testing.T) {
	o := setup(t)
	o.BrainRef = newBrain
	// Make the snapshot fail: SnapshotRoot is a regular file, so creating the
	// generation's directory under it cannot work.
	root := o.SnapshotRoot
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed snapshot root: %v", err)
	}

	d, p := newFakeDocker(), &fakeProber{failURLs: map[string]error{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Kill the context at the exact moment the brain container is removed: the
	// pull has happened, the snapshot has not.
	d.beforeCall = func(call string) {
		if call == "rm:"+o.BrainCfg.ContainerName {
			cancel()
		}
	}

	res, err := Apply(ctx, d, p, o)
	if err == nil {
		t.Fatal("Apply: want the snapshot failure to surface")
	}
	if res.FailureMode != "snapshot" {
		t.Fatalf("failure mode = %q, want snapshot", res.FailureMode)
	}
	if res.RevertErr != nil {
		t.Fatalf("recovery failed on a dead context: %v", res.RevertErr)
	}
	if !d.has("run:" + oldBrain) {
		t.Fatalf("the old brain was never started again — the box is left with no brain; calls were %v", d.calls)
	}
}
