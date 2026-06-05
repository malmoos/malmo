// Package hostagent contains the shared HTTP handler layer for both
// cmd/host-agent (fake) and cmd/host-agent-real. It speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a UNIX socket.
package hostagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/molmaos/molma/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

// ErrUnknownUser is returned by UserManager.ResolveHome when the user does not
// exist on the host system. The handler maps this to a 404 with code
// "unknown-user" so the brain can distinguish "user gone" from "host error."
var ErrUnknownUser = errors.New("unknown user")

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

// HealthSource is a consumer-side interface for reading the latest storage
// findings written by molma-storage-verify (BOOT.md # The storage-ready
// target). It backs the storage category of GET /v1/health/system. Provider
// packages return concrete types: FakeHealthSource for the fake binary,
// healthsource.FilesystemHealthSource (which reads
// /run/molma/health/storage.json) for cmd/host-agent-real.
//
// Read must always return a usable StorageHealth — missing report = empty
// findings, malformed report = a single "health-report-malformed" finding.
// host-agent never propagates "I couldn't read the file" as an HTTP error;
// the brain needs a clean payload to drive its health registry.
type HealthSource interface {
	Read() (protocol.StorageHealth, error)
}

// ServiceReporter is a consumer-side interface for the locus-B service-down
// detector: `systemctl is-active` over the core-unit allowlist, one
// service-down Finding (with the unit as instance_key) per non-active unit. It
// backs the services category of GET /v1/health/system. Provider packages return
// concrete types: servicehealth.Reporter (real systemctl) for cmd/host-agent-real,
// FakeServiceReporter for the fake binary and tests.
//
// Read always returns a usable slice (nil = all services healthy) and never
// errors — inactive units are data, not failures.
type ServiceReporter interface {
	Read() []protocol.Finding
}

// ClockReporter is a consumer-side interface for the locus-B clock-not-synced
// detector: it parses `chronyc tracking` and emits a clock-not-synced finding
// when the host clock hasn't synced in 6h or its offset exceeds 10s. It backs the
// time category of GET /v1/health/system. Provider packages return concrete
// types: clockhealth.Reporter (real chronyc) for cmd/host-agent-real,
// FakeClockReporter for the fake binary and tests.
//
// Read always returns a usable slice (nil = clock healthy) and never errors —
// like ServiceReporter, an out-of-sync clock is data, not a failure.
type ClockReporter interface {
	Read() []protocol.Finding
}

// LogSource is a consumer-side interface for the per-app log tail behind
// GET /v1/journal/follow (LOGGING.md # Per-app logs, BRAIN_HOST_PROTOCOL.md
// # Pattern C). Follow returns a channel of log lines for the given container;
// the channel is closed when the source ends (the underlying follower exits)
// and the follow stops when ctx is cancelled (the brain disconnected). Provider
// packages return concrete types: journalsource.Reader (real
// `journalctl CONTAINER_NAME=<container> -f`) for cmd/host-agent-real,
// FakeLogSource (a synthetic ticker) for the fake binary and tests.
//
// When nil (no source wired), GET /v1/journal/follow returns 501 so the brain
// surfaces "logs unavailable" rather than a silently empty stream.
type LogSource interface {
	Follow(ctx context.Context, container string) (<-chan protocol.JournalLine, error)
}

// RAMReporter is a consumer-side interface for the locus-B ram-pressure
// detector: it reads the kernel's PSI memory file (/proc/pressure/memory, the
// `some avg60` field) and emits a ram-pressure finding when memory-stall
// pressure is sustained above threshold. It backs the resources category of GET
// /v1/health/system. Provider packages return concrete types:
// rampressure.Reporter (real /proc) for cmd/host-agent-real, FakeRAMReporter for
// the fake binary and tests.
//
// Read always returns a usable slice (nil = pressure below threshold) and never
// errors — like ServiceReporter, a PSI reading is data, not a failure; a missing
// PSI file fails open to nil.
type RAMReporter interface {
	Read() []protocol.Finding
}

// UserManager is a consumer-side interface for the system-level user account
// operations behind /v1/auth/set-password (and, later, /set-role and
// /delete-user). Provider packages (usermgr.LinuxUserManager) export concrete
// types only.
//
// UpsertPassword is upsert: creates the user if missing, otherwise updates the
// password. SetRole updates Linux group membership to match the role
// (admin → in `sudo`, member → not in `sudo`); idempotent. ResolveHome returns
// the user's home directory path and POSIX UID/GID; returns ErrUnknownUser when
// the user does not exist. WellKnownIdentity returns the fixed service-account
// UIDs/GIDs for molma-app and molma-shared. See BRAIN_HOST_PROTOCOL.md # User
// info endpoints and USERS_AND_GROUPS.md # Roles.
type UserManager interface {
	UpsertPassword(user, password string) error
	SetRole(user, role string) error
	DeleteUser(user string) error
	ResolveHome(user string) (home string, uid, gid int, err error)
	WellKnownIdentity() (appUID, appGID, sharedGID int, err error)
}

