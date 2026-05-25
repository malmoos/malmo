// Package hostclient is the brain's client for the host-agent UNIX socket
// (BRAIN_HOST_PROTOCOL.md). HTTP/JSON over net.Dial("unix", ...).
package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/malmo/malmo/internal/protocol"
)

type Client struct {
	http *http.Client
}

// New dials the given UNIX socket path. The host part of the URL is ignored
// by the dialer; we use "http://agent" as a stable placeholder.
func New(sockPath string) *Client {
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sockPath)
				},
			},
		},
	}
}

func (c *Client) Publish(ctx context.Context, slug string) (protocol.PublishResponse, error) {
	var out protocol.PublishResponse
	err := c.do(ctx, "POST", "/v1/discovery/publish", protocol.PublishRequest{Slug: slug}, &out)
	return out, err
}

func (c *Client) Unpublish(ctx context.Context, slug string) error {
	return c.do(ctx, "POST", "/v1/discovery/unpublish", protocol.UnpublishRequest{Slug: slug}, nil)
}

// VerifyPassword asks host-agent whether (user, password) is valid.
// Returned bool is the only signal — host-agent deliberately doesn't
// distinguish wrong-password from unknown-user. See BRAIN_HOST_PROTOCOL.md.
func (c *Client) VerifyPassword(ctx context.Context, user, password string) (bool, error) {
	var out protocol.VerifyPasswordResponse
	err := c.do(ctx, "POST", "/v1/auth/verify-password", protocol.VerifyPasswordRequest{User: user, Password: password}, &out)
	return out.Valid, err
}

// SetPassword upserts the user's password (creates the user if missing).
func (c *Client) SetPassword(ctx context.Context, user, password string) error {
	return c.do(ctx, "POST", "/v1/auth/set-password", protocol.SetPasswordRequest{User: user, Password: password}, nil)
}

// SetRole updates the user's Linux group membership to match role ("admin" or "member").
func (c *Client) SetRole(ctx context.Context, user, role string) error {
	return c.do(ctx, "POST", "/v1/auth/set-role", protocol.SetRoleRequest{User: user, Role: role}, nil)
}

// DeleteUser removes the user. Idempotent: unknown user returns nil.
func (c *Client) DeleteUser(ctx context.Context, user string) error {
	return c.do(ctx, "POST", "/v1/auth/delete-user", protocol.DeleteUserRequest{User: user}, nil)
}

func (c *Client) SystemStatus(ctx context.Context) (protocol.SystemStatus, error) {
	var out protocol.SystemStatus
	err := c.do(ctx, "GET", "/v1/system/status", nil, &out)
	return out, err
}

// StorageHealth returns the latest storage findings recorded by
// malmo-storage-verify (BOOT.md # The storage-ready target). host-agent
// always returns 200 with a parseable payload — see the contract on
// hostagent.HealthSource — so a transport error here is genuinely "host-agent
// unreachable," not "storage looks bad."
func (c *Client) StorageHealth(ctx context.Context) (protocol.StorageHealth, error) {
	var out protocol.StorageHealth
	err := c.do(ctx, "GET", "/v1/health/storage", nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("host-agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e protocol.Error
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Code == "" {
			e.Code = "host-agent-error"
			e.Message = resp.Status
		}
		return fmt.Errorf("host-agent %s: %s (%s)", path, e.Message, e.Code)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
