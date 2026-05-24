// Package hostagent contains the shared HTTP handler layer for both
// cmd/host-agent (fake) and cmd/host-agent-real. It speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a UNIX socket.
package hostagent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/malmo/malmo/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

const AgentVersion = "0.0.1-fake"

// PasswordVerifier is a consumer-side interface: it lives here because this is
// the package that calls it (the verifyPassword handler). Provider packages
// (FakeVerifier, PAMVerifier) export concrete types only.
type PasswordVerifier interface {
	Verify(user, password string) (bool, error)
}

// Publisher is a consumer-side interface for writing/removing Avahi service
// files. Lives here because the publish/unpublish HTTP handlers are the
// consumers. Provider packages (FakePublisher, avahipublisher.FilePublisher)
// export concrete types only.
type Publisher interface {
	Publish(slug string) (name string, err error)
	Unpublish(slug string) error
}

// UserManager is a consumer-side interface for the system-level user account
// operations behind /v1/auth/set-password (and, later, /set-role and
// /delete-user). Provider packages (usermgr.LinuxUserManager) export concrete
// types only.
//
// UpsertPassword is upsert: creates the user if missing, otherwise updates the
// password. SetRole updates Linux group membership to match the role
// (admin → in `sudo`, member → not in `sudo`); idempotent. See
// BRAIN_HOST_PROTOCOL.md # Credential mutation endpoints and
// USERS_AND_GROUPS.md # Roles.
type UserManager interface {
	UpsertPassword(user, password string) error
	SetRole(user, role string) error
	DeleteUser(user string) error
}

// Agent is the HTTP handler set for host-agent. It holds both the
// PasswordVerifier (swapped per binary) and the in-memory fake maps used by
// setPassword / setRole / deleteUser when UserMgr is nil (the fake binary).
// cmd/host-agent-real wires UserMgr so all three delegate to /etc/passwd via
// usermgr.LinuxUserManager.
type Agent struct {
	mu       sync.Mutex
	// published is a write-through cache of announced names, keyed by slug.
	// Updated on every successful Publish/Unpublish call so GET /v1/discovery/state
	// can answer without requiring the Publisher to expose a listing method.
	published map[string]protocol.PublishedName
	// passwords is the in-memory bcrypt map used by setPassword/deleteUser
	// when UserMgr is nil (the fake binary). FakeVerifier reads from it.
	// In cmd/host-agent-real, UserMgr is wired and these handlers bypass
	// the map entirely — /etc/shadow is the source of truth there.
	passwords map[string][]byte
	roles     map[string]string
	startedAt time.Time

	// Verifier handles POST /v1/auth/verify-password.
	// Swapped per binary: FakeVerifier (fake) vs PAMVerifier (real).
	Verifier PasswordVerifier

	// Publisher handles POST /v1/discovery/publish and /v1/discovery/unpublish.
	// Swapped per binary: FakePublisher (fake) vs avahipublisher.FilePublisher (real).
	Publisher Publisher

	// UserMgr, when non-nil, takes over POST /v1/auth/set-password,
	// /v1/auth/set-role, and /v1/auth/delete-user: handlers delegate to the
	// manager instead of writing to the in-memory maps. cmd/host-agent leaves
	// this nil (fake path); cmd/host-agent-real wires usermgr.LinuxUserManager
	// so /etc/passwd + /etc/shadow + /etc/group become the source of truth.
	UserMgr UserManager
}

// New constructs an Agent with the given PasswordVerifier and Publisher.
// Either may be nil at construction time and set later (useful for the
// FakeVerifier pointer-back pattern), but both must be non-nil before
// Mount is called and requests arrive.
func New(v PasswordVerifier, pub Publisher) *Agent {
	return &Agent{
		published: map[string]protocol.PublishedName{},
		passwords: map[string][]byte{},
		roles:     map[string]string{},
		startedAt: time.Now(),
		Verifier:  v,
		Publisher: pub,
	}
}

