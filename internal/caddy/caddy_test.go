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
	if got := apiMatch["path"].([]any)[0]; got != "/api/*" {
		t.Errorf("api leg path = %v, want /api/*", got)
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
	policy := put.body["automation"].(map[string]any)["policies"].([]any)[0].(map[string]any)
	gotSubjects := policy["subjects"].([]any)
	if len(gotSubjects) != 2 || gotSubjects[0] != subjects[0] || gotSubjects[1] != subjects[1] {
		t.Errorf("policy subjects = %v, want %v", gotSubjects, subjects)
	}
	issuer := policy["issuers"].([]any)[0].(map[string]any)
	if issuer["module"] != "acme" {
		t.Errorf("issuer module = %v, want acme", issuer["module"])
	}
	provider := issuer["challenges"].(map[string]any)["dns"].(map[string]any)["provider"].(map[string]any)
	if provider["name"] != "acmedns" {
		t.Errorf("dns provider = %v, want acmedns", provider["name"])
	}
	if provider["server_url"] != "https://auth.malmo.network" {
		t.Errorf("provider server_url = %v", provider["server_url"])
	}
	if provider["username"] != "u" || provider["password"] != "p" || provider["subdomain"] != "abc-123" {
		t.Errorf("provider creds = %v, want {abc-123 u p}", provider)
	}

	// The :443 listener is added alongside :80.
	patch := admin.find("PATCH", "/servers/malmo/listen")
	if patch == nil {
		t.Fatal("expected a PATCH of the server listen array")
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
