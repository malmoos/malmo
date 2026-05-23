// Command brain is malmo-brain — the control-plane daemon (CONTROL_PLANE.md).
// In production it runs as a container supervised by host-agent; in dev it runs
// natively (`go run`) against the local Docker socket and the fake host-agent.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/malmo/malmo/internal/api"
	"github.com/malmo/malmo/internal/auth"
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
	installLogger(cfg.logLevel, cfg.logFormat)

	if err := os.MkdirAll(cfg.stateDir, 0o755); err != nil {
		fatal("create state dir", "err", err)
	}

	st, err := store.Open(filepath.Join(cfg.stateDir, "malmo.db"))
	if err != nil {
		fatal("open store", "err", err)
	}
	defer st.Close()

	cat := catalog.New(cfg.catalogDir)
	host := hostclient.New(cfg.agentSock)
	cd := caddy.New(cfg.caddyAdmin)
	bus := events.NewBus()
	life := lifecycle.NewManager(st, cat, host, cd, lifecycle.NewCLIDocker(), bus, cfg.stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	life.EnsureIngress(ctx, cfg.caddyListen)
	// Startup reconcile: converge Docker + routing to SQLite desired state and
	// re-assert Caddy routes lost when EnsureIngress reset the server block.
	if err := life.Reconcile(ctx); err != nil {
		slog.Warn("startup reconcile failed", "err", err)
	}
	cancel()

	if status, err := host.SystemStatus(context.Background()); err != nil {
		slog.Warn("host-agent not reachable; host ops will fail",
			"sock", cfg.agentSock, "err", err)
	} else {
		slog.Info("host-agent ready",
			"hostname", status.Hostname, "agent_version", status.AgentVersion)
	}

	authMgr := auth.NewManager(st)
	srv := api.NewServer(st, cat, life, bus, authMgr, host)
	httpSrv := &http.Server{Addr: cfg.listen, Handler: srv.Handler()}
	slog.Info("malmo-brain listening",
		"listen", cfg.listen, "state_dir", cfg.stateDir, "catalog_dir", cfg.catalogDir)
	if err := httpSrv.ListenAndServe(); err != nil {
		fatal("http server", "err", err)
	}
}

// installLogger replaces the default slog handler with a TextHandler at the
// configured level. Single-process appliance → setting the default once and
// using slog.Default() everywhere beats threading a *slog.Logger through every
// constructor.
func installLogger(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.ToLower(format) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// fatal is the slog equivalent of log.Fatalf: structured Error + exit(1).
// Used only for startup failures we genuinely can't recover from.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

type config struct {
	listen      string
	stateDir    string
	catalogDir  string
	agentSock   string
	caddyAdmin  string
	caddyListen string
	logLevel    string
	logFormat   string
}

func loadConfig() config {
	return config{
		listen:      env("MALMO_LISTEN", ":8080"),
		stateDir:    env("MALMO_STATE_DIR", "./.dev/state"),
		catalogDir:  env("MALMO_CATALOG_DIR", "./catalog"),
		agentSock:   env("MALMO_AGENT_SOCK", protocol.SocketPath),
		caddyAdmin:  env("MALMO_CADDY_ADMIN", "http://localhost:2019"),
		caddyListen: env("MALMO_CADDY_LISTEN", ":80"),
		logLevel:    env("MALMO_LOG_LEVEL", "info"),
		logFormat:   env("MALMO_LOG_FORMAT", "text"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
