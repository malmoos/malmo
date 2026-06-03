package lifecycle

import (
	"context"
	"fmt"
	"sync"

	"github.com/malmo/malmo/internal/protocol"
)

// call records one driver invocation across any fake. We keep it as a flat
// `<method>(<arg1>,<arg2>,…)` string so assertions can substring-match without
// caring about call shape; tests that need exact args inspect typed fields on
// the fake instead.
type call struct {
	method string
	args   []any
}

func (c call) String() string {
	parts := make([]string, len(c.args))
	for i, a := range c.args {
		parts[i] = fmt.Sprintf("%v", a)
	}
	out := c.method + "("
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out + ")"
}

// fakeDocker is a scriptable, recording stand-in for DockerDriver.
//
// Defaults: every operation succeeds. The interesting tests set one field
// (e.g. PullErr) to drive a specific failure mode.
type fakeDocker struct {
	mu sync.Mutex

	digests       map[string]string // image → digest returned by ImageInspect
	pullErr       map[string]error  // per-image Pull error (nil = success)
	composeUp     func(dir, project string) (string, error)
	inspect       func(id, mainService string) (running bool, health string, err error)
	psManaged     map[string]bool // returned by PSManaged
	restartCounts map[string]int  // returned by RestartCounts

	composeUpErr   error // simple "always fail compose up"
	composeDownErr error
	composeStopErr error

	calls []call
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		digests:   map[string]string{},
		pullErr:   map[string]error{},
		psManaged: map[string]bool{},
	}
}

func (f *fakeDocker) record(method string, args ...any) {
	f.mu.Lock()
	f.calls = append(f.calls, call{method: method, args: args})
	f.mu.Unlock()
}

func (f *fakeDocker) Calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]call, len(f.calls))
	copy(out, f.calls)
	return out
}

// methods records all methods called, in order.
func (f *fakeDocker) methods() []string {
	cs := f.Calls()
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.method
	}
	return out
}

func (f *fakeDocker) called(method string) bool {
	for _, m := range f.methods() {
		if m == method {
			return true
		}
	}
	return false
}

func (f *fakeDocker) Pull(_ context.Context, image string) error {
	f.record("Pull", image)
	return f.pullErr[image]
}

func (f *fakeDocker) ImageInspect(_ context.Context, image string) (RepoDigests, error) {
	f.record("ImageInspect", image)
	d, ok := f.digests[image]
	if !ok {
		return nil, fmt.Errorf("fakeDocker: no digest scripted for %q", image)
	}
	return RepoDigests{repoOf(image) + "@" + d}, nil
}

func (f *fakeDocker) ComposeUp(_ context.Context, dir, project string) (string, error) {
	f.record("ComposeUp", dir, project)
	if f.composeUp != nil {
		return f.composeUp(dir, project)
	}
	if f.composeUpErr != nil {
		return "boom", f.composeUpErr
	}
	return "", nil
}

func (f *fakeDocker) ComposeDown(_ context.Context, dir, project string) (string, error) {
	f.record("ComposeDown", dir, project)
	return "", f.composeDownErr
}

func (f *fakeDocker) ComposeStop(_ context.Context, dir, project string) (string, error) {
	f.record("ComposeStop", dir, project)
	return "", f.composeStopErr
}

func (f *fakeDocker) Inspect(_ context.Context, id, main string) (bool, string, error) {
	f.record("Inspect", id, main)
	if f.inspect != nil {
		return f.inspect(id, main)
	}
	return true, "healthy", nil
}

func (f *fakeDocker) NetworkCreate(_ context.Context, name string, internal bool) error {
	f.record("NetworkCreate", name, internal)
	return nil
}

func (f *fakeDocker) NetworkRemove(_ context.Context, name string) error {
	f.record("NetworkRemove", name)
	return nil
}

func (f *fakeDocker) PSManaged(_ context.Context) (map[string]bool, error) {
	f.record("PSManaged")
	out := make(map[string]bool, len(f.psManaged))
	for k, v := range f.psManaged {
		out[k] = v
	}
	return out, nil
}

