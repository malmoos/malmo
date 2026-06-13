package brainlaunch

import (
	"context"
	"errors"
	"testing"

	"github.com/malmoos/malmo/internal/protocol"
)

// fakeDocker records calls and returns programmed results so Launch's sequence
// is exercised with no Docker daemon. Zero value: image present, label matches
// protocol.Major, container absent — the steady "image already loaded, fresh
// box" path; tests override the fields they're probing.
type fakeDocker struct {
	present      bool
	presentErr   error
	loadErr      error
	label        string
	labelErr     error
	exists       bool
	existsErr    error
	runErr       error
	loadCalls    int
	runCalls     int
	lastRun      RunSpec
	lastLabelKey string
}

func newFake() *fakeDocker {
	return &fakeDocker{present: true, label: "1"}
}

func (f *fakeDocker) ImagePresent(context.Context, string) (bool, error) {
	return f.present, f.presentErr
}
func (f *fakeDocker) Load(context.Context, string) error {
	f.loadCalls++
	return f.loadErr
}
func (f *fakeDocker) ImageLabel(_ context.Context, _, label string) (string, error) {
	f.lastLabelKey = label
	return f.label, f.labelErr
}
func (f *fakeDocker) ContainerExists(context.Context, string) (bool, error) {
	return f.exists, f.existsErr
}
func (f *fakeDocker) Run(_ context.Context, spec RunSpec) error {
	f.runCalls++
	f.lastRun = spec
	return f.runErr
}

func testConfig() Config {
	return Config{
		Image:         "malmo-brain:dev",
		ImageTar:      "/var/lib/malmo/brain-image.tar",
		ContainerName: "malmo-brain",
		DataDir:       "/var/lib/malmo",
		StateDir:      "/var/lib/malmo/state",
		SocketPath:    "/var/run/malmo/agent.sock",
	}
}

func TestLaunchImageAbsentLoadsThenRuns(t *testing.T) {
	f := newFake()
	f.present = false // image not loaded yet → docker load

	if err := Launch(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if f.loadCalls != 1 {
		t.Errorf("load calls = %d, want 1 (absent image must be loaded)", f.loadCalls)
	}
	if f.runCalls != 1 {
		t.Errorf("run calls = %d, want 1", f.runCalls)
	}
	if f.lastLabelKey != protocol.ImageProtocolMajorLabel {
		t.Errorf("read label %q, want %q", f.lastLabelKey, protocol.ImageProtocolMajorLabel)
	}
}

func TestLaunchImagePresentSkipsLoad(t *testing.T) {
	f := newFake() // present=true

	if err := Launch(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if f.loadCalls != 0 {
		t.Errorf("load calls = %d, want 0 (present image must not be reloaded)", f.loadCalls)
	}
	if f.runCalls != 1 {
		t.Errorf("run calls = %d, want 1", f.runCalls)
	}
}

func TestLaunchRunSpec(t *testing.T) {
	f := newFake()
	if err := Launch(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	s := f.lastRun
	if s.Name != "malmo-brain" || s.Image != "malmo-brain:dev" {
		t.Errorf("run spec name/image = %q/%q", s.Name, s.Image)
	}
	if s.Restart != "unless-stopped" {
		t.Errorf("restart policy = %q, want unless-stopped", s.Restart)
	}
	if !hasMount(s.Mounts, "/var/run/malmo", "/var/run/malmo") {
		t.Errorf("missing host-agent socket-dir mount: %+v", s.Mounts)
	}
	if !hasMount(s.Mounts, "/var/lib/malmo", "/var/lib/malmo") {
		t.Errorf("missing data-dir mount: %+v", s.Mounts)
	}
	if v := envVal(s.Env, "MALMO_STATE_DIR"); v != "/var/lib/malmo/state" {
		t.Errorf("MALMO_STATE_DIR = %q, want /var/lib/malmo/state", v)
	}
	if v := envVal(s.Env, "MALMO_AGENT_SOCK"); v != "/var/run/malmo/agent.sock" {
		t.Errorf("MALMO_AGENT_SOCK = %q, want the socket path", v)
	}
}

func TestLaunchProtocolMismatchRefuses(t *testing.T) {
	f := newFake()
	f.label = "2" // brain speaks a major this host-agent doesn't

	err := Launch(context.Background(), f, testConfig())
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("err = %v, want ErrProtocolMismatch", err)
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 (mismatch must refuse launch)", f.runCalls)
	}
}

func TestLaunchMissingLabelRefuses(t *testing.T) {
	f := newFake()
	f.label = "" // image carries no protocol label → cannot verify → refuse

	err := Launch(context.Background(), f, testConfig())
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("err = %v, want ErrProtocolMismatch", err)
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0", f.runCalls)
	}
}

func TestLaunchContainerExistsIsNoOp(t *testing.T) {
	f := newFake()
	f.exists = true // brain already running (host-agent restart)

	if err := Launch(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 (existing brain left to Docker)", f.runCalls)
	}
}

func TestLaunchLoadErrorPropagates(t *testing.T) {
	f := newFake()
	f.present = false
	f.loadErr = errors.New("disk full")

	err := Launch(context.Background(), f, testConfig())
	if err == nil {
		t.Fatal("want error when docker load fails")
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 (no run after a failed load)", f.runCalls)
	}
}

func TestLaunchImagePresentErrorPropagates(t *testing.T) {
	f := newFake()
	f.presentErr = errors.New("docker daemon unreachable")

	if err := Launch(context.Background(), f, testConfig()); err == nil {
		t.Fatal("want error when the image check fails")
	}
	if f.loadCalls != 0 || f.runCalls != 0 {
		t.Errorf("load/run calls = %d/%d, want 0/0 on an image-check error", f.loadCalls, f.runCalls)
	}
}

func TestLaunchContainerCheckErrorPropagates(t *testing.T) {
	f := newFake()
	f.existsErr = errors.New("docker ps failed")

	if err := Launch(context.Background(), f, testConfig()); err == nil {
		t.Fatal("want error when the container check fails")
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 when the container check errors", f.runCalls)
	}
}

func TestLaunchRunErrorPropagates(t *testing.T) {
	f := newFake()
	f.runErr = errors.New("no such image")

	if err := Launch(context.Background(), f, testConfig()); err == nil {
		t.Fatal("want error when docker run fails")
	}
	if f.runCalls != 1 {
		t.Errorf("run calls = %d, want 1 (run was attempted)", f.runCalls)
	}
}

func hasMount(ms []Mount, src, tgt string) bool {
	for _, m := range ms {
		if m.Source == src && m.Target == tgt {
			return true
		}
	}
	return false
}

func envVal(es []EnvVar, key string) string {
	for _, e := range es {
		if e.Key == key {
			return e.Value
		}
	}
	return ""
}
