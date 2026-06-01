// Command brain is malmo-brain — the control-plane daemon (CONTROL_PLANE.md).
// In production it runs as a container supervised by host-agent; in dev it runs
// natively (`go run`) against the local Docker socket and the fake host-agent.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	"github.com/malmo/malmo/internal/systemlive"
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
	pullSystemHealth(pollCtx, host, healthMgr, auditor, notifier)
	go systemHealthPollLoop(pollCtx, host, healthMgr, auditor, notifier, cfg.healthPollPeriod)

	// Locus-D container-restart-loop detector (HEALTH.md # Detector catalog):
	// sample managed containers' cumulative RestartCount on the health-poll
	// cadence and raise per-instance when restarts climb past the threshold
	// within the window. Brain-only — reads Docker through the shared lifecycle
	// seam (no host-agent change, no EVENTS proxy grant needed).
	rld := newRestartLoopDetector(dock, healthMgr, auditor, notifier)
	go rld.run(pollCtx, cfg.healthPollPeriod)

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
	checkAgentVersion(pollCtx, host, healthMgr, auditor, notifier)
	go versionCheckPollLoop(pollCtx, host, healthMgr, auditor, notifier, cfg.healthPollPeriod)

	// Locus-C brain-DB integrity check (HEALTH.md # Detector catalog): PRAGMA
	// integrity_check at boot + every 6h, reconciling brain-db-corrupt. Runs
	// entirely on its own goroutine — the boot run is inside the loop, never
	// before ListenAndServe — because a corrupt DB must raise a banner, never
	// gate startup (HEALTH.md # Stance: a brain that can't boot has no UI; that
	// path is bootstrap-state-mismatch / recovery, not this issue).
	go brainDBIntegrityLoop(pollCtx, st, healthMgr, auditor, notifier, dbIntegrityCheckPeriod)

	// Live system-resources hub (BRAIN_UI_PROTOCOL.md Pattern C, stream 3): a
	// ref-counted 1 Hz poller of host-agent's raw counters, fanned out as
	// derived rates over GET /api/v1/system/live. It polls only while ≥1 SSE
	// subscriber is connected (zero idle cost); pollCtx bounds any active poll
	// to the process lifetime.
	live := systemlive.New(pollCtx, host, time.Second)

	srv := api.NewServer(st, cat, life, bus, authMgr, host, auditor, healthMgr, live)
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
func pullSystemHealth(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier) {
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
func systemHealthPollLoop(ctx context.Context, host *hostclient.Client, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pullSystemHealth(ctx, host, healthMgr, auditor, notifier)
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
func checkAgentVersion(ctx context.Context, host agentStatusReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier) {
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
	emitHealthTransitions(ctx, auditor, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// versionCheckPollLoop re-runs the locus-C version check on the same loose
// cadence as the storage poll — each periodic status read is a "handshake"
// (HEALTH.md). version-mismatch only changes when a component is upgraded, so
// the cadence is loose by design.
func versionCheckPollLoop(ctx context.Context, host agentStatusReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			checkAgentVersion(ctx, host, healthMgr, auditor, notifier)
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
func checkBrainDBIntegrity(ctx context.Context, db integrityChecker, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier) {
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
	emitHealthTransitions(ctx, auditor, raised, cleared)
	emitHealthNotifications(notifier, healthMgr, raised, cleared)
}

// brainDBIntegrityLoop runs the boot integrity check and then re-checks on the
// 6h cadence. The boot run lives *inside* this goroutine (not synchronously in
// main before serving) on purpose — see the call site: the check must never
// gate brain startup.
func brainDBIntegrityLoop(ctx context.Context, db integrityChecker, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier, interval time.Duration) {
	checkBrainDBIntegrity(ctx, db, healthMgr, auditor, notifier) // boot run
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			checkBrainDBIntegrity(ctx, db, healthMgr, auditor, notifier)
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
	window    time.Duration
	threshold int
	now       func() time.Time
	history   map[string][]restartSample // instance_id -> samples within the window
}

func newRestartLoopDetector(docker restartCountReader, healthMgr *health.Manager, auditor *audit.Recorder, notifier *notify.Notifier) *restartLoopDetector {
	return &restartLoopDetector{
		docker:    docker,
		healthMgr: healthMgr,
		auditor:   auditor,
		notifier:  notifier,
		window:    restartLoopWindow,
		threshold: restartLoopThreshold,
		now:       func() time.Time { return time.Now().UTC() },
		history:   map[string][]restartSample{},
	}
}

// run takes the first sample immediately (establishing baselines) then re-samples
// every interval. Locus-D is reactive in spirit, but the socket-proxy allowlist
// grants CONTAINERS, not EVENTS (CONTROL_PLANE.md), so polling RestartCount is
// the no-proxy-change path the issue prefers.
func (d *restartLoopDetector) run(ctx context.Context, interval time.Duration) {
	d.check(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.check(ctx)
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
	emitHealthTransitions(ctx, d.auditor, raised, cleared)
	emitHealthNotifications(d.notifier, d.healthMgr, raised, cleared)
}

func sortByInstanceKey(ks []health.IssueKey) {
	sort.Slice(ks, func(i, j int) bool { return ks[i].InstanceKey < ks[j].InstanceKey })
}