// Mount registers all routes on mux.
func (a *Agent) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/discovery/publish", a.publish)
	mux.HandleFunc("POST /v1/discovery/unpublish", a.unpublish)
	mux.HandleFunc("GET /v1/discovery/state", a.discoveryState)
	mux.HandleFunc("GET /v1/system/status", a.systemStatus)
	mux.HandleFunc("POST /v1/auth/verify-password", a.verifyPassword)
	mux.HandleFunc("POST /v1/auth/set-password", a.setPassword)
	mux.HandleFunc("POST /v1/auth/set-role", a.setRole)
	mux.HandleFunc("POST /v1/auth/delete-user", a.deleteUser)
}

func (a *Agent) publish(w http.ResponseWriter, r *http.Request) {
	var req protocol.PublishRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Slug == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "slug is required")
		return
	}
	name, err := a.Publisher.Publish(req.Slug)
	if err != nil {
		slog.Error("publish: publisher error", "slug", req.Slug, "err", err)
		writeErr(w, http.StatusInternalServerError, "publish-failed", err.Error())
		return
	}
	// Write-through cache: keep the in-memory map in sync so GET /v1/discovery/state
	// can answer without requiring the Publisher to expose a listing method.
	a.mu.Lock()
	a.published[req.Slug] = protocol.PublishedName{Slug: req.Slug, Name: name, State: "established"}
	a.mu.Unlock()
	slog.Info("publish", "slug", req.Slug, "name", name, "state", "established")
	writeJSON(w, http.StatusOK, protocol.PublishResponse{Name: name, State: "established"})
}

func (a *Agent) unpublish(w http.ResponseWriter, r *http.Request) {
	var req protocol.UnpublishRequest
	if !decode(w, r, &req) {
		return
	}
	if err := a.Publisher.Unpublish(req.Slug); err != nil {
		slog.Error("unpublish: publisher error", "slug", req.Slug, "err", err)
		writeErr(w, http.StatusInternalServerError, "unpublish-failed", err.Error())
		return
	}
	// Keep write-through cache in sync.
	a.mu.Lock()
	delete(a.published, req.Slug)
	a.mu.Unlock()
	slog.Info("unpublish", "slug", req.Slug)
	writeJSON(w, http.StatusOK, struct{}{})
}

func (a *Agent) discoveryState(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	names := make([]protocol.PublishedName, 0, len(a.published))
	for _, n := range a.published {
		names = append(names, n)
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, protocol.DiscoveryState{
		Publisher:  "avahi-fake",
		HostName:   "malmo",
		RenamedTo:  nil,
		Published:  names,
		Interfaces: []string{"eth0"},
	})
}

func (a *Agent) systemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.SystemStatus{
		Hostname:     "malmo-dev",
		UptimeS:      int64(time.Since(a.startedAt).Seconds()),
		DiskPressure: false,
		AgentVersion: AgentVersion,
	})
}

// verifyPassword delegates to a.Verifier so the verification strategy
// (fake bcrypt map vs. real PAM) is swapped per binary.
//
// Per BRAIN_HOST_PROTOCOL.md: the response is always {valid: bool} — we never
// reveal *why* verification failed (wrong password, unknown user, locked
// account, PAM config error). Even a Verifier transport/config error returns
// {valid: false} rather than a 5xx so the brain's rate-limiter sees a clean
// false and the brain never leaks the distinction.
func (a *Agent) verifyPassword(w http.ResponseWriter, r *http.Request) {
	var req protocol.VerifyPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	ok, err := a.Verifier.Verify(req.User, req.Password)
	if err != nil {
		slog.Error("verify-password: verifier error", "user", req.User, "err", err)
		// Never reveal why — return false, not 5xx. See doc comment above.
		writeJSON(w, http.StatusOK, protocol.VerifyPasswordResponse{Valid: false})
		return
	}
	writeJSON(w, http.StatusOK, protocol.VerifyPasswordResponse{Valid: ok})
}

