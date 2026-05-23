// Command host-agent is, for now, a FAKE host-agent: it speaks the real
// BRAIN_HOST_PROTOCOL.md wire format over a real UNIX socket, but its host
// operations are canned (no Avahi, no LUKS, no apt). This lets the brain be
// developed against a faithful protocol seam before the real host-agent
// (DBus/systemd/cryptsetup) exists. See BRAIN_HOST_PROTOCOL.md.
package main

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/malmo/malmo/internal/protocol"
)

const agentVersion = "0.0.1-fake"

type agent struct {
	mu        sync.Mutex
	published map[string]protocol.PublishedName
	startedAt time.Time
}

func main() {
	sockPath := os.Getenv("MALMO_AGENT_SOCK")
	if sockPath == "" {
		sockPath = protocol.SocketPath
	}

	// Remove a stale socket from a previous run.
	if err := os.RemoveAll(sockPath); err != nil {
		slog.Error("remove stale socket", "sock", sockPath, "err", err)
		os.Exit(1)
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		slog.Error("listen", "sock", sockPath, "err", err)
		os.Exit(1)
	}
	defer ln.Close()
	// 0660 root:malmo in prod; in dev we just make it group-accessible.
	_ = os.Chmod(sockPath, 0o660)

	a := &agent{
		published: map[string]protocol.PublishedName{},
		startedAt: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/discovery/publish", a.publish)
	mux.HandleFunc("POST /v1/discovery/unpublish", a.unpublish)
	mux.HandleFunc("GET /v1/discovery/state", a.discoveryState)
	mux.HandleFunc("GET /v1/system/status", a.systemStatus)

	slog.Info("host-agent (fake) listening", "sock", sockPath)
	srv := &http.Server{Handler: logRequests(mux)}
	if err := srv.Serve(ln); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func (a *agent) publish(w http.ResponseWriter, r *http.Request) {
	var req protocol.PublishRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Slug == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "slug is required")
		return
	}
	// Real host-agent writes /etc/avahi/services/app-<slug>.service and waits
	// on Avahi DBus for ESTABLISHED. We fake the propagation delay and result.
	name := req.Slug + ".malmo.local"
	a.mu.Lock()
	a.published[req.Slug] = protocol.PublishedName{Slug: req.Slug, Name: name, State: "established"}
	a.mu.Unlock()
	slog.Info("publish", "slug", req.Slug, "name", name, "state", "established")
	writeJSON(w, http.StatusOK, protocol.PublishResponse{Name: name, State: "established"})
}

func (a *agent) unpublish(w http.ResponseWriter, r *http.Request) {
	var req protocol.UnpublishRequest
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	delete(a.published, req.Slug) // idempotent: unknown slug -> 200
	a.mu.Unlock()
	slog.Info("unpublish", "slug", req.Slug)
	writeJSON(w, http.StatusOK, struct{}{})
}

func (a *agent) discoveryState(w http.ResponseWriter, r *http.Request) {
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

func (a *agent) systemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.SystemStatus{
		Hostname:     "malmo-dev",
		UptimeS:      int64(time.Since(a.startedAt).Seconds()),
		DiskPressure: false,
		AgentVersion: agentVersion,
	})
}

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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		_ = r
	})
}
