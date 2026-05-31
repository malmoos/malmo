// Package hostclient is the brain's client for the host-agent UNIX socket
// (BRAIN_HOST_PROTOCOL.md). HTTP/JSON over net.Dial("unix", ...).
package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

// ErrUnknownUser is returned by ResolveHome when the host reports the user
// does not exist. The brain maps this to an installation error, not a retry.
// Check with errors.Is — the value is stable across versions.
var ErrUnknownUser = errors.New("unknown user")

// ResolveHome returns the user's home directory path, UID, and GID from the
// host. The brain calls this during install to build bind-mount sources and
// user: directives for personal app instances.
//
// Returns ErrUnknownUser (checkable with errors.Is) when the host reports the
// user does not exist — the brain maps this to an installation error, not a
// retry. Any other non-200 is a generic host error.
//
// Does not go through do because do flattens all errors to opaque strings;
// the 404 → typed-sentinel discrimination must survive to the caller.
func (c *Client) ResolveHome(ctx context.Context, username string) (protocol.ResolveHomeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"http://agent/v1/users/"+url.PathEscape(username)+"/home", http.NoBody)
	if err != nil {
		return protocol.ResolveHomeResponse{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return protocol.ResolveHomeResponse{}, fmt.Errorf("host-agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return protocol.ResolveHomeResponse{}, ErrUnknownUser
	}
	if resp.StatusCode >= 300 {
		var e protocol.Error
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Code == "" {
			e.Code = "host-agent-error"
			e.Message = resp.Status
		}
		return protocol.ResolveHomeResponse{}, fmt.Errorf("host-agent /v1/users/%s/home: %s (%s)",
			url.PathEscape(username), e.Message, e.Code)
	}
	var out protocol.ResolveHomeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return protocol.ResolveHomeResponse{}, err
	}
	return out, nil
}

// WellKnownIdentity returns the fixed service-account UIDs/GIDs for malmo-app
// and malmo-shared from the host. The brain calls this during install to build
// user: and group_add directives for household-scope app instances.
//
// All non-200 responses are generic host errors (there is no unknown-user case
// here); propagated via the standard do helper.
func (c *Client) WellKnownIdentity(ctx context.Context) (protocol.WellKnownIdentityResponse, error) {
	var out protocol.WellKnownIdentityResponse
	err := c.do(ctx, "GET", "/v1/identity/well-known", nil, &out)
	return out, err
}

func (c *Client) SystemStatus(ctx context.Context) (protocol.SystemStatus, error) {
	var out protocol.SystemStatus
	err := c.do(ctx, "GET", "/v1/system/status", nil, &out)
	return out, err
}

// SystemResources returns one raw cumulative-counter sample (CPU jiffies,
// memory levels, per-interface and per-device byte counters, uptime) plus a
// monotonic ts_ns. host-agent computes no rates — the caller (the brain's
// live-resources hub) diffs two successive samples, using the ts_ns delta as
// the rate denominator. All non-200 responses are generic host errors,
// propagated via the standard do helper.
func (c *Client) SystemResources(ctx context.Context) (protocol.SystemResources, error) {
	var out protocol.SystemResources
	err := c.do(ctx, "GET", "/v1/system/resources", nil, &out)
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
	var reqBody io.Reader = http.NoBody
	if body != nil {
		reqBody = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