// Agent is the HTTP handler set for host-agent. It holds both the
// PasswordVerifier (swapped per binary) and the in-memory fake maps used by
// setPassword / setRole / deleteUser when UserMgr is nil (the fake binary).
// cmd/host-agent-real wires UserMgr so all three delegate to /etc/passwd via
// usermgr.LinuxUserManager.
type Agent struct {
	mu sync.Mutex
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
	// statePath, when non-empty, backs passwords+roles with a JSON file so the
	// fake binary's accounts survive a restart (a dev stand-in for /etc/shadow,
	// which the real agent persists for free). Empty by default — tests and the
	// real binary keep the maps purely in memory. Set via EnablePersistence.
	statePath string
	startedAt time.Time

	// Verifier handles POST /v1/auth/verify-password.
	// Swapped per binary: FakeVerifier (fake) vs PAMVerifier (real).
	Verifier PasswordVerifier

	// Publisher handles POST /v1/discovery/publish and /v1/discovery/unpublish.
	// Swapped per binary: FakePublisher (fake) vs avahipublisher.FilePublisher (real).
	Publisher Publisher

	// Health, when non-nil, backs the storage category of GET /v1/health/system.
	// Swapped per binary: FakeHealthSource (fake) vs
	// healthsource.FilesystemHealthSource (real). When nil, the storage category
	// reports empty findings — useful for the fake binary in dev where no
	// storage-verify reporter is running.
	Health HealthSource

	// Services, when non-nil, backs the services category of GET /v1/health/system
	// (the service-down detector). Swapped per binary: servicehealth.Reporter
	// (real systemctl) vs FakeServiceReporter. When nil, the report omits the
	// services category entirely — the brain then leaves service issues alone
	// rather than treating "no services measured" as "all services up".
	Services ServiceReporter

	// Time, when non-nil, backs the time category of GET /v1/health/system (the
	// clock-not-synced detector). Swapped per binary: clockhealth.Reporter (real
	// chronyc) vs FakeClockReporter. When nil, the report omits the time category
	// entirely — the brain then leaves clock issues alone rather than treating
	// "no clock measured" as "clock healthy".
	Time ClockReporter

	// Logs, when non-nil, backs GET /v1/journal/follow (the per-app log tail).
	// Swapped per binary: journalsource.Reader (real journalctl) for
	// cmd/host-agent-real vs FakeLogSource (synthetic ticker) for the fake
	// binary and tests. When nil, GET /v1/journal/follow returns 501.
	Logs LogSource

	// Resources, when non-nil, backs the resources category of GET
	// /v1/health/system (the ram-pressure detector). Swapped per binary:
	// rampressure.Reporter (real /proc/pressure/memory) vs FakeRAMReporter. When
	// nil, the report omits the resources category entirely — the brain then
	// leaves resource issues alone rather than treating "no pressure measured"
	// as "pressure healthy".
	Resources RAMReporter

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
	mux.HandleFunc("GET /v1/system/resources", a.systemResources)
	mux.HandleFunc("POST /v1/auth/verify-password", a.verifyPassword)
	mux.HandleFunc("POST /v1/auth/set-password", a.setPassword)
	mux.HandleFunc("POST /v1/auth/set-role", a.setRole)
	mux.HandleFunc("POST /v1/auth/delete-user", a.deleteUser)
	mux.HandleFunc("GET /v1/health/system", a.systemHealth)
	mux.HandleFunc("GET /v1/journal/follow", a.journalFollow)
	mux.HandleFunc("GET /v1/users/{username}/home", a.resolveHome)
	mux.HandleFunc("GET /v1/identity/well-known", a.wellKnownIdentity)
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
		HostName:   "molma",
		RenamedTo:  nil,
		Published:  names,
		Interfaces: []string{"eth0"},
	})
}

func (a *Agent) systemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.SystemStatus{
		Hostname:     "molma-dev",
		UptimeS:      int64(time.Since(a.startedAt).Seconds()),
		DiskPressure: false,
		AgentVersion: AgentVersion,
	})
}

