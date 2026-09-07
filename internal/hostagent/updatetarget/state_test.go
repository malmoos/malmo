package updatetarget

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The snapshot exists so something other than the journal can say what the loop
// decided (#443). These tests pin the one property that matters: each of the
// four outcomes is reachable and stays distinct from the others. A source that
// is down and a source stuck on a bad answer must never read the same.

func TestSnapshot_NoTickYet(t *testing.T) {
	l := newLoop(&fakeSource{target: goodTarget()}, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, ptr(at(12, 3, 30)))
	s := l.Snapshot()
	if s.Outcome != "" || !s.CheckedAt.IsZero() {
		t.Fatalf("snapshot before the first tick = %+v, want the zero value", s)
	}
}

func TestSnapshot_Outcomes(t *testing.T) {
	unpinned := goodTarget()
	unpinned.BrainImage = "ghcr.io/malmoos/brain:v0.7.0"

	cases := []struct {
		name    string
		src     *fakeSource
		want    Outcome
		wantErr string // a substring of the recorded reason; "" means none
	}{
		{"a target", &fakeSource{target: goodTarget()}, OutcomeOK, ""},
		{"nothing on offer", &fakeSource{err: ErrNoTarget}, OutcomeNone, ""},
		{"source down", &fakeSource{err: errors.New("dial tcp: connection refused")}, OutcomeUnreachable, "connection refused"},
		{"a tagged answer", &fakeSource{target: unpinned}, OutcomeRefused, "not pinned"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := at(12, 3, 30)
			l := newLoop(c.src, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, &now)
			l.Tick(context.Background())

			s := l.Snapshot()
			if s.Outcome != c.want {
				t.Fatalf("outcome = %q, want %q", s.Outcome, c.want)
			}
			if !s.CheckedAt.Equal(now) {
				t.Errorf("checked at %v, want %v", s.CheckedAt, now)
			}
			if c.wantErr == "" {
				if s.Err != "" {
					t.Errorf("recorded reason %q, want none", s.Err)
				}
			} else if !strings.Contains(s.Err, c.wantErr) {
				t.Errorf("recorded reason %q, want it to mention %q", s.Err, c.wantErr)
			}
		})
	}
}

// A refused answer is kept, not dropped: an operator fixing a broken source
// needs to see what that source is actually serving.
func TestSnapshot_KeepsTheRefusedAnswer(t *testing.T) {
	unpinned := goodTarget()
	unpinned.BrainImage = "ghcr.io/malmoos/brain:v0.7.0"
	l := newLoop(&fakeSource{target: unpinned}, fakeRunning{brain: oldBrain, ui: oldUI}, &fakeApplier{}, ptr(at(12, 3, 30)))
	l.Tick(context.Background())

	if got := l.Snapshot().Target.BrainImage; got != unpinned.BrainImage {
		t.Fatalf("kept brain image %q, want the refused %q", got, unpinned.BrainImage)
	}
}

// The window in the snapshot is the one that would actually be used, so an
// answer that names its own outranks the box's setting here too (UPDATES.md
// # 8.4). Without this the report would show an hour the box no longer uses.
func TestSnapshot_WindowComesFromTheAnswer(t *testing.T) {
	tgt := goodTarget()
	tgt.Window = "05:00-06:00"
	l := newLoop(&fakeSource{target: tgt}, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, ptr(at(12, 3, 30)))
	l.Window, l.WindowFrom = Window{Start: 3 * time.Hour, End: 4 * time.Hour}, "env"
	l.Tick(context.Background())

	s := l.Snapshot()
	if got := s.Window.String(); got != "05:00-06:00" {
		t.Errorf("window %q, want the answer's 05:00-06:00", got)
	}
	if s.WindowFrom != fromAnswer {
		t.Errorf("window from %q, want %q", s.WindowFrom, fromAnswer)
	}
}

// A box with no answer still reports the window it is configured with, and where
// that came from. The dashboard says "installs tonight at ..." from this.
func TestSnapshot_WindowFallsBackToTheSetting(t *testing.T) {
	l := newLoop(&fakeSource{err: ErrNoTarget}, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, ptr(at(12, 3, 30)))
	l.Window, l.WindowFrom = Window{Start: 5 * time.Hour, End: 6 * time.Hour}, "env"
	l.Tick(context.Background())

	s := l.Snapshot()
	if got := s.Window.String(); got != "05:00-06:00" {
		t.Errorf("window %q, want the configured 05:00-06:00", got)
	}
	if s.WindowFrom != "env" {
		t.Errorf("window from %q, want %q", s.WindowFrom, "env")
	}
}

// An unset WindowFrom reports the built-in default rather than an empty string:
// the wire field is read by a person, and "" says nothing.
func TestSnapshot_WindowFromDefaults(t *testing.T) {
	l := newLoop(&fakeSource{err: ErrNoTarget}, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, ptr(at(12, 3, 30)))
	l.Tick(context.Background())
	if got := l.Snapshot().WindowFrom; got != fromDefault {
		t.Fatalf("window from %q, want %q", got, fromDefault)
	}
}

// The writer is the tick and the reader is an HTTP handler, so the two run at
// once on a real box. Under -race this is the test that says so.
func TestSnapshot_ConcurrentReadAndTick(t *testing.T) {
	now := at(12, 3, 30)
	l := newLoop(&fakeSource{target: goodTarget()}, fakeRunning{brain: brainRef, ui: uiRef}, &fakeApplier{}, &now)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			l.Tick(context.Background())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = l.Snapshot()
		}
	}()
	wg.Wait()
}
