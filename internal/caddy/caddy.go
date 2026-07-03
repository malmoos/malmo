// Package caddy drives the Caddy admin API (CONTROL_PLANE.md: "Configured via
// Caddy's admin API on localhost:2019 ... no Caddyfile on disk"). Skeleton
// scope: per-instance reverse-proxy routes keyed by Host header, added/removed
// on install/uninstall. All ops are best-effort in dev — if Caddy is down the
// brain logs and continues so the install spine still works.
package caddy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// catchAllBody is the HTML returned by the catch-all 404 route for any
// unmatched hostname. Kept as a raw string so the Go source is readable;
// serialised to a single JSON string value when sent to the Caddy admin API.
const catchAllBody = `<!doctype html><html><head><meta charset="utf-8"><title>404 — malmo</title><style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem;color:#222}h1{margin:0 0 .5rem;font-size:1.5rem}p{color:#555;line-height:1.5}a{color:#06c}</style></head><body><h1>404 — No app at this hostname</h1><p>The address you tried doesn't match any installed app on this malmo box.</p><p><a href="http://malmo.local">Go to the dashboard</a></p></body></html>`

type Client struct {
	admin string
	http  *http.Client
	// certReady reports whether Caddy has already obtained and loaded a
	// certificate whose SANs include wildcardSubject. The real implementation
	// (dialCertReady) probes the box's own already-listening :443; tests
	// replace this field with a stub so EnsureWildcardTLS's two-phase
	// sequencing (wildcard first, then base) can be asserted without a real
	// Caddy or ACME.
	certReady func(ctx context.Context, wildcardSubject string) (bool, error)
	// certPollInterval is how often waitForCert re-probes certReady. Tests
	// shrink this so a fake certReady that takes a few probes to report ready
	// doesn't make the test suite slow.
	certPollInterval time.Duration
}

func New(adminAddr string) *Client {
	c := &Client{
		admin:            adminAddr,
		http:             &http.Client{Timeout: 5 * time.Second},
		certPollInterval: wildcardPollInterval,
	}
	c.certReady = c.dialCertReady
	return c
}

// routeID is the stable @id we attach to each route so we can delete it later.
func routeID(instanceID string) string { return "malmo-app-" + instanceID }

// AddRoute registers Host(host) -> reverse_proxy(upstream): the real upstream
// once the app is healthy. upstream is "host:port" reachable from the Caddy
// container (the main_service's alias on the malmo-ingress network).
func (c *Client) AddRoute(ctx context.Context, instanceID, host, upstream string) error {
	return c.upsertRoute(ctx, instanceID, host, []any{
		map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": []any{map[string]any{"dial": upstream}},
		},
	})
}

// AddSplashRoute registers Host(host) -> a malmo-served splash page for the
// given lifecycle state (APP_LIFECYCLE.md # register early, with a splash).
// The brain owns the route from install-time so the hostname never returns
// connection-refused; the upstream is flipped to the real container once
// healthy. State is one of "starting" | "stopped" | "failed".
func (c *Client) AddSplashRoute(ctx context.Context, instanceID, host, appName, state string) error {
	return c.upsertRoute(ctx, instanceID, host, []any{
		map[string]any{
			"handler":     "static_response",
			"status_code": 200,
			"headers":     map[string]any{"Content-Type": []string{"text/html; charset=utf-8"}},
			"body":        splashHTML(appName, state),
		},
	})
}

// upsertRoute replaces any existing route for the instance (remove-then-add),
// so callers can flip a route's handler without duplicate-@id errors.
func (c *Client) upsertRoute(ctx context.Context, instanceID, host string, handle []any) error {
	_ = c.RemoveRoute(ctx, instanceID)
	route := map[string]any{
		"@id":    routeID(instanceID),
		"match":  []any{map[string]any{"host": []string{host}}},
		"handle": handle,
	}
	// Caddy admin API insert semantics: PUT to /routes/<N> inserts at that
	// index, pushing existing items down. POST to /routes/<N> appends
	// regardless of the trailing index (verified 2026-05-24). Using PUT/0
	// keeps the catch-all (which initially sits at index 0, then index 1+
	// after the first install) last in evaluation order.
	return c.put(ctx, "/config/apps/http/servers/malmo/routes/0", route)
}

