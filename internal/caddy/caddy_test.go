package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingAdmin is a stand-in for Caddy's admin API: it records the method +
// path + decoded body of each request and answers a programmable status per
// path so the client's request shaping can be asserted without a real Caddy.
type recordingAdmin struct {
	mu     sync.Mutex
	calls  []adminCall
	status map[string]int // path → status to return on GET (default 200)
}

type adminCall struct {
	method string
	path   string
	body   map[string]any
}

func (a *recordingAdmin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
		}
		a.mu.Lock()
		a.calls = append(a.calls, adminCall{method: r.Method, path: r.URL.Path, body: body})
		st := http.StatusOK
		if s, ok := a.status[r.URL.Path]; ok {
			st = s
		}
		a.mu.Unlock()
		w.WriteHeader(st)
	})
}

func (a *recordingAdmin) find(method, pathContains string) *adminCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.calls {
		c := a.calls[i]
		if c.method == method && strings.Contains(c.path, pathContains) {
			return &c
		}
	}
	return nil
}

// index returns the position of the first call matching method + pathContains
// in call order, or -1 if there is none. Used to assert ordering between two
// calls (e.g. the phase-1 PUT before the phase-2 POST).
func (a *recordingAdmin) index(method, pathContains string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.calls {
		c := a.calls[i]
		if c.method == method && strings.Contains(c.path, pathContains) {
			return i
		}
	}
	return -1
}

func TestEnsureDashboardInstallsSplitRoute(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)

	if err := c.EnsureDashboard(context.Background(), "malmo.local", "malmo-brain:8080", "malmo-ui:80"); err != nil {
		t.Fatalf("EnsureDashboard: %v", err)
	}

	// Idempotency: it deletes any prior route by @id before inserting.
	if admin.find("DELETE", "/id/"+dashboardRouteID) == nil {
		t.Error("expected a DELETE of the dashboard route @id before insert")
	}
	// The route is PUT at index 0 so it sorts before the catch-all.
	put := admin.find("PUT", "/routes/0")
	if put == nil {
		t.Fatal("expected a PUT to routes/0 for the dashboard route")
	}
	if put.body["@id"] != dashboardRouteID {
		t.Errorf("route @id = %v, want %s", put.body["@id"], dashboardRouteID)
	}

	// Walk into the subroute and assert the /api leg targets the brain with
	// buffering disabled and the fallback leg targets the UI.
	handle := put.body["handle"].([]any)[0].(map[string]any)
	if handle["handler"] != "subroute" {
		t.Fatalf("dashboard handler = %v, want subroute", handle["handler"])
	}
	routes := handle["routes"].([]any)
	apiLeg := routes[0].(map[string]any)
	apiMatch := apiLeg["match"].([]any)[0].(map[string]any)
	apiPaths := apiMatch["path"].([]any)
	if len(apiPaths) != 2 || apiPaths[0] != "/api/*" || apiPaths[1] != "/_malmo/*" {
		t.Errorf("api leg paths = %v, want [/api/* /_malmo/*]", apiPaths)
	}
	apiProxy := apiLeg["handle"].([]any)[0].(map[string]any)
	if apiProxy["handler"] != "reverse_proxy" {
		t.Errorf("api leg handler = %v", apiProxy["handler"])
	}
	if apiProxy["flush_interval"].(float64) != -1 {
		t.Errorf("api leg flush_interval = %v, want -1 (SSE buffering off)", apiProxy["flush_interval"])
	}
	if dial := apiProxy["upstreams"].([]any)[0].(map[string]any)["dial"]; dial != "malmo-brain:8080" {
		t.Errorf("api leg dial = %v, want malmo-brain:8080", dial)
	}
	uiLeg := routes[1].(map[string]any)
	if _, hasMatch := uiLeg["match"]; hasMatch {
		t.Error("ui fallback leg should have no match (catch-all within the host)")
	}
	uiProxy := uiLeg["handle"].([]any)[0].(map[string]any)
	if dial := uiProxy["upstreams"].([]any)[0].(map[string]any)["dial"]; dial != "malmo-ui:80" {
		t.Errorf("ui leg dial = %v, want malmo-ui:80", dial)
	}
}

// alwaysCertReady stubs Client.certReady so tests never dial a real :443 or
// wait on real ACME: EnsureWildcardTLS's phase-1-then-phase-2 sequencing is
// what's under test, not the readiness probe's own network behavior (that
// would need a real Caddy + ACME to exercise honestly).
func alwaysCertReady(context.Context, string) (bool, error) { return true, nil }

