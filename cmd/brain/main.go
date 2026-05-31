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
	"github.com/malmo/malmo/internal/audit"
	"github.com/malmo/malmo/internal/auth"
	"github.com/malmo/malmo/internal/caddy"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/health"
	"github.com/malmo/malmo/internal/hostclient"
	"github.com/malmo/malmo/internal/lifecycle"
	"github.com/malmo/malmo/internal/notify"
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
	// Catch-all 404: ensure the tail route is present after reconcile has
	// re-populated per-app routes at index 0. Best-effort — Caddy being
	// transiently unreachable here doesn't block the brain from serving.
	if err := cd.EnsureCatchAll(ctx); err != nil {
		slog.Warn("caddy: ensure catch-all failed; continuing", "err", err)
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
	auditor := audit.New(st)
	notifier := notify.New(st, bus)
	healthMgr := health.NewManager(st)

	// Restore persisted health issues before serving requests. Non-fatal:
	// the brain starts with an empty registry on error (degraded, not dead).
	if err := healthMgr.LoadFromStore(); err != nil {
		slog.Warn("health: failed to restore issues from store; starting empty", "err", err)
	}

	// Pull the boot-time storage findings (BOOT.md # The storage-ready
	// target) once at startup and reconcile them into the health registry,
	// then keep refreshing on a slow poll. Failure here is non-fatal — the
	// brain runs degraded just like everything else.
	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()
	pullStorageHealth(pollCtx, host, healthMgr, auditor, notifier)
	go storageHealthPollLoop(pollCtx, host, healthMgr, auditor, notifier, cfg.healthPollPeriod)

	// Bound the notifications table (NOTIFICATIONS.md # Locked decisions, the
	// retention bullet): prune aged / over-cap rows once at boot, then on a slow
	// loop. Non-fatal — an unbounded
	// table degrades gracefully (the bell just carries more history).
	if err := st.PruneNotifications(time.Now()); err != nil {
		slog.Warn("notification prune failed; continuing", "err", err)
	}
	go notificationPruneLoop(pollCtx, st, cfg.notifyPrunePeriod)

	srv := api.NewServer(st, cat, life, bus, authMgr, host, auditor, healthMgr)
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
	listen            string
	stateDir          string
	catalogDir        string
	agentSock         string
	caddyAdmin        string
	caddyListen       string
	logLevel          string
	logFormat         string
	healthPollPeriod  time.Duration
	notifyPrunePeriod time.Duration
}

func loadConfig() config {
	return config{
		listen:            env("MALMO_LISTEN", ":8080"),
		stateDir:          env("MALMO_STATE_DIR", "./.dev/state"),
		catalogDir:        env("MALMO_CATALOG_DIR", "./catalog"),
		agentSock:         env("MALMO_AGENT_SOCK", protocol.SocketPath),
		caddyAdmin:        env("MALMO_CADDY_ADMIN", "http://localhost:2019"),
		caddyListen:       env("MALMO_CADDY_LISTEN", ":80"),
		logLevel:          env("MALMO_LOG_LEVEL", "info"),
		logFormat:         env("MALMO_LOG_FORMAT", "text"),
		healthPollPeriod:  envDuration("MALMO_HEALTH_POLL", 60*time.Second),
		notifyPrunePeriod: envDuration("MALMO_NOTIFY_PRUNE", time.Hour),
	}
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("invalid duration, using default", "var", k, "value", v, "default", def)
	}
	return def
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// pullStorageHealth fetches the current storage findings from host-agent and
// reconciles them into the health registry. Non-blocking: if host-agent isn't
// reachable yet, we log and return — the brain still starts. The poll loop
// will catch up once host-agent comes online. Transitions are audited per
// issue (see emitHealthTransitions) and fan out to the notification center
// (see emitHealthNotifications).
func pullStorageHealth(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sh, err := host.StorageHealth(c)
	if err != nil {
		slog.Warn("storage health: host-agent unreachable; skipping",
			"err", err)
		return
	}
	raised, cleared := healthMgr.ApplyStorageFindings(sh)
	if len(raised) > 0 || len(cleared) > 0 {
		slog.Info("storage health: reconciled",
			"raised", len(raised), "cleared", len(cleared), "active_findings", len(sh.Findings))
	}
	emitHealthTransitions(ctx, auditor, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// emitHealthTransitions writes one audit record per transitioned health issue,
// targeting {kind: health_issue, id: <id>}, so the Activity view attributes
// each raise/clear to a specific issue rather than a bulk count. No-op when
// both slices are empty — the steady-state case, since most polls change
// nothing.
func emitHealthTransitions(ctx context.Context, auditor *audit.Recorder, raised, cleared []health.IssueKey) {
	for _, k := range raised {
		auditor.Record(ctx, audit.ActionHealthIssueRaised,
			audit.Target{Kind: "health_issue", ID: k.ID}, nil, true)
	}
	for _, k := range cleared {
		auditor.Record(ctx, audit.ActionHealthIssueCleared,
			audit.Target{Kind: "health_issue", ID: k.ID}, nil, true)
	}
}

// emitHealthNotifications fans transitioned health issues out to the
// notification center (NOTIFICATIONS.md): a notification per allowlisted
// raise, a resolve per allowlisted clear. The allowlist gate lives in
// notify, so this dispatches every transition and lets notify drop the ones
// that don't notify. Raises need the issue's severity/summary, which
// ApplyStorageFindings doesn't return — look it up by key. The ok guard skips
// a key with no live issue; in the current sequential poll a just-raised key
// is always still active when looked up, so this is defensive (and notify
// would drop the resulting zero-value Issue anyway, since "" isn't allowlisted).
func emitHealthNotifications(notifier *notify.Notifier, healthMgr *health.Manager, raised, cleared []health.IssueKey) {
	for _, k := range raised {
		if iss, ok := healthMgr.Get(k.ID, k.InstanceKey); ok {
			notifier.HealthRaised(iss)
		}
	}
	for _, k := range cleared {
		notifier.HealthCleared(k.ID, k.InstanceKey)
	}
}

// storageHealthPollLoop keeps the health registry in sync with what
// host-agent reports. 60s is the loose-by-design cadence — storage findings
// don't change often, and the dashboard's view of "active issues" gets a
// refresh on every dashboard load via the same registry.
func storageHealthPollLoop(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pullStorageHealth(ctx, host, healthMgr, auditor, notifier)
		}
	}
}

// notificationPruneLoop bounds the notifications table on a slow cadence
// (NOTIFICATIONS.md # Locked decisions, the retention bullet). Hourly by
// default — retention is
// housekeeping, not latency-sensitive. Each tick is independently best-effort;
// a failure logs and the next tick retries.
func notificationPruneLoop(ctx context.Context, st *store.Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := st.PruneNotifications(time.Now()); err != nil {
				slog.Warn("notification prune failed; continuing", "err", err)
			}
		}
	}
}
