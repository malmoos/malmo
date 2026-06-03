// Package caddy drives the Caddy admin API (CONTROL_PLANE.md: "Configured via
// Caddy's admin API on localhost:2019 ... no Caddyfile on disk"). Skeleton
// scope: per-instance reverse-proxy routes keyed by Host header, added/removed
// on install/uninstall. All ops are best-effort in dev — if Caddy is down the
// brain logs and continues so the install spine still works.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// catchAllBody is the HTML returned by the catch-all 404 route for any
// unmatched hostname. Kept as a raw string so the Go source is readable;
// serialised to a single JSON string value when sent to the Caddy admin API.
const catchAllBody = `<!doctype html><html><head><meta charset="utf-8"><title>404 — molma</title><style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem;color:#222}h1{margin:0 0 .5rem;font-size:1.5rem}p{color:#555;line-height:1.5}a{color:#06c}</style></head><body><h1>404 — No app at this hostname</h1><p>The address you tried doesn't match any installed app on this molma box.</p><p><a href="http://molma.local">Go to the dashboard</a></p></body></html>`

type Client struct {
	admin string
	http  *http.Client
}

func New(adminAddr string) *Client {
	return &Client{
		admin: adminAddr,
		http:  &http.Client{Timeout: 5 * time.Second},
	}
}

// routeID is the stable @id we attach to each route so we can delete it later.
func routeID(instanceID string) string { return "molma-app-" + instanceID }

// AddRoute registers Host(host) -> reverse_proxy(upstream): the real upstream
// once the app is healthy. upstream is "host:port" reachable from the Caddy
// container (the main_service's alias on the molma-ingress network).
func (c *Client) AddRoute(ctx context.Context, instanceID, host, upstream string) error {
	return c.upsertRoute(ctx, instanceID, host, []any{
		map[string]any{
			"handler":   "reverse_proxy",
			"upstreams": []any{map[string]any{"dial": upstream}},
		},
	})
}

// AddSplashRoute registers Host(host) -> a molma-served splash page for the
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
	return c.put(ctx, "/config/apps/http/servers/molma/routes/0", route)
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
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.admin+"/id/"+routeID(instanceID), nil)
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
// route. The @id makes it addressable via /id/molma-catchall so EnsureCatchAll
// can probe for its presence idempotently.
func catchAllRoute() map[string]any {
	return map[string]any{
		"@id":   "molma-catchall",
		"match": []any{map[string]any{}},
		"handle": []any{map[string]any{
			"handler":     "static_response",
			"status_code": 404,
			"headers":     map[string]any{"Content-Type": []any{"text/html; charset=utf-8"}},
			"body":        catchAllBody,
		}},
	}
}

// EnsureCatchAll installs the catch-all 404 route if it is not already present.
// Called at brain startup after EnsureServer resets the route list — it
// appends the catch-all at the tail of routes[] so all per-app routes inserted
// at index 0 naturally sort before it. Idempotent: probes /id/molma-catchall
// first and returns nil immediately if found.
func (c *Client) EnsureCatchAll(ctx context.Context) error {
	status, err := c.get(ctx, "/id/molma-catchall")
	if err != nil {
		return fmt.Errorf("caddy: probe catch-all: %w", err)
	}
	if status == http.StatusOK {
		slog.Info("caddy: catch-all already present")
		return nil
	}
	// Not found (404 or any non-200) — install it by appending to routes[].
	if err := c.post(ctx, "/config/apps/http/servers/molma/routes", catchAllRoute()); err != nil {
		return fmt.Errorf("caddy: install catch-all: %w", err)
	}
	slog.Info("caddy: catch-all installed")
	return nil
}

// EnsureServer resets the "molma" server's route list to empty at brain
// startup, giving the reconciler a clean slate to rebuild routes from desired
// state. It PATCHes only the routes array, so it never touches the server's
// listen addr or Caddy's admin config. The dev Caddy boots with this server
// pre-declared (dev/caddy.json); creating the server from scratch (production,
// where Caddy is brain-managed) is a follow-up.
func (c *Client) EnsureServer(ctx context.Context, listen string) error {
	return c.patch(ctx, "/config/apps/http/servers/molma/routes", []any{})
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	return c.send(ctx, "POST", path, body)
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
