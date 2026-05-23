package hostclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/malmo/malmo/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

// startFakeAuthAgent spins up a minimal stand-in for the fake host-agent's
// auth surface on a UNIX socket in t.TempDir(). We mirror the fake's handler
// logic (bcrypt-backed in-memory hashes) rather than importing cmd/host-agent,
// which is a main package. The point of this test is the wire seam:
// hostclient ↔ socket ↔ /v1/auth/{verify,set,delete}-{password,user}.
func startFakeAuthAgent(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var mu sync.Mutex
	passwords := map[string][]byte{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/set-password", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SetPasswordRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
		mu.Lock()
		passwords[req.User] = hash
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
	})
	mux.HandleFunc("POST /v1/auth/verify-password", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.VerifyPasswordRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		hash, ok := passwords[req.User]
		mu.Unlock()
		valid := ok && bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) == nil
		_ = json.NewEncoder(w).Encode(protocol.VerifyPasswordResponse{Valid: valid})
	})
	mux.HandleFunc("POST /v1/auth/delete-user", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.DeleteUserRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		delete(passwords, req.User)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
	})

	roles := map[string]string{}
	mux.HandleFunc("POST /v1/auth/set-role", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.SetRoleRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Role != "admin" && req.Role != "member" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(protocol.Error{Code: "bad-request", Message: "role must be admin or member"})
			return
		}
		mu.Lock()
		roles[req.User] = req.Role
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct{}{})
	})
	_ = roles

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return sock
}

func TestSetRole(t *testing.T) {
	c := New(startFakeAuthAgent(t))
	ctx := context.Background()

	if err := c.SetRole(ctx, "alice", "admin"); err != nil {
		t.Fatalf("SetRole admin: %v", err)
	}
	if err := c.SetRole(ctx, "alice", "member"); err != nil {
		t.Fatalf("SetRole member: %v", err)
	}
	if err := c.SetRole(ctx, "alice", "superuser"); err == nil {
		t.Fatal("SetRole(bogus role) = nil; want error")
	}
}

func TestAuthEndpoints(t *testing.T) {
	c := New(startFakeAuthAgent(t))
	ctx := context.Background()

	// Set, then verify the right and wrong passwords.
	if err := c.SetPassword(ctx, "cindy", "hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if ok, err := c.VerifyPassword(ctx, "cindy", "hunter2"); err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := c.VerifyPassword(ctx, "cindy", "wrong"); err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
	}

	// Unknown user is false, not an error (mirrors PAM's posture).
	if ok, err := c.VerifyPassword(ctx, "ghost", "anything"); err != nil || ok {
		t.Fatalf("VerifyPassword(unknown) = %v, %v; want false, nil", ok, err)
	}

	// Upsert: changing the password makes the old one invalid.
	if err := c.SetPassword(ctx, "cindy", "newpass"); err != nil {
		t.Fatalf("SetPassword (upsert): %v", err)
	}
	if ok, _ := c.VerifyPassword(ctx, "cindy", "hunter2"); ok {
		t.Fatal("old password still valid after upsert")
	}
	if ok, _ := c.VerifyPassword(ctx, "cindy", "newpass"); !ok {
		t.Fatal("new password not valid after upsert")
	}

	// Delete is idempotent and clears credentials.
	if err := c.DeleteUser(ctx, "cindy"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := c.DeleteUser(ctx, "cindy"); err != nil {
		t.Fatalf("DeleteUser (second call should be idempotent): %v", err)
	}
	if ok, _ := c.VerifyPassword(ctx, "cindy", "newpass"); ok {
		t.Fatal("password still valid after delete")
	}
}
