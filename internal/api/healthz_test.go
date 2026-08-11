package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The probe answers without a session. host-agent has no session and cannot get
// one, so a 401 here would make the endpoint useless to its only caller.
//
// The server is built with **every dependency nil** on purpose. That is the
// second half of the contract: liveness must not consult the store, the host
// client, the auth manager, the lifecycle manager, or anything else that could
// be sick while the brain itself is perfectly able to serve. If this handler
// ever grows a dependency check, this test panics rather than quietly turning
// the updater's commit signal into a report on some unrelated subsystem.
func TestHealthzIsPublicAndDependencyFree(t *testing.T) {
	srv := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached 200 lets a probe read a dead brain as live", got)
	}
}

// The probe is not throttled. The updater polls it once a second for up to 60s
// (UPDATES.md # 3 step 3d) while plane 2 allows 30 req/min/IP, so without the
// exemption the wait would 429 partway through and the updater would revert a
// healthy brain. 90 requests is past both the burst and a minute's budget.
func TestHealthzIsExemptFromTheIPRateLimit(t *testing.T) {
	srv := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for i := 0; i < 90; i++ {
		resp, err := http.Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz #%d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /healthz #%d: status = %d, want 200 (throttled probe = reverted update)", i, resp.StatusCode)
		}
	}
}

// The probe is operational, not product: it must stay out of the versioned API
// surface, or it lands in api/openapi.yaml and the generated TS client and
// becomes a compatibility promise to the dashboard.
func TestHealthzIsAbsentFromTheOpenAPIDocument(t *testing.T) {
	if _, found := OpenAPIDocument().Paths[healthzPath]; found {
		t.Errorf("%q appears in the OpenAPI document; it must stay outside the versioned surface", healthzPath)
	}
}
