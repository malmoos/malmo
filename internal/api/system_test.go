package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/auth"
	"github.com/malmoos/malmo/internal/hostclient"
	"github.com/malmoos/malmo/internal/store"
)

// TestSystemStorage_RequiresAuth: the Storage poll needs a session like every
// non-allowlisted route.
func TestSystemStorage_RequiresAuth(t *testing.T) {
	h := newHarness(t)
	resp := h.do("GET", "/api/v1/system/storage", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/v1/system/storage: want 401, got %d", resp.StatusCode)
	}
}

// TestSystemStorage_ReturnsDisks: an authenticated user gets the host-agent's
// per-volume figures mapped through, in order.
func TestSystemStorage_ReturnsDisks(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	resp := h.do("GET", "/api/v1/system/storage", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/system/storage: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[SystemStorageDTO](t, resp)
	if len(body.Disks) != 2 {
		t.Fatalf("want 2 disks, got %+v", body.Disks)
	}
	if body.Disks[0].Label != "System" || body.Disks[0].TotalBytes != 64<<30 {
		t.Errorf("System entry: got %+v", body.Disks[0])
	}
	if body.Disks[1].Label != "Data" || body.Disks[1].FreeBytes != harnessFreeBytes {
		t.Errorf("Data entry: got %+v", body.Disks[1])
	}
}

// TestSystemStorage_NoIdentity_401: the handler's own auth guard (belt over the
// middleware) returns 401 when no identity rode the context.
func TestSystemStorage_NoIdentity_401(t *testing.T) {
	s := &Server{}
	_, err := s.systemStorage(context.Background(), nil)
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", err)
	}
}

// TestSystemStorage_HostError_502: a host-agent read failure (dead socket) maps
// to 502, not a misleading empty-disk 200.
func TestSystemStorage_HostError_502(t *testing.T) {
	s := &Server{host: hostclient.New(filepath.Join(t.TempDir(), "absent.sock"))}
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		User: store.User{ID: "u_x", Role: store.RoleMember},
	})
	_, err := s.systemStorage(ctx, nil)
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != http.StatusBadGateway {
		t.Fatalf("want 502, got %v", err)
	}
}

// TestSystemStorage_MemberAllowed: host-level storage isn't per-user data, so a
// member gets it too — no admin gate (same posture as the live resource stream).
func TestSystemStorage_MemberAllowed(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob001", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("GET", "/api/v1/system/storage", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member GET /api/v1/system/storage: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[SystemStorageDTO](t, resp)
	if len(body.Disks) != 2 {
		t.Fatalf("member: want 2 disks, got %+v", body.Disks)
	}
}
