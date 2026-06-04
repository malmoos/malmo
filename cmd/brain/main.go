// Command brain is molma-brain — the control-plane daemon (CONTROL_PLANE.md).
// In production it runs as a container supervised by host-agent; in dev it runs
// natively (`go run`) against the local Docker socket and the fake host-agent.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/molmaos/molma/internal/api"
	"github.com/molmaos/molma/internal/audit"
	"github.com/molmaos/molma/internal/auth"
	"github.com/molmaos/molma/internal/caddy"
	"github.com/molmaos/molma/internal/catalog"
	"github.com/molmaos/molma/internal/events"
	"github.com/molmaos/molma/internal/health"
	"github.com/molmaos/molma/internal/hostclient"
	"github.com/molmaos/molma/internal/lifecycle"
	"github.com/molmaos/molma/internal/manifest"
	"github.com/molmaos/molma/internal/notify"
	"github.com/molmaos/molma/internal/protocol"
	"github.com/molmaos/molma/internal/store"
	"github.com/molmaos/molma/internal/systemlive"
)

func main() {
	cfg := loadConfig()
	installLogger(cfg.logLevel, cfg.logFormat)

	if err := os.MkdirAll(cfg.stateDir, 0o755); err != nil {
		fatal("create state dir", "err", err)
	}

	st, err := store.Open(filepath.Join(cfg.stateDir, "molma.db"))
	if err != nil {
		fatal("open store", "err", err)
	}
	defer st.Close()

	cat := catalog.New(cfg.catalogDir)
	host := hostclient.New(cfg.agentSock)
	cd := caddy.New(cfg.caddyAdmin)
	bus := events.NewBus()
	// One Docker driver, shared by the lifecycle manager and the
	// container-restart-loop detector — the detector reuses lifecycle's seam
	// rather than opening a parallel Docker client (issue #35).
	dock := lifecycle.NewCLIDocker()
	life := lifecycle.NewManager(st, cat, host, cd, dock, bus, cfg.stateDir)

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

	// Pull host-agent's system health report (HEALTH.md # Detector catalog,
	// locus B) once at startup and reconcile it into the health registry per
	// category, then keep refreshing on a slow poll. Failure here is non-fatal
	// — the brain runs degraded just like everything else.
	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()
	pullSystemHealth(pollCtx, host, healthMgr, auditor, notifier, bus)
	go systemHealthPollLoop(pollCtx, host, healthMgr, auditor, notifier, bus, cfg.healthPollPeriod)

	// App-runtime health detectors on the health-poll cadence, both driven from
	// ONE goroutine (issue #54: reuse the restart-loop timer, no parallel
	// goroutine):
	//   - container-restart-loop (locus D): sample managed containers' cumulative
	//     RestartCount and raise per-instance when restarts climb past the
	//     threshold within the window.
	//   - app-unresponsive (locus C): for each steady-running instance that
	//     declares a health_probe, GET the probe path through the app's Caddy
	//     route and raise when it fails.
	// Both read Docker through the shared lifecycle seam (no host-agent change, no
	// EVENTS proxy grant). Order matters: the restart-loop check runs first so the
	// probe can defer to a freshly-raised container-restart-loop and not
	// double-banner a crash-looping app (HEALTH.md # app-unresponsive anti-flap).
	rld := newRestartLoopDetector(dock, healthMgr, auditor, notifier, bus)
	probe := newAppProbeDetector(dock, st, life, cfg.caddyProbeURL, healthMgr, auditor, notifier, bus)
	go appRuntimeHealthLoop(pollCtx, cfg.healthPollPeriod, rld.check, probe.check)

	// Bound the notifications table (NOTIFICATIONS.md # Locked decisions, the
	// retention bullet): prune aged / over-cap rows once at boot, then on a slow
	// loop. Non-fatal — an unbounded
	// table degrades gracefully (the bell just carries more history).
	if err := st.PruneNotifications(time.Now()); err != nil {
		slog.Warn("notification prune failed; continuing", "err", err)
	}
	go notificationPruneLoop(pollCtx, st, cfg.notifyPrunePeriod)

	// Locus-C version check (HEALTH.md # Detector catalog): reconcile
	// version-mismatch against host-agent's reported agent_version, once at
	// startup (the first handshake) then on the same loose poll cadence.
	checkAgentVersion(pollCtx, host, healthMgr, auditor, notifier, bus)
	go versionCheckPollLoop(pollCtx, host, healthMgr, auditor, notifier, bus, cfg.healthPollPeriod)

	// Locus-C brain-DB integrity check (HEALTH.md # Detector catalog): PRAGMA
	// integrity_check at boot + every 6h, reconciling brain-db-corrupt. Runs
	// entirely on its own goroutine — the boot run is inside the loop, never
	// before ListenAndServe — because a corrupt DB must raise a banner, never
	// gate startup (HEALTH.md # Stance: a brain that can't boot has no UI; that
	// path is bootstrap-state-mismatch / recovery, not this issue).
	go brainDBIntegrityLoop(pollCtx, st, healthMgr, auditor, notifier, bus, dbIntegrityCheckPeriod)

	// Live system-resources hub (BRAIN_UI_PROTOCOL.md Pattern C, stream 3): a
	// ref-counted 1 Hz poller of host-agent's raw counters, fanned out as
	// derived rates over GET /api/v1/system/live. It polls only while ≥1 SSE
	// subscriber is connected (zero idle cost); pollCtx bounds any active poll
	// to the process lifetime.
	live := systemlive.New(pollCtx, host, time.Second)

	srv := api.NewServer(st, cat, life, bus, authMgr, host, auditor, healthMgr, live)
	httpSrv := &http.Server{Addr: cfg.listen, Handler: srv.Handler()}
	slog.Info("molma-brain listening",
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
	caddyProbeURL     string
	logLevel          string
	logFormat         string
	healthPollPeriod  time.Duration
	notifyPrunePeriod time.Duration
}

func loadConfig() config {
	caddyListen := env("MOLMA_CADDY_LISTEN", ":80")
	return config{
		listen:            env("MOLMA_LISTEN", ":8080"),
		stateDir:          env("MOLMA_STATE_DIR", "./.dev/state"),
		catalogDir:        env("MOLMA_CATALOG_DIR", "./catalog"),
		agentSock:         env("MOLMA_AGENT_SOCK", protocol.SocketPath),
		caddyAdmin:        env("MOLMA_CADDY_ADMIN", "http://localhost:2019"),
		caddyListen:       caddyListen,
		caddyProbeURL:     env("MOLMA_CADDY_PROBE_URL", probeBaseURL(caddyListen)),
		logLevel:          env("MOLMA_LOG_LEVEL", "info"),
		logFormat:         env("MOLMA_LOG_FORMAT", "text"),
		healthPollPeriod:  envDuration("MOLMA_HEALTH_POLL", 60*time.Second),
		notifyPrunePeriod: envDuration("MOLMA_NOTIFY_PRUNE", time.Hour),
	}
}

// probeBaseURL turns the Caddy site-listener address (the same value passed to
// EnsureServer) into a base URL the app-unresponsive probe can dial. ":80" →
// "http://127.0.0.1:80". The probe sets the route Host header and Caddy routes
// by Host, so the dial target is just Caddy's listener. Override with
// MOLMA_CADDY_PROBE_URL when Caddy isn't at localhost (e.g. the brain in a
// container reaching a Caddy container by service name).
func probeBaseURL(caddyListen string) string {
	host, port, err := net.SplitHostPort(caddyListen)
	if err != nil {
		slog.Warn("caddy listen address unparseable; probe base defaults to :80", "caddy_listen", caddyListen, "err", err)
		return "http://127.0.0.1"
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
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

// pullSystemHealth fetches host-agent's system health report and reconciles it
// into the health registry one category at a time. Non-blocking: if host-agent
// isn't reachable yet, we log and return — the brain still starts. The poll loop
// will catch up once host-agent comes online. Transitions are audited per issue
// (see emitHealthTransitions) and fan out to the notification center (see
// emitHealthNotifications).
//
// Each category the report covers is reconciled independently (clearing any
// host-reported issue in it that's now absent); categories not in the report are
// left alone. Sorted iteration keeps the per-issue audit / log order stable.
func pullSystemHealth(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sh, err := host.SystemHealth(c)
	if err != nil {
		slog.Warn("system health: host-agent unreachable; skipping",
			"err", err)
		return
	}
	cats := make([]protocol.HealthCategory, 0, len(sh.Categories))
	for cat := range sh.Categories {
		cats = append(cats, cat)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i] < cats[j] })
	var raised, cleared []health.IssueKey
	for _, cat := range cats {
		r, cl := healthMgr.ApplyFindings(cat, sh.Categories[cat])
		raised = append(raised, r...)
		cleared = append(cleared, cl...)
	}
	if len(raised) > 0 || len(cleared) > 0 {
		slog.Info("system health: reconciled",
			"raised", len(raised), "cleared", len(cleared), "categories", len(cats))
	}
	emitHealthTransitions(ctx, auditor, bus, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// emitHealthTransitions writes one audit record per transitioned health issue,
// targeting {kind: health_issue, id: <id>}, so the Activity view attributes
// each raise/clear to a specific issue rather than a bulk count, and publishes
// the matching SSE event so the dashboard's degraded-mode banner updates live
// (HEALTH.md # Display; issue #12). The event payload is advisory — {id,
// instance_key} — and the UI re-fetches GET /api/v1/health on receipt rather
// than merging it, mirroring the notification pattern. No-op when both slices
// are empty — the steady-state case, since most polls change nothing. bus is
// nil only in tests that assert audit/notify alone; the publish is skipped then.
func emitHealthTransitions(ctx context.Context, auditor *audit.Recorder, bus *events.Bus, raised, cleared []health.IssueKey) {
	for _, k := range raised {
		auditor.Record(ctx, audit.ActionHealthIssueRaised,
			audit.Target{Kind: "health_issue", ID: k.ID}, nil, true)
		if bus != nil {
			bus.Publish(events.HealthIssueRaised, map[string]any{"id": k.ID, "instance_key": k.InstanceKey})
		}
	}
	for _, k := range cleared {
		auditor.Record(ctx, audit.ActionHealthIssueCleared,
			audit.Target{Kind: "health_issue", ID: k.ID}, nil, true)
		if bus != nil {
			bus.Publish(events.HealthIssueCleared, map[string]any{"id": k.ID, "instance_key": k.InstanceKey})
		}
	}
}

// emitHealthNotifications fans transitioned health issues out to the
// notification center (NOTIFICATIONS.md): a notification per allowlisted
// raise, a resolve per allowlisted clear. The allowlist gate lives in
// notify, so this dispatches every transition and lets notify drop the ones
// that don't notify. Raises need the issue's severity/summary, which
// ApplyFindings doesn't return — look it up by key. The ok guard skips
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

// systemHealthPollLoop keeps the health registry in sync with what host-agent
// reports. 60s is the loose-by-design cadence — host-measured findings don't
// change often, and the dashboard's view of "active issues" gets a refresh on
// every dashboard load via the same registry.
func systemHealthPollLoop(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pullSystemHealth(ctx, host, healthMgr, auditor, notifier, bus)
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

// expectedAgentVersion is the host-agent version this brain build expects to
// talk to. It mirrors internal/hostagent.AgentVersion (the version the in-repo
// agent advertises) — the conservative v1 reading of HEALTH.md's "lockstep
// pair": exact string equality against a brain-side constant. The richer
// release-manifest model (RELEASE_MANIFEST.md / UPDATES.md) will replace this
// when it lands; until then, bump this in lockstep with hostagent.AgentVersion.
const expectedAgentVersion = "0.0.1-fake"

// agentStatusReader is the slice of the host client the version check needs — a
// single GET /v1/system/status read. Consumer-side interface (CLAUDE.md) so
// checkAgentVersion is unit-testable with a fake host-agent reporting a chosen
// agent_version; *hostclient.Client satisfies it.
type agentStatusReader interface {
	SystemStatus(ctx context.Context) (protocol.SystemStatus, error)
}

// checkAgentVersion is the locus-C version-mismatch detector (HEALTH.md
// # Detector catalog, locus C). It reads host-agent's reported agent_version
// and reconciles the version-mismatch issue: raise when it differs from the
// version this brain expects, clear when they match. There is no dedicated
// handshake RPC, so each successful status read is the handshake (HEALTH.md:
// "each handshake"). A version string is deterministic and authoritative — it
// cannot flap like a threshold sample — so the check is 1-shot (no debounce):
// it raises/clears on the first definitive reading. A transient unreachable
// host-agent neither raises nor clears, so the issue state survives a blip.
// Transitions are audited per issue and fanned out to notifications, mirroring
// pullStorageHealth. NOTIFICATIONS.md's v1 allowlist routes version-mismatch
// (error) to Admin, but it is not yet wired into internal/notify healthRules
// (like disk-full / brain-db-corrupt — allowlisted in the spec but not yet
// sourced), so the fan-out is a no-op until that entry lands; see the progress
// entry for why wiring it is a separate, deferred step.
func checkAgentVersion(ctx context.Context, host agentStatusReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := host.SystemStatus(c)
	if err != nil {
		slog.Warn("version check: host-agent unreachable; skipping", "err", err)
		return
	}

	var raised, cleared []health.IssueKey
	if status.AgentVersion != expectedAgentVersion {
		details := fmt.Sprintf("The system agent reports version %s, but this dashboard expects %s.",
			status.AgentVersion, expectedAgentVersion)
		if healthMgr.Raise("version-mismatch", "", details) {
			raised = []health.IssueKey{{ID: "version-mismatch"}}
			slog.Warn("version check: agent/brain version mismatch",
				"agent_version", status.AgentVersion, "expected", expectedAgentVersion)
		}
	} else if healthMgr.Clear("version-mismatch", "") {
		cleared = []health.IssueKey{{ID: "version-mismatch"}}
	}
	emitHealthTransitions(ctx, auditor, bus, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// versionCheckPollLoop re-runs the locus-C version check on the same loose
// cadence as the storage poll — each periodic status read is a "handshake"
// (HEALTH.md). version-mismatch only changes when a component is upgraded, so
// the cadence is loose by design.
func versionCheckPollLoop(ctx context.Context, host agentStatusReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			checkAgentVersion(ctx, host, healthMgr, auditor, notifier, bus)
		}
	}
}

// dbIntegrityCheckPeriod is the locus-C brain-db-corrupt cadence (HEALTH.md #
// Detector catalog: "boot + 6h"). A constant, not an env knob: the spec pins
// the value and there's no reason to tune it per-deployment.
const dbIntegrityCheckPeriod = 6 * time.Hour

// integrityChecker is the consumer-side seam for the store's PRAGMA
// integrity_check (CLAUDE.md: interfaces live with the consumer). *store.Store
// satisfies it; tests pass a fake to drive each branch without a real database.
type integrityChecker interface {
	IntegrityCheck() (string, error)
}

// checkBrainDBIntegrity runs one PRAGMA integrity_check and reconciles the
// brain-db-corrupt issue (HEALTH.md # Detector catalog, locus C): a result
// other than "ok" raises it, "ok" clears it. The result is authoritative and
// 1-shot — corruption isn't a noisy threshold sample, so there's no debounce.
// A query error is best-effort: it can't conclude corrupt *or* sound, so it
// logs and leaves the issue state untouched (a transient blip neither raises a
// false banner nor clears a real one). Transitions are audited per issue and
// fan out to the notification center, mirroring pullStorageHealth.
func checkBrainDBIntegrity(ctx context.Context, db integrityChecker, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus) {
	result, err := db.IntegrityCheck()
	if err != nil {
		slog.Warn("integrity check: query failed; leaving brain-db-corrupt unchanged", "err", err)
		return
	}
	var raised, cleared []health.IssueKey
	if result != "ok" {
		details := fmt.Sprintf("SQLite integrity check failed:\n%s", result)
		if healthMgr.Raise("brain-db-corrupt", "", details) {
			raised = []health.IssueKey{{ID: "brain-db-corrupt"}}
			slog.Error("integrity check: brain database is corrupt", "output", result)
		}
	} else if healthMgr.Clear("brain-db-corrupt", "") {
		cleared = []health.IssueKey{{ID: "brain-db-corrupt"}}
		slog.Info("integrity check: brain database recovered; cleared brain-db-corrupt")
	}
	emitHealthTransitions(ctx, auditor, bus, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// brainDBIntegrityLoop runs the boot integrity check and then re-checks on the
// 6h cadence. The boot run lives *inside* this goroutine (not synchronously in
// main before serving) on purpose — see the call site: the check must never
// gate brain startup.
func brainDBIntegrityLoop(ctx context.Context, db integrityChecker, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus, interval time.Duration) {
	checkBrainDBIntegrity(ctx, db, healthMgr, auditor, notifier, bus) // boot run
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			checkBrainDBIntegrity(ctx, db, healthMgr, auditor, notifier, bus)
		}
	}
}

const (
	// restartLoopThreshold / restartLoopWindow define the container-restart-loop
	// trip condition: more than 3 container restarts within a 5-minute sliding
	// window. Conservative defaults — HEALTH.md pins the detector's shape, not
	// the constants (issue #35); tune at first soak if they prove noisy.
	restartLoopThreshold = 3
	restartLoopWindow    = 5 * time.Minute
)

// restartCountReader is the brain's consumer-side slice of the Docker seam:
// just the per-instance restart counts the loop detector needs.
// lifecycle.DockerDriver satisfies it; tests pass a fake (CLAUDE.md:
// consumer-side interfaces).
type restartCountReader interface {
	RestartCounts(ctx context.Context) (map[string]int, error)
}

// restartSample is one (time, cumulative RestartCount) reading for an instance.
type restartSample struct {
	t     time.Time
	count int
}

// restartLoopDetector samples managed containers' cumulative RestartCount and
// reconciles the per-instance container-restart-loop health issue (HEALTH.md #
// Detector catalog, locus D). Not thread-safe: it holds per-instance sample
// history and is driven from a single poll goroutine (run).
type restartLoopDetector struct {
	docker    restartCountReader
	healthMgr *health.Manager
	auditor   *audit.Recorder
	notifier  *notify.Notifier
	bus       *events.Bus
	window    time.Duration
	threshold int
	now       func() time.Time
	history   map[string][]restartSample // instance_id -> samples within the window
}

func newRestartLoopDetector(docker restartCountReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus) *restartLoopDetector {
	return &restartLoopDetector{
		docker:    docker,
		healthMgr: healthMgr,
		auditor:   auditor,
		notifier:  notifier,
		bus:       bus,
		window:    restartLoopWindow,
		threshold: restartLoopThreshold,
		now:       func() time.Time { return time.Now().UTC() },
		history:   map[string][]restartSample{},
	}
}

// appRuntimeHealthLoop drives the app-runtime health detectors (container-
// restart-loop then app-unresponsive) on one ticker — issue #54's "reuse the
// timer, no parallel goroutine." Each check runs once immediately (the
// restart-loop detector establishes its RestartCount baseline on the first
// sample) then re-runs every interval, in the given order. Locus-D is reactive
// in spirit, but the socket-proxy allowlist grants CONTAINERS, not EVENTS
// (CONTROL_PLANE.md), so polling is the no-proxy-change path.
func appRuntimeHealthLoop(ctx context.Context, interval time.Duration, checks ...func(context.Context)) {
	runAll := func() {
		for _, c := range checks {
			c(ctx)
		}
	}
	runAll()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runAll()
		}
	}
}

// check samples every managed container's RestartCount once and reconciles the
// container-restart-loop issue per instance: raise where the within-window
// restart delta exceeds the threshold, clear where it no longer does (the app
// stabilized) or the instance is gone (uninstalled). RestartCount is cumulative
// since container creation, so we threshold the delta over a sliding window, not
// the raw counter — a container that crashed a lot yesterday but is quiet now
// reads as zero recent restarts.
func (d *restartLoopDetector) check(ctx context.Context) {
	counts, err := d.docker.RestartCounts(ctx)
	if err != nil {
		slog.Warn("restart-loop check: docker unreachable; skipping", "err", err)
		return
	}
	now := d.now()
	cutoff := now.Add(-d.window)

	looping := map[string]bool{}
	var raised []health.IssueKey
	for id, count := range counts {
		prev := d.history[id]
		// Container recreation (app update / reinstall) resets RestartCount to 0.
		// A count below the last sample means a fresh container — restart the
		// window from here so a stale high baseline can't mask the new container's
		// restarts (negative delta) or distort the count.
		if n := len(prev); n > 0 && count < prev[n-1].count {
			prev = nil
		}
		var samples []restartSample
		for _, s := range prev {
			if !s.t.Before(cutoff) {
				samples = append(samples, s)
			}
		}
		samples = append(samples, restartSample{t: now, count: count})
		d.history[id] = samples

		// Baseline is the oldest sample still inside the window; the delta against
		// it is how many times this container restarted within the window. The
		// first sample for an instance has delta 0 — no false raise from history.
		if count-samples[0].count > d.threshold {
			looping[id] = true
			details := fmt.Sprintf("This app's container restarted %d times in about %d minutes.",
				count-samples[0].count, int(d.window.Minutes()))
			if d.healthMgr.Raise("container-restart-loop", id, details) {
				raised = append(raised, health.IssueKey{ID: "container-restart-loop", InstanceKey: id})
			}
		}
	}

	// Forget instances Docker no longer reports (stopped / uninstalled) so the
	// history map can't grow without bound.
	for id := range d.history {
		if _, ok := counts[id]; !ok {
			delete(d.history, id)
		}
	}

	// Clear any active restart-loop whose instance isn't looping this poll — it
	// stabilized, or its app was uninstalled (absent from counts entirely).
	var cleared []health.IssueKey
	for _, iss := range d.healthMgr.List() {
		if iss.ID != "container-restart-loop" || looping[iss.InstanceKey] {
			continue
		}
		if d.healthMgr.Clear("container-restart-loop", iss.InstanceKey) {
			cleared = append(cleared, health.IssueKey{ID: "container-restart-loop", InstanceKey: iss.InstanceKey})
		}
	}

	// Stable order (all keys share the ID) so the per-issue audit records and
	// tests see a deterministic sequence.
	sortByInstanceKey(raised)
	sortByInstanceKey(cleared)
	emitHealthTransitions(ctx, d.auditor, d.bus, raised, cleared)
	emitHealthNotifications(d.notifier, d.healthMgr, raised, cleared)
}

func sortByInstanceKey(ks []health.IssueKey) {
	sort.Slice(ks, func(i, j int) bool { return ks[i].InstanceKey < ks[j].InstanceKey })
}

const (
	// appProbeRaiseThreshold is the cross-cutting debounce (HEALTH.md
	// # Cross-cutting detector policy) for app-unresponsive: raise only on the
	// 2nd consecutive failed probe; one good probe clears.
	appProbeRaiseThreshold = 2
	// appProbeTimeout bounds a single probe request. The spec leaves the exact
	// value open (issue #54); 5s matches the brain's other host-call timeouts and
	// sits well under the 60s tick. Tune at first soak.
	appProbeTimeout = 5 * time.Second
)

// managedContainerReader is the brain's consumer-side slice of the Docker seam
// the probe needs: per-instance container service / running / StartedAt.
// lifecycle.DockerDriver satisfies it.
type managedContainerReader interface {
	ManagedContainers(ctx context.Context) ([]lifecycle.ManagedContainer, error)
}

// instanceLister enumerates installed instances (id + slug + mDNS host).
// *store.Store satisfies it.
type instanceLister interface {
	List() ([]store.Instance, error)
}

// instanceManifestLoader loads an instance's persisted manifest (main_service +
// health_probe). *lifecycle.Manager satisfies it.
type instanceManifestLoader interface {
	InstanceManifest(id string) (*manifest.Manifest, error)
}

// appProbeDetector is the locus-C app-unresponsive detector (HEALTH.md #
// Detector catalog). For each steady-running instance that declares a
// health_probe (APP_MANIFEST.md # B), it GETs the probe path *through the app's
// Caddy route* (Host header, never dialing the container — THREAT_MODEL.md # B2)
// and reconciles the per-instance app-unresponsive issue. Not thread-safe: it
// holds per-instance bad-sample counters and is driven from the single
// app-runtime poll goroutine (appRuntimeHealthLoop), after the restart-loop
// detector so a crash-looper surfaces as container-restart-loop, not here.
type appProbeDetector struct {
	docker    managedContainerReader
	instances instanceLister
	manifests instanceManifestLoader
	healthMgr *health.Manager
	auditor   *audit.Recorder
	notifier  *notify.Notifier
	bus       *events.Bus
	client    *http.Client
	baseURL   string // Caddy site-listener base, e.g. http://127.0.0.1:80
	now       func() time.Time
	bad       map[string]int // instance_id -> consecutive failed-probe count
}

func newAppProbeDetector(docker managedContainerReader, instances instanceLister, manifests instanceManifestLoader, baseURL string, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, bus *events.Bus) *appProbeDetector {
	return &appProbeDetector{
		docker:    docker,
		instances: instances,
		manifests: manifests,
		healthMgr: healthMgr,
		auditor:   auditor,
		notifier:  notifier,
		bus:       bus,
		client:    &http.Client{Timeout: appProbeTimeout},
		baseURL:   baseURL,
		now:       func() time.Time { return time.Now().UTC() },
		bad:       map[string]int{},
	}
}

// check probes every eligible instance once and reconciles app-unresponsive.
// Eligible = declares a health_probe, isn't already flagged
// container-restart-loop, has its main_service container running, and is past
// the start-period grace. A failed probe raises on the 2nd consecutive failure
// (or immediately while already active); one good probe — or losing eligibility
// (stopped, crash-looping, uninstalled) — clears.
func (d *appProbeDetector) check(ctx context.Context) {
	instances, err := d.instances.List()
	if err != nil {
		slog.Warn("app-probe check: list instances; skipping", "err", err)
		return
	}
	containers, err := d.docker.ManagedContainers(ctx)
	if err != nil {
		slog.Warn("app-probe check: docker unreachable; skipping", "err", err)
		return
	}
	byInstance := map[string]map[string]lifecycle.ManagedContainer{}
	for _, c := range containers {
		if byInstance[c.InstanceID] == nil {
			byInstance[c.InstanceID] = map[string]lifecycle.ManagedContainer{}
		}
		byInstance[c.InstanceID][c.Service] = c
	}

	now := d.now()
	probed := map[string]bool{}    // instances probed this tick
	unhealthy := map[string]bool{} // instances holding/raising app-unresponsive this tick
	var raised []health.IssueKey

	for _, inst := range instances {
		man, err := d.manifests.InstanceManifest(inst.ID)
		if err != nil || man.HealthProbe == nil {
			continue // unloadable, or opt-out → never probed
		}
		// Defer to container-restart-loop: a crash-looper is its domain, not ours
		// (HEALTH.md # app-unresponsive anti-flap — don't double-banner).
		if _, looping := d.healthMgr.Get("container-restart-loop", inst.ID); looping {
			continue
		}
		main, ok := byInstance[inst.ID][man.MainService]
		if !ok || !main.Running {
			continue // not steady-running
		}
		if now.Sub(main.StartedAt) < man.HealthProbe.StartPeriod {
			continue // start-period grace: warming up, don't count failures yet
		}
		probed[inst.ID] = true
		host := inst.MDNSName
		if host == "" {
			host = inst.Slug + protocol.AppHostSuffix
		}
		if d.probe(ctx, host, man.HealthProbe) {
			d.bad[inst.ID] = 0
			continue // good sample → clear handled in the sweep below
		}
		d.bad[inst.ID]++
		_, active := d.healthMgr.Get("app-unresponsive", inst.ID)
		if active || d.bad[inst.ID] >= appProbeRaiseThreshold {
			unhealthy[inst.ID] = true
			details := "This app is running but didn't respond to its health check."
			if d.healthMgr.Raise("app-unresponsive", inst.ID, details) {
				raised = append(raised, health.IssueKey{ID: "app-unresponsive", InstanceKey: inst.ID})
			}
		}
	}

	// Drop bad-sample counters for instances not probed this tick (stopped, in
	// grace, crash-looping, uninstalled) so a stale count can't carry across a
	// gap — "2 consecutive" means consecutive probes.
	for id := range d.bad {
		if !probed[id] {
			delete(d.bad, id)
		}
	}

	// Clear any active app-unresponsive not held unhealthy this tick: a good
	// sample, or the instance lost eligibility (stopped / now crash-looping /
	// uninstalled) — all "no longer app-unresponsive".
	var cleared []health.IssueKey
	for _, iss := range d.healthMgr.List() {
		if iss.ID != "app-unresponsive" || unhealthy[iss.InstanceKey] {
			continue
		}
		if d.healthMgr.Clear("app-unresponsive", iss.InstanceKey) {
			cleared = append(cleared, health.IssueKey{ID: "app-unresponsive", InstanceKey: iss.InstanceKey})
		}
	}

	sortByInstanceKey(raised)
	sortByInstanceKey(cleared)
	emitHealthTransitions(ctx, d.auditor, d.bus, raised, cleared)
	emitHealthNotifications(d.notifier, d.healthMgr, raised, cleared)
}

// probe GETs the app's probe path through Caddy with Host: <route host> —
// exactly the request a browser makes, never the brain dialing the container
// (THREAT_MODEL.md # B2). Healthy = status in the app's healthy set. A timeout,
// connection failure, or Caddy 502 (dead upstream) is unhealthy.
func (d *appProbeDetector) probe(ctx context.Context, host string, p *manifest.HealthProbe) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+p.Path, nil)
	if err != nil {
		slog.Warn("app-probe: build request", "host", host, "err", err)
		return false
	}
	req.Host = host // route by Host through Caddy
	resp, err := d.client.Do(req)
	if err != nil {
		return false // timeout / connection failure = unhealthy
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the keep-alive can be reused
	return probeHealthy(resp.StatusCode, p.HealthyStatus)
}

// probeHealthy classifies a probe response. Empty allowed set ⇒ the default
// "any status < 500" (the server answered coherently; 401/403/404 still count
// as responding). A non-empty set is exact-match.
func probeHealthy(status int, allowed []int) bool {
	if len(allowed) == 0 {
		return status < 500
	}
	for _, s := range allowed {
		if s == status {
			return true
		}
	}
	return false
}
