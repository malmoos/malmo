package cpupdate

import (
	"context"
	"errors"
	"testing"
)

// TestRunnerAppliesRequestRefs: the target comes from the request, and nothing
// else about the box does. Base is the box's own config; only the two refs are
// per-update.
func TestRunnerAppliesRequestRefs(t *testing.T) {
	o := setup(t)
	d, p := newFakeDocker(), &fakeProber{failURLs: map[string]error{}}
	r := Runner{Docker: d, Prober: p, Base: o}

	res, err := r.Update(context.Background(), newBrain, newUI)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.BrainChanged || !res.UIChanged || res.Reverted {
		t.Fatalf("result = %+v, want both changed and no revert", res)
	}
	if !d.has("pull:" + newBrain) {
		t.Errorf("the requested brain ref never reached docker; calls were %v", d.calls)
	}
	if got := ledgerOf(t, o); got.Current.Brain != newBrain || got.Current.UI != newUI {
		t.Errorf("ledger current = %+v, want the requested pair", got.Current)
	}
}

// TestRunnerNothingToDo: asking for the pair the box already runs is success
// with nothing changed, not an error. A retry after a flaky network must not
// look like a broken update.
func TestRunnerNothingToDo(t *testing.T) {
	o := setup(t)
	d, p := newFakeDocker(), &fakeProber{failURLs: map[string]error{}}
	r := Runner{Docker: d, Prober: p, Base: o}

	res, err := r.Update(context.Background(), oldBrain, oldUI)
	if err != nil {
		t.Fatalf("Update on the running pair: want success, got %v", err)
	}
	if res.BrainChanged || res.UIChanged {
		t.Errorf("result = %+v, want nothing changed", res)
	}
	if len(d.calls) != 0 {
		t.Errorf("a no-op update touched docker: %v", d.calls)
	}
}

// TestRunnerFailureCarriesMode: a failed transaction reports the error *and*
// what it did about it. Losing either half would leave the admin with "it
// broke" and no idea whether the box was put back.
func TestRunnerFailureCarriesMode(t *testing.T) {
	o := setup(t)
	d, p := newFakeDocker(), &fakeProber{failURLs: map[string]error{}}
	d.failOn["pull:"+newBrain] = errors.New("no such image")
	r := Runner{Docker: d, Prober: p, Base: o}

	res, err := r.Update(context.Background(), newBrain, "")
	if err == nil {
		t.Fatal("Update: want the pull failure to surface")
	}
	if res.FailureMode != "pull" {
		t.Errorf("failure mode = %q, want pull", res.FailureMode)
	}
	if !res.BrainChanged {
		t.Errorf("result = %+v, want the attempted change recorded", res)
	}
}

// TestRunnerRevertErrorSurfaces: when the rollback itself fails, the box is in
// a state no automatic path can fix. That has to reach the wire — it is the
// only signal an operator gets.
func TestRunnerRevertErrorSurfaces(t *testing.T) {
	o := setup(t)
	d := newFakeDocker()
	p := &fakeProber{failURLs: map[string]error{}}
	// Health fails, so the transaction reverts; the revert's own brain start
	// then fails too.
	p.failURLs["http://10.0.0.1:8080/healthz"] = errors.New("connection refused")
	d.failOn["run:"+oldBrain] = errors.New("daemon is gone")
	r := Runner{Docker: d, Prober: p, Base: o}

	res, err := r.Update(context.Background(), newBrain, "")
	if err == nil {
		t.Fatal("Update: want the health failure to surface")
	}
	if !res.Reverted || res.FailureMode != "health" {
		t.Fatalf("result = %+v, want a reverted health failure", res)
	}
	if res.RevertError == "" {
		t.Error("the revert failed but the wire result says nothing about it")
	}
}