func (f *fakeDocker) RestartCounts(_ context.Context) (map[string]int, error) {
	f.record("RestartCounts")
	out := make(map[string]int, len(f.restartCounts))
	for k, v := range f.restartCounts {
		out[k] = v
	}
	return out, nil
}

func (f *fakeDocker) RemoveContainersByInstance(_ context.Context, id string) error {
	f.record("RemoveContainersByInstance", id)
	return nil
}

func (f *fakeDocker) RemoveImage(_ context.Context, ref string) error {
	f.record("RemoveImage", ref)
	return nil
}

// --- caddy fake ----------------------------------------------------------

type fakeCaddy struct {
	mu     sync.Mutex
	routes map[string]string // instanceID → "splash:<state>" | "upstream:<addr>"
	calls  []call
}

func newFakeCaddy() *fakeCaddy { return &fakeCaddy{routes: map[string]string{}} }

func (c *fakeCaddy) record(method string, args ...any) {
	c.mu.Lock()
	c.calls = append(c.calls, call{method: method, args: args})
	c.mu.Unlock()
}

func (c *fakeCaddy) EnsureServer(context.Context, string) error {
	c.record("EnsureServer")
	return nil
}

func (c *fakeCaddy) AddRoute(_ context.Context, id, host, upstream string) error {
	c.record("AddRoute", id, host, upstream)
	c.mu.Lock()
	c.routes[id] = "upstream:" + upstream
	c.mu.Unlock()
	return nil
}

func (c *fakeCaddy) AddSplashRoute(_ context.Context, id, host, name, state string) error {
	c.record("AddSplashRoute", id, host, name, state)
	c.mu.Lock()
	c.routes[id] = "splash:" + state
	c.mu.Unlock()
	return nil
}

func (c *fakeCaddy) RemoveRoute(_ context.Context, id string) error {
	c.record("RemoveRoute", id)
	c.mu.Lock()
	delete(c.routes, id)
	c.mu.Unlock()
	return nil
}

func (c *fakeCaddy) route(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.routes[id]
}

func (c *fakeCaddy) called(method string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range c.calls {
		if k.method == method {
			return true
		}
	}
	return false
}

// --- host fake -----------------------------------------------------------

type fakeHost struct {
	mu        sync.Mutex
	published map[string]bool
	calls     []call

	// resolveHomeErr, when set, is returned by ResolveHome (e.g.
	// hostclient.ErrUnknownUser to exercise the deleted-owner path).
	resolveHomeErr error
}

func newFakeHost() *fakeHost { return &fakeHost{published: map[string]bool{}} }

func (h *fakeHost) Publish(_ context.Context, slug string) (protocol.PublishResponse, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call{method: "Publish", args: []any{slug}})
	h.published[slug] = true
	h.mu.Unlock()
	return protocol.PublishResponse{Name: slug + ".local"}, nil
}

func (h *fakeHost) Unpublish(_ context.Context, slug string) error {
	h.mu.Lock()
	h.calls = append(h.calls, call{method: "Unpublish", args: []any{slug}})
	delete(h.published, slug)
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) ResolveHome(_ context.Context, user string) (protocol.ResolveHomeResponse, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call{method: "ResolveHome", args: []any{user}})
	h.mu.Unlock()
	if h.resolveHomeErr != nil {
		return protocol.ResolveHomeResponse{}, h.resolveHomeErr
	}
	return protocol.ResolveHomeResponse{HomePath: "/home/" + user, UID: 3000, GID: 3000}, nil
}

func (h *fakeHost) WellKnownIdentity(_ context.Context) (protocol.WellKnownIdentityResponse, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call{method: "WellKnownIdentity"})
	h.mu.Unlock()
	return protocol.WellKnownIdentityResponse{MalmoAppUID: 2000, MalmoAppGID: 2000, MalmoSharedGID: 2001}, nil
}

func (h *fakeHost) isPublished(slug string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.published[slug]
}

func (h *fakeHost) called(method string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if c.method == method {
			return true
		}
	}
	return false
}