// systemResources serves one raw cumulative-counter sample for the live
// system-resources view (BRAIN_HOST_PROTOCOL.md # Pattern A). The real binary
// reads /proc and /sys and applies the iface/device allowlist; this fake
// synthesizes monotonically-climbing counters off a.startedAt so two successive
// 1 Hz polls always diff to a non-zero, plausible rate in the dev loop. It is
// stateless (no Agent field, no mutex) — the same property the spec requires of
// the real implementation. ts_ns advances on every call so the brain's rate
// denominator (ts_ns delta) is always positive.
func (a *Agent) systemResources(w http.ResponseWriter, r *http.Request) {
	elapsed := time.Since(a.startedAt)
	secs := elapsed.Seconds()
	writeJSON(w, http.StatusOK, protocol.SystemResources{
		TsNs:    time.Now().UnixNano(),
		CPU:     protocol.CPUCounters{TotalJiffies: int64(secs * 400), IdleJiffies: int64(secs * 300)},
		LoadAvg: [3]float64{0.42, 0.51, 0.48},
		Mem: protocol.MemCounters{
			TotalBytes:     16728338432,
			AvailableBytes: 9214455808,
			UsedBytes:      7513882624,
		},
		Net: []protocol.NetCounters{
			{Iface: "eth0", RxBytes: int64(secs * 120000), TxBytes: int64(secs * 48000)},
		},
		Disk: []protocol.DiskCounters{
			{Dev: "sda", ReadBytes: int64(secs * 90000), WriteBytes: int64(secs * 14000)},
		},
		UptimeS: int64(secs),
	})
}

// systemHealth returns the locus-B findings report across categories
// (HEALTH.md # Detector catalog). The storage category is always present
// (empty findings when no source is wired — "storage looks healthy" per
// BOOT.md); the services category is present only when a service reporter is
// wired, so the brain doesn't read "no services measured" as "all services up".
//
// Even when a source returns an error, the handler returns 200 with whatever
// payload the source produced (the storage source synthesizes a
// "health-report-malformed" finding on parse error and an empty list on a
// missing file). The contract: 200 always, payload always parseable. The
// brain's polling loop must never have to retry on a 5xx for this endpoint.
func (a *Agent) systemHealth(w http.ResponseWriter, r *http.Request) {
	cats := map[protocol.HealthCategory][]protocol.Finding{}

	// Storage category (locus A/B): always measured.
	storage := []protocol.Finding{}
	if a.Health != nil {
		sh, err := a.Health.Read()
		if err != nil {
			slog.Error("system-health: storage source error", "err", err)
		}
		if sh.Findings != nil {
			storage = sh.Findings
		}
	}
	cats[protocol.HealthCategoryStorage] = storage

	// Services category (locus B): only when a service reporter is wired.
	if a.Services != nil {
		svc := a.Services.Read()
		if svc == nil {
			svc = []protocol.Finding{}
		}
		cats[protocol.HealthCategoryServices] = svc
	}

	// Time category (locus B): only when a clock reporter is wired.
	if a.Time != nil {
		clk := a.Time.Read()
		if clk == nil {
			clk = []protocol.Finding{}
		}
		cats[protocol.HealthCategoryTime] = clk
	}

	// Resources category (locus B): only when a RAM reporter is wired.
	if a.Resources != nil {
		res := a.Resources.Read()
		if res == nil {
			res = []protocol.Finding{}
		}
		cats[protocol.HealthCategoryResources] = res
	}

	writeJSON(w, http.StatusOK, protocol.SystemHealth{
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		Categories: cats,
	})
}

// journalFollow streams a container's log lines as SSE (BRAIN_HOST_PROTOCOL.md
// # Pattern C — the per-app log tail behind the dashboard Logs tab). Each line
// is one `id: <n>\ndata: {json}\n\n` frame with a per-connection monotonic id;
// no `event:` field, so the brain (and a curl) read them as default-type
// messages.
//
// Reconnect: the follower here is fresh per connection with no history before
// "now", so a reconnect carrying Last-Event-ID can't be replayed — the handler
// emits one {"lost":true} frame and resumes live (the spec's buffer-overflow
// path). The authoritative rolling-buffer replay lives one hop up, in the
// brain's per-instance log hub (BRAIN_UI_PROTOCOL.md Pattern C: "the brain
// re-emits IDs from its own monotonic counter"); a host-side shared-follower
// buffer is deferred until a second consumer (job / Tier-2 service logs) needs it.
func (a *Agent) journalFollow(w http.ResponseWriter, r *http.Request) {
	container := r.URL.Query().Get("container")
	if container == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "container is required")
		return
	}
	if a.Logs == nil {
		writeErr(w, http.StatusNotImplemented, "logs-unsupported", "this host-agent has no log source")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no-flush", "streaming unsupported")
		return
	}

	ch, err := a.Logs.Follow(r.Context(), container)
	if err != nil {
		slog.Error("journal-follow: source error", "container", container, "err", err)
		writeErr(w, http.StatusInternalServerError, "journal-follow-failed", "could not follow logs")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var id uint64
	// A reconnect carrying Last-Event-ID can't be replayed by a fresh follower —
	// emit one {"lost":true} so the consumer knows the gap, then resume live.
	if r.Header.Get("Last-Event-ID") != "" {
		id++
		writeJournalFrame(w, flusher, id, protocol.JournalLine{Lost: true})
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			id++
			writeJournalFrame(w, flusher, id, line)
		}
	}
}

