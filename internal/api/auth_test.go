package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/malmo/malmo/internal/audit"
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
	st   *store.Store
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
	roles := map[string]string{}
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
	mux.HandleFunc("POST /v1/auth/set-role", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SetRoleRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		pmu.Lock()
		roles[req.User] = req.Role
		pmu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
	})
	mux.HandleFunc("POST /v1/auth/delete-user", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.DeleteUserRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		pmu.Lock()
		delete(pwds, req.User)
		pmu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
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
	srv := NewServer(st, cat, nil, bus, authMgr, host, audit.New(st))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := newJar()
	return &harness{srv: ts, jar: jar, t: t, pwds: pwds, pmu: &pmu, st: st}
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
		auth.NewManager(st), hostclient.New(sock), audit.New(st))
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

func TestListAuditAdminSeesAll(t *testing.T) {
	h := newHarness(t)
	// Setup creates the first admin (records setup.complete) and leaves
	// a valid session cookie in h.jar.
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "alice", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Login records login.success.
	resp = h.do("POST", "/api/v1/login", map[string]string{
		"username": "alice", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do("GET", "/api/v1/audit", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/v1/audit = %d; want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Events []AuditEventDTO `json:"events"`
	}](t, resp)
	if len(body.Events) < 2 {
		t.Fatalf("admin audit: got %d events, want >= 2", len(body.Events))
	}
	// Newest first — last login.success should appear before setup.complete.
	if body.Events[0].Action != "login.success" {
		t.Errorf("first event action = %q, want login.success", body.Events[0].Action)
	}
}

func TestListAuditRequiresAuth(t *testing.T) {
	h := newHarness(t)
	// Use the raw client (no cookie jar) to verify 401.
	resp, err := http.Get(h.srv.URL + "/api/v1/audit")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated audit = %d; want 401", resp.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		remoteAddr string
		xff        string
		want       string
	}{
		{"192.168.1.1:54321", "", "192.168.1.1"},
		{"192.168.1.1:54321", "10.0.0.5", "10.0.0.5"},
		{"192.168.1.1:54321", "10.0.0.5, 172.16.0.1", "10.0.0.5"},
		{"[::1]:54321", "", "::1"},
	}
	for _, tc := range cases {
		r := &http.Request{
			RemoteAddr: tc.remoteAddr,
			Header:     http.Header{},
		}
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		got := clientIP(r)
		if got != tc.want {
			t.Errorf("remoteAddr=%q xff=%q: got %q, want %q", tc.remoteAddr, tc.xff, got, tc.want)
		}
	}
}

// addMember writes a member user directly into the store and seeds the fake
// host-agent's password map. There's no create-user API yet (Tier-A item 2),
// so tests that need a second user bypass the wire.
func (h *harness) addMember(id, username, password string) {
	h.t.Helper()
	if err := h.st.CreateUser(store.User{
		ID: id, Username: username, Role: store.RoleMember, CreatedAt: time.Now(),
	}); err != nil {
		h.t.Fatalf("create member: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	h.pmu.Lock()
	h.pwds[username] = hash
	h.pmu.Unlock()
}

func TestListAuditMemberVisibility(t *testing.T) {
	h := newHarness(t)

	// Admin bootstrap records setup.complete (target = admin user).
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "alice", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	resp.Body.Close()

	alice, err := h.st.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("lookup alice: %v", err)
	}
	// Admin installs an app — synthesized directly since the lifecycle is nil
	// in the harness. Bob must not see this row (actor=alice, target=app).
	if err := h.st.InsertAuditEvent(store.AuditEvent{
		TS: time.Now().UnixMilli(), ActorUserID: alice.ID, ActorRole: "admin",
		Action: "app.install", TargetKind: "app", TargetID: "inst_xyz", Success: true,
	}); err != nil {
		t.Fatalf("seed admin row: %v", err)
	}

	const bobID = "u_bob"
	h.addMember(bobID, "bob", "bobpass")

	// Switch sessions: logout admin, login as bob (records login.success
	// with actor=bob, target=user:bob).
	h.do("POST", "/api/v1/logout", nil).Body.Close()
	resp = h.do("POST", "/api/v1/login", map[string]string{
		"username": "bob", "password": "bobpass",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("bob login: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do("GET", "/api/v1/audit", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("bob GET /audit: %d", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Events []AuditEventDTO `json:"events"`
	}](t, resp)

	if len(body.Events) == 0 {
		t.Fatal("bob should see at least their own login.success")
	}
	for _, e := range body.Events {
		actorIsBob := e.ActorUserID == bobID
		targetIsBob := e.TargetKind == "user" && e.TargetID == bobID
		if !actorIsBob && !targetIsBob {
			t.Errorf("bob sees unrelated event: action=%s actor=%q target=%s/%s",
				e.Action, e.ActorUserID, e.TargetKind, e.TargetID)
		}
	}
}

func TestListAuditLimitClamped(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "alice", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	resp.Body.Close()

	for i := 0; i < 250; i++ {
		if err := h.st.InsertAuditEvent(store.AuditEvent{
			TS: int64(i), ActorRole: "system", Action: "login.success", Success: true,
		}); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	resp = h.do("GET", "/api/v1/audit?limit=500", nil)
	defer resp.Body.Close()
	body := decodeJSON[struct {
		Events []AuditEventDTO `json:"events"`
	}](t, resp)
	if len(body.Events) != maxAuditLimit {
		t.Fatalf("limit=500 with 250+ rows returned %d events, want %d (clamped)",
			len(body.Events), maxAuditLimit)
	}
}

func TestListAuditCursorPagination(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/api/v1/setup", map[string]string{
		"username": "alice", "password": "hunter2",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	resp.Body.Close()

	for i := 0; i < 5; i++ {
		_ = h.st.InsertAuditEvent(store.AuditEvent{
			TS: int64(i), ActorRole: "system", Action: "login.success", Success: true,
		})
	}

	resp = h.do("GET", "/api/v1/audit?limit=3", nil)
	page1 := decodeJSON[struct {
		Events []AuditEventDTO `json:"events"`
	}](t, resp)
	if len(page1.Events) != 3 {
		t.Fatalf("page 1: %d events, want 3", len(page1.Events))
	}
	cursor := page1.Events[2].ID

	resp = h.do("GET", fmt.Sprintf("/api/v1/audit?limit=3&after_id=%d", cursor), nil)
	page2 := decodeJSON[struct {
		Events []AuditEventDTO `json:"events"`
	}](t, resp)
	for _, e := range page2.Events {
		if e.ID >= cursor {
			t.Errorf("page 2 event id %d should be < cursor %d", e.ID, cursor)
		}
	}
	// setup.complete + 5 seeded = 6 admin-visible rows. Page 1 returns
	// ids 6,5,4 (cursor=4); page 2 returns ids 3,2,1.
	if len(page2.Events) != 3 {
		t.Fatalf("page 2: %d events, want 3", len(page2.Events))
	}
}
