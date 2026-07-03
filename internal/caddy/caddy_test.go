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

func TestEnsureWildcardTLS(t *testing.T) {
	admin := &recordingAdmin{}
	srv := httptest.NewServer(admin.handler())
	defer srv.Close()
	c := New(srv.URL)

	subjects := []string{"cindy-fox.malmo.network", "*.cindy-fox.malmo.network"}
	enr := EnrollmentCredentials{Subdomain: "abc-123", Username: "u", Password: "p"}
	if err := c.EnsureWildcardTLS(context.Background(), subjects, "https://auth.malmo.network", enr); err != nil {
		t.Fatalf("EnsureWildcardTLS: %v", err)
	}

	// Idempotent remove-then-put of the tls app.
	if admin.find("DELETE", "/config/apps/tls") == nil {
		t.Error("expected a DELETE of the tls app before PUT")
	}

	put := admin.find("PUT", "/config/apps/tls")
	if put == nil {
		t.Fatal("expected a PUT to /config/apps/tls")
	}

	// The wildcard must be listed in certificates.automate — the whole fix. A
	// policy alone only says *how* to manage a matching name; without an automate
	// entry Caddy never places the wildcard order (the live #278 symptom).
	automate := put.body["certificates"].(map[string]any)["automate"].([]any)
	if len(automate) != 1 || automate[0] != "*.cindy-fox.malmo.network" {
		t.Errorf("certificates.automate = %v, want [*.cindy-fox.malmo.network]", automate)
	}

	// Exactly one automation policy — the wildcard, pinned to the acme-dns issuer.
	policies := put.body["automation"].(map[string]any)["policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("policies = %v, want exactly 1 (wildcard only)", policies)
	}
	policy := policies[0].(map[string]any)
	gotSubjects := policy["subjects"].([]any)
	if len(gotSubjects) != 1 || gotSubjects[0] != "*.cindy-fox.malmo.network" {
		t.Errorf("policy subjects = %v, want [*.cindy-fox.malmo.network]", gotSubjects)
	}
	assertACMEIssuer(t, policy, "https://auth.malmo.network", "u", "p", "abc-123")

	// The :443 listener is added alongside :80.
	if admin.find("PATCH", "/servers/malmo/listen") == nil {
		t.Fatal("expected a PATCH of the server listen array")
	}

	// The base subject is NOT routed through acme-dns: it is served by Caddy's
	// default issuer (tls-alpn-01/http-01), so no base policy is ever POSTed and
	// only one acme-dns order (the wildcard's) ever runs.
	if admin.find("POST", "/config/apps/tls/automation/policies") != nil {
		t.Error("did not expect a base-subject policy POST (base is served via the default issuer)")
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

// TestSplitCertSubjects covers both the happy path (order-independent) and the
// error path EnsureWildcardTLS relies on to reject a malformed subjects list
// before configuring anything.
func TestSplitCertSubjects(t *testing.T) {
	cases := []struct {
		name         string
		subjects     []string
		wantWildcard string
		wantBase     string
		wantErr      bool
	}{
		{
			name:         "base then wildcard",
			subjects:     []string{"cindy-fox.malmo.network", "*.cindy-fox.malmo.network"},
			wantWildcard: "*.cindy-fox.malmo.network",
			wantBase:     "cindy-fox.malmo.network",
		},
		{
			name:         "wildcard then base",
			subjects:     []string{"*.cindy-fox.malmo.network", "cindy-fox.malmo.network"},
			wantWildcard: "*.cindy-fox.malmo.network",
			wantBase:     "cindy-fox.malmo.network",
		},
		{
			name:     "no wildcard",
			subjects: []string{"cindy-fox.malmo.network"},
			wantErr:  true,
		},
		{
			name:     "no base",
			subjects: []string{"*.cindy-fox.malmo.network"},
			wantErr:  true,
		},
		{
			name:     "empty",
			subjects: nil,
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wildcard, base, err := splitCertSubjects(tc.subjects)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitCertSubjects(%v) = %q, %q, nil; want an error", tc.subjects, wildcard, base)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitCertSubjects(%v): %v", tc.subjects, err)
			}
			if wildcard != tc.wantWildcard || base != tc.wantBase {
				t.Errorf("splitCertSubjects(%v) = %q, %q; want %q, %q", tc.subjects, wildcard, base, tc.wantWildcard, tc.wantBase)
			}
		})
	}
}