func TestEnsureWildcardTLS(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)
	c.certReady = alwaysCertReady

	subjects := []string{"cindy-fox.malmo.network", "*.cindy-fox.malmo.network"}
	enr := EnrollmentCredentials{Subdomain: "abc-123", Username: "u", Password: "p"}
	if err := c.EnsureWildcardTLS(context.Background(), subjects, "https://auth.malmo.network", enr); err != nil {
		t.Fatalf("EnsureWildcardTLS: %v", err)
	}

	// Idempotent remove-then-put of the tls app.
	if admin.find("DELETE", "/config/apps/tls") == nil {
		t.Error("expected a DELETE of the tls app before PUT")
	}

	// Phase 1: the wildcard alone, as the only policy in the initial PUT.
	put := admin.find("PUT", "/config/apps/tls")
	if put == nil {
		t.Fatal("expected a PUT to /config/apps/tls")
	}
	policies := put.body["automation"].(map[string]any)["policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("phase-1 PUT policies = %v, want exactly 1 (wildcard only)", policies)
	}
	policy := policies[0].(map[string]any)
	gotSubjects := policy["subjects"].([]any)
	if len(gotSubjects) != 1 || gotSubjects[0] != "*.cindy-fox.malmo.network" {
		t.Errorf("phase-1 policy subjects = %v, want [*.cindy-fox.malmo.network]", gotSubjects)
	}
	assertACMEIssuer(t, policy, "https://auth.malmo.network", "u", "p", "abc-123")

	// The :443 listener is added alongside :80, in phase 1.
	patch := admin.find("PATCH", "/servers/malmo/listen")
	if patch == nil {
		t.Fatal("expected a PATCH of the server listen array")
	}

	// Phase 2: the base subject is appended as its own policy, only after the
	// wildcard's readiness probe reports ready.
	appendCall := admin.find("POST", "/config/apps/tls/automation/policies")
	if appendCall == nil {
		t.Fatal("expected a POST appending the base-subject policy")
	}
	baseSubjects := appendCall.body["subjects"].([]any)
	if len(baseSubjects) != 1 || baseSubjects[0] != "cindy-fox.malmo.network" {
		t.Errorf("phase-2 policy subjects = %v, want [cindy-fox.malmo.network]", baseSubjects)
	}
	assertACMEIssuer(t, appendCall.body, "https://auth.malmo.network", "u", "p", "abc-123")

	// Phase ordering: the wildcard PUT must precede the base POST. The whole
	// point is that the base subject's order never starts until the
	// wildcard's has finished.
	putIdx, postIdx := admin.index("PUT", "/config/apps/tls"), admin.index("POST", "/config/apps/tls/automation/policies")
	if putIdx < 0 || postIdx < 0 || putIdx > postIdx {
		t.Errorf("expected the wildcard PUT (call %d) before the base-subject POST (call %d)", putIdx, postIdx)
	}
}

// assertACMEIssuer checks the shared acme-dns issuer shape embedded in a
// single-subject policy body.
func assertACMEIssuer(t *testing.T, policy map[string]any, wantEndpoint, wantUser, wantPass, wantSubdomain string) {
	t.Helper()
	issuer := policy["issuers"].([]any)[0].(map[string]any)
	if issuer["module"] != "acme" {
		t.Errorf("issuer module = %v, want acme", issuer["module"])
	}
	provider := issuer["challenges"].(map[string]any)["dns"].(map[string]any)["provider"].(map[string]any)
	if provider["name"] != "acmedns" {
		t.Errorf("dns provider = %v, want acmedns", provider["name"])
	}
	if provider["server_url"] != wantEndpoint {
		t.Errorf("provider server_url = %v, want %s", provider["server_url"], wantEndpoint)
	}
	if provider["username"] != wantUser || provider["password"] != wantPass || provider["subdomain"] != wantSubdomain {
		t.Errorf("provider creds = %v, want {%s %s %s}", provider, wantSubdomain, wantUser, wantPass)
	}
}

// TestEnsureWildcardTLSWaitsForWildcardBeforeBase asserts the concurrency fix
// directly: while the wildcard's cert isn't ready yet, the base-subject phase
// must not run at all, even though phase 1 (the wildcard PUT + :443 listener)
// already has.
func TestEnsureWildcardTLSWaitsForWildcardBeforeBase(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)
	c.certPollInterval = time.Millisecond

	var probes int
	const readyAfter = 3
	c.certReady = func(context.Context, string) (bool, error) {
		probes++
		return probes >= readyAfter, nil
	}

	subjects := []string{"cindy-fox.malmo.network", "*.cindy-fox.malmo.network"}
	enr := EnrollmentCredentials{Subdomain: "abc-123", Username: "u", Password: "p"}
	if err := c.EnsureWildcardTLS(context.Background(), subjects, "https://auth.malmo.network", enr); err != nil {
		t.Fatalf("EnsureWildcardTLS: %v", err)
	}
	if probes < readyAfter {
		t.Errorf("probes = %d, want >= %d (waitForCert should keep polling until ready)", probes, readyAfter)
	}
	if admin.find("POST", "/config/apps/tls/automation/policies") == nil {
		t.Error("expected the base-subject POST once certReady finally reported true")
	}
}

// TestEnsureWildcardTLSTimesOutIfWildcardNeverIssues asserts the bounded-wait
// requirement: if the wildcard subject never becomes ready (DNS/ACME stuck),
// EnsureWildcardTLS returns an error instead of hanging, and, critically,
// never runs the base-subject phase, since that phase must never start
// concurrently with an unresolved wildcard order.
func TestEnsureWildcardTLSTimesOutIfWildcardNeverIssues(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)
	c.certReady = func(context.Context, string) (bool, error) { return false, nil }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	subjects := []string{"cindy-fox.malmo.network", "*.cindy-fox.malmo.network"}
	enr := EnrollmentCredentials{Subdomain: "abc-123", Username: "u", Password: "p"}
	err := c.EnsureWildcardTLS(ctx, subjects, "https://auth.malmo.network", enr)
	if err == nil {
		t.Fatal("want a timeout error when the wildcard cert never becomes ready")
	}
	if admin.find("POST", "/config/apps/tls/automation/policies") != nil {
		t.Error("base-subject phase must not run when the wildcard phase never completed")
	}
}

func TestWaitReadyReturnsWhenAdminAnswers(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady against a live admin: %v", err)
	}
}

func TestWaitReadyTimesOutWhenAdminDown(t *testing.T) {
	// Point at a closed server so every probe fails the transport dial.
	srv := httptest.NewServer(http.NewServeMux())
	url := srv.URL
	srv.Close()
	c := New(url)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err == nil {
		t.Fatal("want a timeout error when the admin API never answers")
	}
}
