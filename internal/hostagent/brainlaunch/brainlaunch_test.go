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
//
// present is the default ImagePresent answer for any ref; presentByRef overrides
// it per-ref so a test can express "proxy image absent, brain image present"
// (the first-boot sequence loads each from its own tarball). Production's
// CLIDocker.ImagePresent already queries each ref independently — this keeps the
// fake honest to that.
type fakeDocker struct {
	present      bool
	presentByRef map[string]bool
	presentErr   error
	loadErr      error
	label        string
	labelErr     error
	exists       bool
	existsErr    error
	runErr       error
	netErr       error
	loadCalls    int
	loadPaths    []string
	runCalls     int
	netCalls     int
	lastRun      RunSpec
	runSpecs     []RunSpec
	lastNet      string
	lastLabelKey string
}

func newFake() *fakeDocker {
	return &fakeDocker{present: true, label: "1"}
}

func (f *fakeDocker) ImagePresent(_ context.Context, ref string) (bool, error) {
	if v, ok := f.presentByRef[ref]; ok {
		return v, f.presentErr
	}
	return f.present, f.presentErr
}
func (f *fakeDocker) Load(_ context.Context, path string) error {
	f.loadCalls++
	f.loadPaths = append(f.loadPaths, path)
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
	f.runSpecs = append(f.runSpecs, spec)
	return f.runErr
}
func (f *fakeDocker) NetworkCreate(_ context.Context, name string) error {
	f.netCalls++
	f.lastNet = name
	return f.netErr
}