func writeJournalFrame(w http.ResponseWriter, f http.Flusher, id uint64, line protocol.JournalLine) {
	data, _ := json.Marshal(line)
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, data)
	f.Flush()
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
	a.persistLocked()
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
	a.persistLocked()
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
	a.persistLocked()
	a.mu.Unlock()
	slog.Info("delete-user", "user", req.User)
	writeJSON(w, http.StatusOK, struct{}{})
}

// resolveHome returns the user's home directory path, UID, and GID.
//
// When UserMgr is wired (cmd/host-agent-real), delegates to ResolveHome which
// reads /etc/passwd via os/user.Lookup. When nil (cmd/host-agent), returns a
// deterministic fake: /home/<username> and a stable UID/GID derived from the
// username so the dev loop (fake agent over socket) produces coherent output.
//
// 404 with code "unknown-user" when the real manager reports the user is gone.
// The brain maps this to a 422 or installation error, not a 500 retry.
func (a *Agent) resolveHome(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if username == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "username is required")
		return
	}

	if a.UserMgr != nil {
		home, uid, gid, err := a.UserMgr.ResolveHome(username)
		if err != nil {
			if errors.Is(err, ErrUnknownUser) {
				writeErr(w, http.StatusNotFound, "unknown-user", "user not found")
				return
			}
			slog.Error("resolve-home: user-manager error", "username", username, "err", err)
			writeErr(w, http.StatusInternalServerError, "resolve-home-failed", "resolve-home failed")
			return
		}
		slog.Info("resolve-home", "username", username, "home", home)
		writeJSON(w, http.StatusOK, protocol.ResolveHomeResponse{HomePath: home, UID: uid, GID: gid})
		return
	}

	// Fake branch: deterministic home path and UID/GID from the username.
	uid := fakeUID(username)
	slog.Info("resolve-home (fake)", "username", username, "uid", uid)
	writeJSON(w, http.StatusOK, protocol.ResolveHomeResponse{
		HomePath: "/home/" + username,
		UID:      uid,
		GID:      uid,
	})
}

// wellKnownIdentity returns the fixed service-account UIDs/GIDs for the
// molma-app system user and the molma-shared group.
//
// When UserMgr is wired (cmd/host-agent-real), delegates to WellKnownIdentity
// which resolves the real system user/group via os/user.Lookup. When nil
// (cmd/host-agent fake), returns fixed dev constants that sit below the
// per-user FNV hash range [3000, 3999] so service identities don't collide.
func (a *Agent) wellKnownIdentity(w http.ResponseWriter, r *http.Request) {
	if a.UserMgr != nil {
		appUID, appGID, sharedGID, err := a.UserMgr.WellKnownIdentity()
		if err != nil {
			slog.Error("well-known-identity: user-manager error", "err", err)
			writeErr(w, http.StatusInternalServerError, "well-known-identity-failed", "well-known-identity failed")
			return
		}
		writeJSON(w, http.StatusOK, protocol.WellKnownIdentityResponse{
			MolmaAppUID:    appUID,
			MolmaAppGID:    appGID,
			MolmaSharedGID: sharedGID,
		})
		return
	}

	// Fake branch: fixed dev constants.
	// 2000/2001 sit below the per-user FNV hash range [3000, 3999] so service
	// identities don't collide with hashed user UIDs in the fake host-agent.
	writeJSON(w, http.StatusOK, protocol.WellKnownIdentityResponse{
		MolmaAppUID:    2000,
		MolmaAppGID:    2000,
		MolmaSharedGID: 2001,
	})
}

// fakeUID returns a stable UID in the range [3000, 3999] for the given username
// using FNV-32a so the fake host-agent's resolve-home responses are coherent
// across calls without persisting state.
func fakeUID(username string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(username))
	return 3000 + int(h.Sum32()%1000)
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