// setPassword is upsert per BRAIN_HOST_PROTOCOL.md: creates the user if
// missing, otherwise updates the password.
//
// When UserMgr is non-nil (cmd/host-agent-real), delegates to UpsertPassword
// which writes to /etc/shadow via useradd+chpasswd. When nil (cmd/host-agent),
// writes a bcrypt hash to the in-memory map used by FakeVerifier so the fake
// binary's tests and the bootstrap flow (POST /setup → SetPassword) still work.
//
// Never reveals system-level failure detail in the HTTP response body — same
// posture as verify-password. The structured log captures the underlying error.
func (a *Agent) setPassword(w http.ResponseWriter, r *http.Request) {
	var req protocol.SetPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if req.User == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "user and password are required")
		return
	}

	if a.UserMgr != nil {
		if err := a.UserMgr.UpsertPassword(req.User, req.Password); err != nil {
			slog.Error("set-password: user-manager error", "user", req.User, "err", err)
			writeErr(w, http.StatusInternalServerError, "set-password-failed", "set-password failed")
			return
		}
		slog.Info("set-password", "user", req.User)
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash-failed", err.Error())
		return
	}
	a.mu.Lock()
	a.passwords[req.User] = hash
	a.mu.Unlock()
	slog.Info("set-password", "user", req.User)
	writeJSON(w, http.StatusOK, struct{}{})
}

// setRole updates Linux group membership to match the role.
//
// When UserMgr is non-nil (cmd/host-agent-real), delegates to SetRole which
// runs `gpasswd -a/-d <user> sudo`. When nil (cmd/host-agent), records the
// role in the in-memory map. Body never leaks system detail on error — same
// opaque-error posture as verify-password / set-password.
func (a *Agent) setRole(w http.ResponseWriter, r *http.Request) {
	var req protocol.SetRoleRequest
	if !decode(w, r, &req) {
		return
	}
	if req.User == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "user is required")
		return
	}
	if req.Role != "admin" && req.Role != "member" {
		writeErr(w, http.StatusBadRequest, "bad-request", "role must be admin or member")
		return
	}

	if a.UserMgr != nil {
		if err := a.UserMgr.SetRole(req.User, req.Role); err != nil {
			slog.Error("set-role: user-manager error", "user", req.User, "role", req.Role, "err", err)
			writeErr(w, http.StatusInternalServerError, "set-role-failed", "set-role failed")
			return
		}
		slog.Info("set-role", "user", req.User, "role", req.Role)
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}

	a.mu.Lock()
	a.roles[req.User] = req.Role
	a.mu.Unlock()
	slog.Info("set-role", "user", req.User, "role", req.Role)
	writeJSON(w, http.StatusOK, struct{}{})
}

// deleteUser removes the user. When UserMgr is wired (cmd/host-agent-real),
// delegates to UserMgr.DeleteUser (userdel -r -f); otherwise drops the entry
// from the in-memory fake maps. Idempotent per BRAIN_HOST_PROTOCOL.md # Auth
// endpoints: unknown user returns 200.
func (a *Agent) deleteUser(w http.ResponseWriter, r *http.Request) {
	var req protocol.DeleteUserRequest
	if !decode(w, r, &req) {
		return
	}
	if req.User == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "user is required")
		return
	}

	if a.UserMgr != nil {
		if err := a.UserMgr.DeleteUser(req.User); err != nil {
			slog.Error("delete-user: user-manager error", "user", req.User, "err", err)
			writeErr(w, http.StatusInternalServerError, "delete-user-failed", "delete-user failed")
			return
		}
		slog.Info("delete-user", "user", req.User)
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}

	a.mu.Lock()
	delete(a.passwords, req.User)
	delete(a.roles, req.User)
	a.mu.Unlock()
	slog.Info("delete-user", "user", req.User)
	writeJSON(w, http.StatusOK, struct{}{})
}

// --- HTTP helpers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad-json", err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, protocol.Error{Code: code, Message: msg})
}

// LogRequests is a minimal middleware that lets the binary log requests if desired.
// Currently a no-op (mirrors the fake's original stub); exported so cmd/ can
// wrap with its own logger if needed.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