func splashHTML(appName, state string) string {
	var msg, refresh string
	switch state {
	case "stopped":
		msg = appName + " is stopped."
	case "failed":
		msg = appName + " failed to start."
	default: // starting
		msg = appName + " is starting up…"
		refresh = `<meta http-equiv="refresh" content="2">`
	}
	return `<!doctype html><html><head><meta charset="utf-8">` + refresh +
		`<title>` + appName + `</title>` +
		`<style>body{font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0;color:#444;background:#f6f6f7}</style>` +
		`</head><body><div><h1 style="font-weight:600">` + msg + `</h1></div></body></html>`
}

func (c *Client) RemoveRoute(ctx context.Context, instanceID string) error {
	return c.RemoveRouteByID(ctx, routeID(instanceID))
}

// RemoveRouteByID deletes a route by its raw Caddy @id (idempotent: an unknown
// @id is treated as already-gone). RemoveRoute is the per-instance wrapper.
func (c *Client) RemoveRouteByID(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.admin+"/id/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 200 on delete; Caddy returns an error if the @id is unknown — treat that
	// as already-gone (idempotent uninstall).
	return nil
}

// catchAllRoute returns the map[string]any that represents the catch-all 404
// route. The @id makes it addressable via /id/malmo-catchall so EnsureCatchAll
// can probe for its presence idempotently.
func catchAllRoute() map[string]any {
	return map[string]any{
		"@id":   "malmo-catchall",
		"match": []any{map[string]any{}},
		"handle": []any{map[string]any{
			"handler":     "static_response",
			"status_code": 404,
			"headers":     map[string]any{"Content-Type": []any{"text/html; charset=utf-8"}},
			"body":        catchAllBody,
		}},
	}
}

// dashboardRouteID is the stable @id of the dashboard route, so EnsureDashboard
// is idempotent (remove-then-add) the same way per-app routes are.
const dashboardRouteID = "malmo-dashboard"

// EnsureDashboard installs the dashboard host route (WEB_UI.md # deploy model):
// requests to the dashboard host split by path — /api/v1/* (including the SSE
// streams) reverse-proxy to the brain, everything else to the malmo-ui static
// server. It is a production-only route: in dev the brain runs natively and the
// UI is served by Vite, so cmd/brain only calls this when the UI upstream is
// configured (the containerized stack). Inserted at index 0 like an app route,
// so it sorts before the catch-all; idempotent via its @id.
//
// SSE buffering: the brain's API serves the live-resources and per-app log
// streams under /api/v1/, so the brain leg sets flush_interval -1 (flush each
// write, no response buffering) — buffering those would stall the dashboard's
// live updates. It is harmless for the plain JSON requests on the same leg.
func (c *Client) EnsureDashboard(ctx context.Context, host, brainUpstream, uiUpstream string) error {
	_ = c.RemoveRouteByID(ctx, dashboardRouteID)
	route := map[string]any{
		"@id":   dashboardRouteID,
		"match": []any{map[string]any{"host": []string{host}}},
		"handle": []any{map[string]any{
			"handler": "subroute",
			"routes": []any{
				map[string]any{
					// /api/* is the brain's REST+SSE surface; /_malmo/* is the brain's
					// non-API browser endpoints (today the portal-to-box SSO landing
					// GET /_malmo/sso). Both leg to the brain; everything else is the
					// dashboard SPA.
					"match": []any{map[string]any{"path": []string{"/api/*", "/_malmo/*"}}},
					"handle": []any{map[string]any{
						"handler":        "reverse_proxy",
						"flush_interval": -1,
						"upstreams":      []any{map[string]any{"dial": brainUpstream}},
					}},
				},
				map[string]any{
					"handle": []any{map[string]any{
						"handler":   "reverse_proxy",
						"upstreams": []any{map[string]any{"dial": uiUpstream}},
					}},
				},
			},
		}},
	}
	if err := c.put(ctx, "/config/apps/http/servers/malmo/routes/0", route); err != nil {
		return fmt.Errorf("caddy: install dashboard route: %w", err)
	}
	slog.Info("caddy: dashboard route installed", "host", host, "upstream", uiUpstream)
	return nil
}