func testConfig() Config {
	return Config{
		Image:         "malmo-brain:dev",
		ImageTar:      "/var/lib/malmo/brain-image.tar",
		ContainerName: "malmo-brain",
		DataDir:       "/var/lib/malmo",
		StateDir:      "/var/lib/malmo/state",
		SocketPath:    "/var/run/malmo/agent.sock",

		Network:            "malmo-ingress",
		ProxyImage:         "tecnativa/docker-socket-proxy:v0.4.2",
		ProxyImageTar:      "/var/lib/malmo/control-plane/images/docker-socket-proxy.tar",
		ProxyContainerName: "malmo-docker-proxy",
		ControlPlaneDir:    "/var/lib/malmo/control-plane",
		UIUpstream:         "malmo-ui:80",
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
	// M1b: the brain joins the ingress network and is pointed at the proxy +
	// Caddy + the staged control-plane compose. It must never get the raw socket.
	if s.Network != "malmo-ingress" {
		t.Errorf("brain network = %q, want malmo-ingress", s.Network)
	}
	if v := envVal(s.Env, "DOCKER_HOST"); v != "tcp://docker-proxy:2375" {
		t.Errorf("DOCKER_HOST = %q, want tcp://docker-proxy:2375", v)
	}
	if v := envVal(s.Env, "MALMO_CADDY_ADMIN"); v != "http://malmo-caddy:2019" {
		t.Errorf("MALMO_CADDY_ADMIN = %q, want http://malmo-caddy:2019", v)
	}
	if v := envVal(s.Env, "MALMO_CONTROL_PLANE_DIR"); v != "/var/lib/malmo/control-plane" {
		t.Errorf("MALMO_CONTROL_PLANE_DIR = %q", v)
	}
	if v := envVal(s.Env, "MALMO_DASHBOARD_UI_UPSTREAM"); v != "malmo-ui:80" {
		t.Errorf("MALMO_DASHBOARD_UI_UPSTREAM = %q, want malmo-ui:80", v)
	}
	if hasMount(s.Mounts, "/var/run/docker.sock", "/var/run/docker.sock") {
		t.Error("brain must NOT mount the raw Docker socket")
	}
}

func TestEnsureTransportSeedsNetworkAndProxy(t *testing.T) {
	f := newFake()
	f.exists = false // proxy not yet running

	if err := EnsureTransport(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("EnsureTransport: %v", err)
	}
	if f.netCalls != 1 || f.lastNet != "malmo-ingress" {
		t.Errorf("network create calls=%d last=%q, want 1 malmo-ingress", f.netCalls, f.lastNet)
	}
	if f.runCalls != 1 {
		t.Fatalf("run calls = %d, want 1 (proxy launched)", f.runCalls)
	}
	s := f.lastRun
	if s.Name != "malmo-docker-proxy" || s.Network != "malmo-ingress" {
		t.Errorf("proxy spec name/network = %q/%q", s.Name, s.Network)
	}
	// The brain dials the proxy by the docker-proxy alias regardless of the
	// container's malmo-prefixed name.
	if len(s.Aliases) != 1 || s.Aliases[0] != "docker-proxy" {
		t.Errorf("proxy aliases = %v, want [docker-proxy]", s.Aliases)
	}
	// The raw socket is mounted read-only into the proxy — the one place it is
	// exposed to a container.
	var sockRO bool
	for _, m := range s.Mounts {
		if m.Source == "/var/run/docker.sock" && m.Target == "/var/run/docker.sock" {
			sockRO = m.ReadOnly
		}
	}
	if !sockRO {
		t.Errorf("proxy socket mount must be read-only: %+v", s.Mounts)
	}
	// EXEC must stay denied (it is absent from the allowlist).
	if v := envVal(s.Env, "EXEC"); v != "" {
		t.Errorf("proxy EXEC = %q, want unset (denied)", v)
	}
	if envVal(s.Env, "POST") != "1" || envVal(s.Env, "CONTAINERS") != "1" {
		t.Errorf("proxy allowlist missing POST/CONTAINERS: %+v", s.Env)
	}
}

func TestEnsureTransportProxyExistsIsNoOp(t *testing.T) {
	f := newFake()
	f.exists = true // proxy already running (host-agent restart)

	if err := EnsureTransport(context.Background(), f, testConfig()); err != nil {
		t.Fatalf("EnsureTransport: %v", err)
	}
	if f.netCalls != 1 {
		t.Errorf("network create still ensured idempotently: calls=%d, want 1", f.netCalls)
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 (existing proxy left to Docker)", f.runCalls)
	}
}

func TestEnsureTransportLoadsAbsentProxyImage(t *testing.T) {
	f := newFake()
	f.present = false // proxy image not loaded yet

	cfg := testConfig()
	if err := EnsureTransport(context.Background(), f, cfg); err != nil {
		t.Fatalf("EnsureTransport: %v", err)
	}
	if f.loadCalls != 1 {
		t.Fatalf("load calls = %d, want 1 (absent proxy image loaded)", f.loadCalls)
	}
	if f.loadPaths[0] != cfg.ProxyImageTar {
		t.Errorf("loaded %q, want the proxy tarball %q", f.loadPaths[0], cfg.ProxyImageTar)
	}
	// A regression that loads the image and returns early (skipping Run) must
	// fail here — loading is not the end of the absent-image path, launching is.
	if f.runCalls != 1 {
		t.Fatalf("run calls = %d, want 1 (proxy launched after load)", f.runCalls)
	}
	if f.lastRun.Name != "malmo-docker-proxy" {
		t.Errorf("ran %q, want the proxy after loading its image", f.lastRun.Name)
	}
}

// TestFirstBootSequenceLoadsEachImageFromItsOwnTarball mirrors host-agent's
// first-boot order — EnsureTransport then Launch — on a fresh box where both the
// proxy and brain images are absent. It proves each is loaded from its *own*
// tarball (proxy ← ProxyImageTar, brain ← ImageTar), which a single shared
// present bool can't express: the per-ref fake distinguishes the two refs the
// way production's CLIDocker does.
func TestFirstBootSequenceLoadsEachImageFromItsOwnTarball(t *testing.T) {
	cfg := testConfig()
	f := newFake()
	f.presentByRef = map[string]bool{
		cfg.ProxyImage: false, // both absent on a fresh box → both load
		cfg.Image:      false,
	}

	if err := EnsureTransport(context.Background(), f, cfg); err != nil {
		t.Fatalf("EnsureTransport: %v", err)
	}
	if err := Launch(context.Background(), f, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	want := []string{cfg.ProxyImageTar, cfg.ImageTar}
	if len(f.loadPaths) != len(want) {
		t.Fatalf("load paths = %v, want %v", f.loadPaths, want)
	}
	for i, w := range want {
		if f.loadPaths[i] != w {
			t.Errorf("load[%d] = %q, want %q", i, f.loadPaths[i], w)
		}
	}
	// Proxy then brain — two distinct containers, in order.
	if len(f.runSpecs) != 2 || f.runSpecs[0].Name != "malmo-docker-proxy" || f.runSpecs[1].Name != "malmo-brain" {
		t.Errorf("ran %d containers (%+v), want [malmo-docker-proxy malmo-brain]", len(f.runSpecs), f.runSpecs)
	}
}

func TestEnsureTransportNetworkErrorPropagates(t *testing.T) {
	f := newFake()
	f.netErr = errors.New("daemon down")

	if err := EnsureTransport(context.Background(), f, testConfig()); err == nil {
		t.Fatal("want error when network create fails")
	}
	if f.runCalls != 0 {
		t.Errorf("run calls = %d, want 0 after a network-create failure", f.runCalls)
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
