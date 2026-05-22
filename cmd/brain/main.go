// Command brain is malmo-brain — the control-plane daemon (CONTROL_PLANE.md).
// In production it runs as a container supervised by host-agent; in dev it runs
// natively (`go run`) against the local Docker socket and the fake host-agent.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/malmo/malmo/internal/api"
	"github.com/malmo/malmo/internal/caddy"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/hostclient"
	"github.com/malmo/malmo/internal/lifecycle"
	"github.com/malmo/malmo/internal/protocol"
	"github.com/malmo/malmo/internal/store"
)

func main() {
	cfg := loadConfig()

	if err := os.MkdirAll(cfg.stateDir, 0o755); err != nil {
		log.Fatalf("create state dir: %v", err)
	}

	st, err := store.Open(filepath.Join(cfg.stateDir, "malmo.db"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	cat := catalog.New(cfg.catalogDir)
	host := hostclient.New(cfg.agentSock)
	cd := caddy.New(cfg.caddyAdmin)
	bus := events.NewBus()
	life := lifecycle.NewManager(st, cat, host, cd, bus, cfg.stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	life.EnsureIngress(ctx, cfg.caddyListen)
	cancel()

	if status, err := host.SystemStatus(context.Background()); err != nil {
		log.Printf("host-agent not reachable at %s (host ops will fail): %v", cfg.agentSock, err)
	} else {
		log.Printf("host-agent ok: %s (v%s)", status.Hostname, status.AgentVersion)
	}

	srv := api.NewServer(st, cat, life, bus)
	httpSrv := &http.Server{Addr: cfg.listen, Handler: srv.Handler()}
	log.Printf("malmo-brain listening on %s (state=%s catalog=%s)", cfg.listen, cfg.stateDir, cfg.catalogDir)
	log.Fatal(httpSrv.ListenAndServe())
}

type config struct {
	listen      string
	stateDir    string
	catalogDir  string
	agentSock   string
	caddyAdmin  string
	caddyListen string
}

func loadConfig() config {
	return config{
		listen:      env("MALMO_LISTEN", ":8080"),
		stateDir:    env("MALMO_STATE_DIR", "./.dev/state"),
		catalogDir:  env("MALMO_CATALOG_DIR", "./catalog"),
		agentSock:   env("MALMO_AGENT_SOCK", protocol.SocketPath),
		caddyAdmin:  env("MALMO_CADDY_ADMIN", "http://localhost:2019"),
		caddyListen: env("MALMO_CADDY_LISTEN", ":80"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