// WaitReady blocks until the Caddy admin API answers a GET on the config root,
// or ctx is done. Used at brain startup after the brain has brought Caddy up via
// compose: the one-shot route configuration (EnsureServer / EnsureCatchAll /
// EnsureDashboard) must not race the container's first second of life, since
// nothing re-runs those on a transient failure.
func (c *Client) WaitReady(ctx context.Context) error {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		if status, err := c.get(ctx, "/config/"); err == nil && status == http.StatusOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("caddy: admin API not ready: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// EnsureCatchAll installs the catch-all 404 route if it is not already present.
// Called at brain startup after EnsureServer resets the route list — it
// appends the catch-all at the tail of routes[] so all per-app routes inserted
// at index 0 naturally sort before it. Idempotent: probes /id/malmo-catchall
// first and returns nil immediately if found.
func (c *Client) EnsureCatchAll(ctx context.Context) error {
	status, err := c.get(ctx, "/id/malmo-catchall")
	if err != nil {
		return fmt.Errorf("caddy: probe catch-all: %w", err)
	}
	if status == http.StatusOK {
		slog.Info("caddy: catch-all already present")
		return nil
	}
	// Not found (404 or any non-200) — install it by appending to routes[].
	if err := c.post(ctx, "/config/apps/http/servers/malmo/routes", catchAllRoute()); err != nil {
		return fmt.Errorf("caddy: install catch-all: %w", err)
	}
	slog.Info("caddy: catch-all installed")
	return nil
}

// EnsureServer resets the "malmo" server's route list to empty at brain
// startup, giving the reconciler a clean slate to rebuild routes from desired
// state. It PATCHes only the routes array, so it never touches the server's
// listen addr or Caddy's admin config. Both dev (dev/caddy.json) and production
// (dev/control-plane/caddy.json, staged by the M1b bootstrap) boot Caddy with
// this server and its :80 listener pre-declared, so the brain never sets the
// listen addr — there is no caller-supplied listen to apply.
func (c *Client) EnsureServer(ctx context.Context) error {
	return c.patch(ctx, "/config/apps/http/servers/malmo/routes", []any{})
}

// EnrollmentCredentials is the per-box acme-dns account Caddy uses to answer the
// ACME DNS-01 challenge (consumed from the first-boot seed; C3b). Passed as
// primitives so this package stays decoupled from internal/profile.
type EnrollmentCredentials struct {
	Subdomain string
	Username  string
	Password  string
}

// EnsureWildcardTLS configures automatic HTTPS for a hosted box: it provisions
// Let's Encrypt certs covering `subjects` (the dashboard apex
// "<box-id>.malmo.network" + the wildcard "*.<box-id>.malmo.network") via ACME
// DNS-01 challenges solved against acme-dns with the box's seeded credentials,
// and adds the :443 listener so the host-matched routes serve over it.
// Hosted-only and always-on (ENVIRONMENT.md # Networking & discovery); the
// appliance path, which has no enrollment and serves plain-HTTP `.local`, never
// calls this and keeps its :80-only config.
//
// Two certs, issued one at a time, not one combined cert. Caddy/certmagic
// issues one certificate per SAN, so listing both subjects in a single
// automation policy spawns two independent ACME orders. Both orders solve
// DNS-01 at the *same* name ("_acme-challenge.<box-id>.malmo.network" is
// where RFC 8555 puts the wildcard's challenge, and it's also where the base
// name's challenge lands), against the *same* acme-dns account. acme-dns keeps
// only the small fixed number of most-recent TXT values for an account, so a
// third write from either order (a propagation recheck, an authorization
// retry) can evict the sibling order's still-unvalidated value and fail its
// validation silently. Running the two orders concurrently is therefore
// unsafe: this function issues the wildcard subject alone first, waits
// (bounded) until Caddy has actually obtained a certificate covering it, and
// only then adds the base subject as its own policy, so its order is never
// concurrent with the wildcard's. Only one order is ever presenting or
// cleaning up a DNS-01 record at a time.
//
// The tradeoff is latency, not correctness: the two orders that used to race
// now run back to back, so first-boot cert acquisition takes roughly two ACME
// round-trips end-to-end instead of overlapping, and the box's ":443 fully
// ready" point lands correspondingly later. Any startup budget wrapping this
// call should size for that.
//
// The acme-dns provider sets only this box's own `_acme-challenge` TXT, so
// renewal (~every 60 days) runs box→acme-dns directly with no malmo control-plane
// call (cloud specs/ARCHITECTURE.md Contract 2). The challenge `server_url` is a
// box-side constant (the same acme-dns endpoint for every box), not part of the
// seeded payload.
//
// Idempotent: it removes any prior tls app config and re-puts (the file's
// remove-then-put idiom), so a re-run at every boot converges cleanly. The DNS
// provider's JSON shape is pinned to the caddy-dns/acmedns module compiled into
// the hosted Caddy image; real issuance is verified end-to-end in the cloud
// on-ramp (cloud #6 / CL6), not in the inner loop where there is no real ACME:
// this function is only ever reached on the hosted + enrolled path (its one
// caller gates on both), so the inner dev loop never runs it or waits on it.
func (c *Client) EnsureWildcardTLS(ctx context.Context, subjects []string, acmeDNSEndpoint string, enr EnrollmentCredentials) error {
	wildcard, base, err := splitCertSubjects(subjects)
	if err != nil {
		return err
	}
	issuer := acmeIssuer(acmeDNSEndpoint, enr)

	// Remove-then-put so a re-run replaces the policy cleanly; PUT on a fresh
	// (deleted) key creates it, matching upsertRoute's idiom. The DELETE is
	// best-effort — an absent tls app is the first-boot case.
	_ = c.del(ctx, "/config/apps/tls")

	// Phase 1: the wildcard alone, as the only policy, so its order is the
	// only one that can write the enrollment account's DNS-01 record.
	tlsApp := map[string]any{
		"automation": map[string]any{
			"policies": []any{certPolicy(wildcard, issuer)},
		},
	}
	if err := c.put(ctx, "/config/apps/tls", tlsApp); err != nil {
		return fmt.Errorf("caddy: configure wildcard tls (phase 1: wildcard subject): %w", err)
	}
	// Add :443 alongside the existing :80 so the same host-matched routes serve
	// HTTPS, and so the phase-1 readiness probe below has something to dial.
	// PATCH replaces the listen array the bootstrap config declared as [":80"].
	if err := c.patch(ctx, "/config/apps/http/servers/malmo/listen", []any{":80", ":443"}); err != nil {
		return fmt.Errorf("caddy: add :443 listener: %w", err)
	}
	slog.Info("caddy: wildcard tls phase 1 configured, waiting for cert", "subject", wildcard)

	if err := c.waitForCert(ctx, wildcard); err != nil {
		return fmt.Errorf("caddy: wildcard subject %s not issued before adding base subject: %w", wildcard, err)
	}

	// Phase 2: the base subject, appended as its own policy only after the
	// wildcard's order has fully finished (present, validate, clean up), so
	// the two orders are never concurrent.
	if err := c.post(ctx, "/config/apps/tls/automation/policies", certPolicy(base, issuer)); err != nil {
		return fmt.Errorf("caddy: configure wildcard tls (phase 2: base subject): %w", err)
	}
	slog.Info("caddy: wildcard tls configured", "wildcard", wildcard, "base", base)
	return nil
}

// splitCertSubjects sorts subjects (profile.CertSubjects' [base, wildcard]
// pair) into its wildcard and base names, independent of input order.
func splitCertSubjects(subjects []string) (wildcard, base string, err error) {
	for _, s := range subjects {
		if strings.HasPrefix(s, "*.") {
			wildcard = s
		} else if base == "" {
			base = s
		}
	}
	if wildcard == "" || base == "" {
		return "", "", fmt.Errorf("caddy: want exactly one wildcard and one base subject, got %v", subjects)
	}
	return wildcard, base, nil
}

// acmeIssuer builds the acme-dns ACME issuer config shared by both phases'
// automation policies.
func acmeIssuer(acmeDNSEndpoint string, enr EnrollmentCredentials) map[string]any {
	return map[string]any{
		"module": "acme",
		"challenges": map[string]any{
			"dns": map[string]any{
				"provider": map[string]any{
					"name":       "acmedns",
					"server_url": acmeDNSEndpoint,
					"username":   enr.Username,
					"password":   enr.Password,
					"subdomain":  enr.Subdomain,
				},
			},
		},
	}
}

// certPolicy builds a single-subject Caddy TLS automation policy. Each phase
// of EnsureWildcardTLS gets its own policy (rather than one policy listing
// both subjects) so the two subjects' ACME orders are configured, and so run,
// separately.
func certPolicy(subject string, issuer map[string]any) map[string]any {
	return map[string]any{
		"subjects": []any{subject},
		"issuers":  []any{issuer},
	}
}

// wildcardPollInterval is how often waitForCert re-probes Caddy while it
// waits for the phase-1 wildcard order to finish.
const wildcardPollInterval = 2 * time.Second

// waitForCert blocks until c.certReady reports the certificate covering
// wildcardSubject has been obtained, or ctx is done, whichever comes first.
// It is the handoff between EnsureWildcardTLS's two phases: the base
// subject's order must not start until the wildcard's has completed, so the
// two never write the enrollment account's DNS-01 record at the same time.
// Bounded by ctx, so a box whose wildcard order never completes (DNS/ACME
// misconfigured, network partition) fails this call instead of hanging
// forever; the caller treats that the same as any other best-effort Caddy
// config failure.
func (c *Client) waitForCert(ctx context.Context, wildcardSubject string) error {
	t := time.NewTicker(c.certPollInterval)
	defer t.Stop()
	for {
		if ready, err := c.certReady(ctx, wildcardSubject); err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for cert: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// dialCertReady is certReady's real implementation. It dials the box's own
// :443 (the same port the box already serves apps on, not a new externally
// exposed endpoint) with SNI set to a concrete name the wildcard covers, and
// reports whether the certificate Caddy presents lists wildcardSubject itself
// among its SANs (i.e., certmagic has already obtained and loaded the
// wildcard cert, as opposed to some other or no cert yet).
//
// Skipping certificate verification is deliberate and does not weaken
// anything: this dial never sends or trusts data, it only inspects whichever
// certificate Caddy currently has loaded (which may legitimately not be
// issued, or not be the wildcard, yet).
func (c *Client) dialCertReady(ctx context.Context, wildcardSubject string) (bool, error) {
	addr, err := c.tlsAddr()
	if err != nil {
		return false, err
	}
	probeSNI := strings.Replace(wildcardSubject, "*", "malmo-cert-probe", 1)
	d := tls.Dialer{Config: &tls.Config{ServerName: probeSNI, InsecureSkipVerify: true}} //nolint:gosec // inspecting SANs only, see comment above
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		// Most likely "no cert yet" (or nothing listening yet), which is
		// exactly what we're polling for, not a hard failure.
		return false, nil
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return false, nil
	}
	for _, san := range certs[0].DNSNames {
		if san == wildcardSubject {
			return true, nil
		}
	}
	return false, nil
}

// tlsAddr derives the box's own :443 address from the admin API address:
// same host, different port (e.g. "malmo-caddy:2019" -> "malmo-caddy:443").
// Caddy's admin API and its public listener are the same process.
func (c *Client) tlsAddr() (string, error) {
	u, err := url.Parse(c.admin)
	if err != nil {
		return "", fmt.Errorf("caddy: parse admin address %q: %w", c.admin, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("caddy: admin address %q has no host", c.admin)
	}
	return net.JoinHostPort(host, "443"), nil
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	return c.send(ctx, "POST", path, body)
}

// del issues a DELETE to a Caddy admin config path, tolerating a not-found
// (the path was never configured — an absent tls app on first boot). Transport
// failures are returned.
func (c *Client) del(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.admin+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

func (c *Client) patch(ctx context.Context, path string, body any) error {
	return c.send(ctx, "PATCH", path, body)
}

func (c *Client) put(ctx context.Context, path string, body any) error {
	return c.send(ctx, "PUT", path, body)
}

// get issues a GET to the Caddy admin API and returns the HTTP status code.
// It never returns an error for non-2xx responses — only for transport failures.
// This lets EnsureCatchAll distinguish "not found" (404) from "unreachable".
func (c *Client) get(ctx context.Context, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.admin+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("caddy admin unreachable: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

func (c *Client) send(ctx context.Context, method, path string, body any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.admin+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(resp.Body)
		return fmt.Errorf("caddy admin %s %s: %s: %s", method, path, resp.Status, b.String())
	}
	return nil
}
