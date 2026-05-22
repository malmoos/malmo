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
	"net/http"
	"time"
)

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
func routeID(instanceID string) string { return "malmo-app-" + instanceID }

// AddRoute registers Host(host) -> reverse_proxy(upstream) on the default
// server. upstream is "host:port" reachable from the Caddy container (the
// main_service's alias on the malmo-ingress network).
func (c *Client) AddRoute(ctx context.Context, instanceID, host, upstream string) error {
	route := map[string]any{
		"@id": routeID(instanceID),
		"match": []any{
			map[string]any{"host": []string{host}},
		},
		"handle": []any{
			map[string]any{
				"handler":   "reverse_proxy",
				"upstreams": []any{map[string]any{"dial": upstream}},
			},
		},
	}
	// POST to the routes array of the http server named "malmo".
	return c.post(ctx, "/config/apps/http/servers/malmo/routes", route)
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

// EnsureServer makes sure an http server named "malmo" exists listening on the
// given addr (e.g. ":80"). It PUTs only the server subtree so it never clobbers
// Caddy's own admin config (which lives at the config root). The dev Caddy
// boots with this server pre-declared (dev/caddy.json); this call re-asserts it
// and is harmless if it already matches.
func (c *Client) EnsureServer(ctx context.Context, listen string) error {
	server := map[string]any{
		"listen": []string{listen},
		"routes": []any{},
	}
	return c.put(ctx, "/config/apps/http/servers/malmo", server)
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	return c.send(ctx, "POST", path, body)
}

func (c *Client) put(ctx context.Context, path string, body any) error {
	return c.send(ctx, "PUT", path, body)
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
