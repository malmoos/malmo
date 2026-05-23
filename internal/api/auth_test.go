package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/malmo/malmo/internal/auth"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/hostclient"
	"github.com/malmo/malmo/internal/protocol"
	"github.com/malmo/malmo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// harness wires a real api.Server to a real store and a real (in-memory)
// host-agent stand-in over a unix socket. We deliberately don't mock the
// store or hostclient — those have their own tests, and the value of these
// tests is exercising the full HTTP boundary (CORS → auth middleware →
// huma handler → store + host) end-to-end.
type harness struct {
	srv  *httptest.Server
	jar  http.CookieJar // shared cookie jar across helpers
	t    *testing.T
	pwds map[string][]byte
	pmu  *sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Spin up an in-process host-agent on a unix socket implementing only
	// the auth surface. We don't need discovery for auth tests.
	pwds := map[string][]byte{}
	var pmu sync.Mutex
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/set-password", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SetPasswordRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		h, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
		pmu.Lock()
		pwds[req.User] = h
		pmu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
	})
	mux.HandleFunc("POST /v1/auth/verify-password", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.VerifyPasswordRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		pmu.Lock()
		h, ok := pwds[req.User]
		pmu.Unlock()
		valid := ok && bcrypt.CompareHashAndPassword(h, []byte(req.Password)) == nil
		_ = json.NewEncoder(w).Encode(protocol.VerifyPasswordResponse{Valid: valid})
	})
	hostHTTP := &http.Server{Handler: mux}
	go func() { _ = hostHTTP.Serve(ln) }()
	t.Cleanup(func() { _ = hostHTTP.Close() })

	host := hostclient.New(sock)
	cat := catalog.New(t.TempDir())
	bus := events.NewBus()
	authMgr := auth.NewManager(st)

	// life is nil — install/uninstall handlers aren't exercised here. The
	// auth middleware fences them anyway; we only assert that fence.
	srv := NewServer(st, cat, nil, bus, authMgr, host)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := newJar()
	return &harness{srv: ts, jar: jar, t: t, pwds: pwds, pmu: &pmu}
}

func (h *harness) do(method, path string, body any) *http.Response {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, rdr)
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range h.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	if cs := resp.Cookies(); len(cs) > 0 {
		h.jar.SetCookies(req.URL, cs)
	}
	return resp
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	return v
}

func newJar() (http.CookieJar, error) {
	return cookiejar.New(nil)
}

// --- tests ---------------------------------------------------------------

func TestAuthStateProgression(t *testing.T) {
	h := newHarness(t)

	// Fresh box: has_users is false, no auth required.
	resp := h.do("GET", "/api/v1/auth/state", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("auth/state on fresh box: %d", resp.StatusCode)
	}
	body := decodeJSON[struct {
		HasUsers bool `json:"has_users"`
	}](t, resp)
	if body.HasUsers {
		t.Fatal("fresh box reported has_users=true")
	}

	// Setup the admin.
	resp = h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	setupBody := decodeJSON[struct {
		User         UserDTO `json:"user"`
		RecoveryCode string  `json:"recovery_code"`
	}](t, resp)
	if setupBody.User.Username != "andrei" || setupBody.User.Role != store.RoleAdmin {
		t.Fatalf("setup user = %+v", setupBody.User)
	}
	if len(setupBody.RecoveryCode) != 24 {
		t.Fatalf("recovery code len = %d; want 24", len(setupBody.RecoveryCode))
	}

	// After setup, the same endpoint should refuse with 409.
	resp = h.do("POST", "/api/v1/setup", map[string]string{
		"username": "cindy", "password": "doesntmatter",
	})
	if resp.StatusCode != 409 {
		t.Fatalf("second setup = %d; want 409", resp.StatusCode)
	}

	// auth/state now reports true, still public.
	resp = h.do("GET", "/api/v1/auth/state", nil)
	body = decodeJSON[struct {
		HasUsers bool `json:"has_users"`
	}](t, resp)
	if !body.HasUsers {
		t.Fatal("after setup, has_users=false")
	}
}

func TestProtectedRoutesRequireSession(t *testing.T) {
	h := newHarness(t)
	// No setup, no session: protected route 401s.
	resp := h.do("GET", "/api/v1/apps", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("apps unauthenticated = %d; want 401", resp.StatusCode)
	}
	resp = h.do("GET", "/api/v1/me", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("me unauthenticated = %d; want 401", resp.StatusCode)
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	h := newHarness(t)
	// Bootstrap admin so we have credentials to log in with.
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// /v1/setup also issues a session, so we're already logged in. /me works.
	resp = h.do("GET", "/api/v1/me", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("me after setup = %d", resp.StatusCode)
	}
	me := decodeJSON[UserDTO](t, resp)
	if me.Username != "andrei" {
		t.Fatalf("me = %+v", me)
	}

	// Logout clears the session. huma returns 204 (no body) which is fine.
	resp = h.do("POST", "/api/v1/logout", nil)
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.do("GET", "/api/v1/me", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("me after logout = %d; want 401", resp.StatusCode)
	}

	// Wrong password -> 401, no session minted.
	resp = h.do("POST", "/api/v1/login", map[string]string{
		"username": "andrei", "password": "wrong",
	})
	if resp.StatusCode != 401 {
		t.Fatalf("bad login = %d; want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown user -> 401 (no leakage of which usernames exist).
	resp = h.do("POST", "/api/v1/login", map[string]string{
		"username": "ghost", "password": "anything",
	})
	if resp.StatusCode != 401 {
		t.Fatalf("ghost login = %d; want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Correct password -> 200, session minted, /me works again.
	resp = h.do("POST", "/api/v1/login", map[string]string{
		"username": "andrei", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.do("GET", "/api/v1/me", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("me after relogin = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSetupRollsBackOnHostFailure(t *testing.T) {
	h := newHarness(t)
	// Replace the host-agent stand-in's handler with one that always 500s
	// for set-password — exercises the rollback path. Easiest way: shut
	// down the existing harness host server and assert that setup fails and
	// the user row was rolled back. We can do that by killing one entry in
	// the password map's underlying mutex … or, simpler: point the brain
	// at a dead socket via a NEW harness with no agent.
	// Approach: replace via a tighter harness. Inline below.
	st, err := store.Open(filepath.Join(t.TempDir(), "rb.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// host-agent that 500s set-password.
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/set-password", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"boom","message":"nope"}`, 500)
	})
	hostHTTP := &http.Server{Handler: mux}
	go func() { _ = hostHTTP.Serve(ln) }()
	t.Cleanup(func() { _ = hostHTTP.Close() })

	srv := NewServer(st, catalog.New(t.TempDir()), nil, events.NewBus(),
		auth.NewManager(st), hostclient.New(sock))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/v1/setup", "application/json",
		bytes.NewReader([]byte(`{"username":"andrei","password":"hunter2"}`)))
	if err != nil {
		t.Fatalf("setup post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("setup with broken host = %d; want 502", resp.StatusCode)
	}
	// Row must have been rolled back: HasAnyUser is false again.
	if has, _ := st.HasAnyUser(); has {
		t.Fatal("user row survived host failure; rollback broken")
	}
	_ = h
}
