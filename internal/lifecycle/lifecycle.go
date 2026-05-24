// Package lifecycle implements the app install/uninstall transaction
// (APP_LIFECYCLE.md). Docker driver is the `docker compose` CLI. The brain
// holds the author's compose verbatim and layers a generated
// compose.override.yml for isolation + appliance behavior.
package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/malmo/malmo/internal/admission"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/manifest"
	"github.com/malmo/malmo/internal/store"

	"gopkg.in/yaml.v3"
)

const ingressNetwork = "malmo-ingress"

var reservedSlugs = map[string]bool{
	"api": true, "admin": true, "dashboard": true, "malmo": true,
	"host": true, "setup": true,
}

type Manager struct {
	store    *store.Store
	catalog  *catalog.Catalog
	host     HostDriver
	caddy    CaddyDriver
	docker   DockerDriver
	admit    Admitter
	bus      *events.Bus
	stateDir string // e.g. ./.dev/state -> instances under <stateDir>/instances/<id>

	// healthWait is overridable in tests; production uses healthWaitTimeout.
	healthWait time.Duration
	// healthPoll is the inter-poll interval; production uses 2s.
	healthPoll time.Duration
}

func NewManager(st *store.Store, cat *catalog.Catalog, host HostDriver, cd CaddyDriver, docker DockerDriver, bus *events.Bus, stateDir string) *Manager {
	return &Manager{
		store: st, catalog: cat, host: host, caddy: cd, docker: docker,
		admit: admission.Check, bus: bus, stateDir: stateDir,
		healthWait: healthWaitTimeout, healthPoll: 2 * time.Second,
	}
}

// SetAdmitter overrides the default compose admitter (admission.Check). Tests
// use admission.CheckStructure to skip `docker compose config -q`.
func (m *Manager) SetAdmitter(a Admitter) { m.admit = a }

// SetHealthTiming overrides the default 120s wait / 2s poll cadence. Tests use
// short timings to keep scenarios fast.
func (m *Manager) SetHealthTiming(wait, poll time.Duration) {
	m.healthWait, m.healthPoll = wait, poll
}

// EnsureIngress creates the shared ingress network and the Caddy server block.
// Called once at brain startup. Best-effort: dev runs without Docker/Caddy
// should still let the API come up.
func (m *Manager) EnsureIngress(ctx context.Context, caddyListen string) {
	if err := m.docker.NetworkCreate(ctx, ingressNetwork, false); err != nil {
		slog.Warn("ensure ingress network", "err", err)
	}
	if err := m.caddy.EnsureServer(ctx, caddyListen); err != nil {
		slog.Warn("ensure caddy server (routes will retry)", "err", err)
	}
}

func (m *Manager) instanceDir(id string) string {
	return filepath.Join(m.stateDir, "instances", id)
}

// Install runs the install transaction for a catalog (Door-1) manifest_id.
func (m *Manager) Install(ctx context.Context, manifestID string, progress func(step string)) (store.Instance, error) {
	man, composeBytes, err := m.catalog.Load(manifestID)
	if err != nil {
		return store.Instance{}, err
	}
	return m.install(ctx, man, composeBytes, progress)
}

// CustomSpec is a user-pasted (Door-2) app: a raw compose plus the bits the
// brain can't infer.
type CustomSpec struct {
	Name        string
	Compose     string
	MainService string // optional if the compose has exactly one service
	MainPort    int
}

// InstallCustom synthesizes a manifest from a pasted compose (APP_MANIFEST.md #
// Custom container — synthetic manifest) and installs it through the same
// transaction as catalog apps.
func (m *Manager) InstallCustom(ctx context.Context, spec CustomSpec, progress func(step string)) (store.Instance, error) {
	man, composeBytes, err := manifest.Synthesize(spec.Name, []byte(spec.Compose), spec.MainService, spec.MainPort)
	if err != nil {
		return store.Instance{}, err
	}
	return m.install(ctx, man, composeBytes, progress)
}

// install is the shared transaction both doors converge on (APP_MANIFEST.md #
// one model, two doors): a manifest + verbatim compose pair, whether loaded
// from the catalog or synthesized from a pasted compose.
func (m *Manager) install(ctx context.Context, man *manifest.Manifest, composeBytes []byte, progress func(step string)) (store.Instance, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	// 1-2. Manifest validated by the caller; admit the compose. Admission runs
	// for BOTH doors and writes no state on rejection (APP_LIFECYCLE.md #
	// admission policy).
	step("admitting_compose")
	if err := m.admit(ctx, composeBytes); err != nil {
		return store.Instance{}, err
	}

	// 3. Allocate slug, write SQLite row (state: installing).
	step("allocating_slug")
	slug, err := m.allocateSlug(man)
	if err != nil {
		return store.Instance{}, err
	}
	id := newInstanceID(man.ID)
	inst := store.Instance{
		ID: id, ManifestID: man.ID, Name: man.Name, Slug: slug,
		Version: man.Version, State: "installing", CreatedAt: time.Now(),
	}
	if err := m.store.Create(inst); err != nil {
		return store.Instance{}, fmt.Errorf("write instance row: %w", err)
	}
	m.emitState(inst, "absent")

	// From here, failures roll back.
	rollback := func(cause error) (store.Instance, error) {
		slog.Warn("install failed, rolling back",
			"instance_id", id, "manifest_id", man.ID, "err", cause)
		_ = m.teardown(context.Background(), inst, true)
		_ = m.store.Delete(id)
		return store.Instance{}, cause
	}

	// 4. Create instance dir tree (manifest + compose verbatim, data/).
	step("writing_instance_dir")
	if err := m.writeInstanceDir(id, man, composeBytes); err != nil {
		return rollback(fmt.Errorf("instance dir: %w", err))
	}

	// 5. Pull images, resolve digests, verify against the catalog promise
	// (Door-1) or TOFU (Door-2), and persist (APP_LIFECYCLE.md # image digest
	// pinning). Runs before the override is written so we generate it once
	// with `image: name@sha256:…` pins rather than write-then-rewrite.
	step("resolving_digests")
	pins, err := resolveImages(ctx, m.docker, man, composeBytes)
	if err != nil {
		return rollback(fmt.Errorf("resolve digests: %w", err))
	}
	if err := m.store.SetInstanceImages(id, toInstanceImages(pins)); err != nil {
		return rollback(fmt.Errorf("persist digests: %w", err))
	}

	// 6. Generate override (with pins) + .env.
	step("generating_override")
	if err := m.writeOverride(id, man, composeBytes, pins); err != nil {
		return rollback(fmt.Errorf("override: %w", err))
	}
	if err := m.writeEnv(id, slug); err != nil {
		return rollback(fmt.Errorf("env: %w", err))
	}

	// 7. Create per-app network.
	step("creating_network")
	appNet := "malmo-app-" + id
	if err := m.docker.NetworkCreate(ctx, appNet, !man.Permissions.Internet); err != nil {
		return rollback(fmt.Errorf("create network: %w", err))
	}

	// 8. Publish mDNS + register the Caddy route pointing at a splash page, so
	// the hostname is reachable immediately (APP_LIFECYCLE.md # register early,
	// with a splash) instead of returning connection-refused for ~120s.
	host := slug + ".malmo.local"
	step("publishing_mdns")
	pub, err := m.host.Publish(ctx, slug)
	if err != nil {
		slog.Warn("mDNS publish failed (continuing)",
			"instance_id", id, "slug", slug, "err", err)
	} else {
		_ = m.store.SetMDNSName(id, pub.Name)
		inst.MDNSName = pub.Name
	}
	step("registering_route")
	if err := m.caddy.AddSplashRoute(ctx, id, host, man.Name, "starting"); err != nil {
		slog.Warn("caddy splash route failed (continuing)",
			"instance_id", id, "host", host, "err", err)
	}

	// 9. docker compose up -d.
	step("compose_up")
	if out, err := m.docker.ComposeUp(ctx, m.instanceDir(id), "malmo-"+id); err != nil {
		return rollback(fmt.Errorf("compose up: %w\n%s", err, out))
	}

	// 10. Wait for main_service healthy. Failures here do NOT roll back: the
	// instance dir is kept for inspection and the route flips to a "failed"
	// splash (APP_LIFECYCLE.md install transaction, steps 10-11 failure).
	step("waiting_healthy")
	if err := m.waitHealthy(ctx, id, man.MainService, m.healthWait); err != nil {
		_ = m.caddy.AddSplashRoute(ctx, id, host, man.Name, "failed")
		_ = m.store.SetState(id, "failed")
		inst.State = "failed"
		m.emitState(inst, "installing")
		slog.Warn("main_service not healthy",
			"instance_id", id, "service", man.MainService, "err", err)
		return store.Instance{}, fmt.Errorf("%s did not become healthy: %w", man.Name, err)
	}

	// 11. Flip the Caddy upstream from splash to the real container.
	step("flipping_route")
	upstream := fmt.Sprintf("malmo-%s-%s:%d", id, man.MainService, man.MainPort)
	if err := m.caddy.AddRoute(ctx, id, host, upstream); err != nil {
		slog.Warn("caddy upstream flip failed (continuing)",
			"instance_id", id, "host", host, "upstream", upstream, "err", err)
	}

	// 12. Mark running.
	if err := m.store.SetState(id, "running"); err != nil {
		return rollback(err)
	}
	inst.State = "running"
	m.emitState(inst, "installing")
	m.bus.Publish(events.AppInstalled, map[string]any{
		"instance_id": id, "name": man.Name, "slug": slug, "url": "http://" + host,
	})
	slog.Info("app installed",
		"instance_id", id, "name", man.Name, "url", "http://"+host, "upstream", upstream)
	return inst, nil
}

const healthWaitTimeout = 120 * time.Second

// waitHealthy blocks until the instance's main_service container is ready or
// the timeout elapses. "Ready" = container Running and, if it declares a Docker
// healthcheck, health status "healthy". Containers without a healthcheck are
// ready as soon as they're Running (APP_LIFECYCLE.md default-120s wait).
func (m *Manager) waitHealthy(ctx context.Context, id, mainService string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		running, health, err := m.docker.Inspect(ctx, id, mainService)
		if err == nil {
			last = health
			if running && (health == "none" || health == "healthy") {
				return nil
			}
			if health == "unhealthy" {
				return fmt.Errorf("container reported unhealthy")
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s (last: %s)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.healthPoll):
		}
	}
}

// Uninstall tears down an instance (APP_LIFECYCLE.md: compose down -v, remove
// route + mDNS, rm instance dir). Skeleton always deletes data.
func (m *Manager) Uninstall(ctx context.Context, id string) error {
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	_ = m.store.SetState(id, "uninstalling")
	m.emitState(inst, inst.State)
	if err := m.teardown(ctx, inst, true); err != nil {
		return err
	}
	if err := m.store.Delete(id); err != nil {
		return err
	}
	m.bus.Publish(events.AppUninstalled, map[string]any{"instance_id": id})
	slog.Info("app uninstalled", "instance_id", id, "name", inst.Name)
	return nil
}

// teardown reverses the resources install creates. Each step is best-effort so
// a partial install can always be cleaned up.
func (m *Manager) teardown(ctx context.Context, inst store.Instance, removeDir bool) error {
	if _, err := os.Stat(m.composeFile(inst.ID)); err == nil {
		if out, err := m.docker.ComposeDown(ctx, m.instanceDir(inst.ID), "malmo-"+inst.ID); err != nil {
			slog.Warn("teardown: compose down",
				"instance_id", inst.ID, "err", err, "output", out)
		}
	}
	if err := m.caddy.RemoveRoute(ctx, inst.ID); err != nil {
		slog.Warn("teardown: caddy remove route", "instance_id", inst.ID, "err", err)
	}
	if err := m.host.Unpublish(ctx, inst.Slug); err != nil {
		slog.Warn("teardown: mDNS unpublish", "slug", inst.Slug, "err", err)
	}
	_ = m.docker.NetworkRemove(ctx, "malmo-app-"+inst.ID)
	if removeDir {
		_ = os.RemoveAll(m.instanceDir(inst.ID))
	}
	return nil
}

// Reconcile is the brain-startup pass (APP_LIFECYCLE.md # reconciliation is
// imperative, with a startup pass). It walks SQLite (desired state), compares
// against Docker (actual state), and converges:
//   - running but no containers  -> compose up -d
//   - stopped but containers up  -> compose stop
//   - orphan containers (labeled, no SQLite row) -> tear down
//
// For every running instance it also re-asserts the Caddy route + mDNS, which
// is what fixes "brain restart drops routes" (EnsureServer resets the route
// list at startup, then this re-adds them). Idempotent: safe to run repeatedly.
//
// Skeleton scope: handles running/stopped. Interrupted installing/uninstalling
// states (crash mid-transaction) are left for the install-transaction rollback
// and a future dangerous-op-aware pass.
func (m *Manager) Reconcile(ctx context.Context) error {
	desired, err := m.store.List()
	if err != nil {
		return fmt.Errorf("reconcile: list desired: %w", err)
	}
	actual, err := m.docker.PSManaged(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list actual: %w", err)
	}

	seen := map[string]bool{}
	var avahiTotal, avahiOK, avahiFail int
	for _, inst := range desired {
		seen[inst.ID] = true
		switch inst.State {
		case "running":
			if !actual[inst.ID] {
				slog.Info("reconcile: starting drifted instance",
					"instance_id", inst.ID, "reason", "no containers")
				if out, err := m.docker.ComposeUp(ctx, m.instanceDir(inst.ID), "malmo-"+inst.ID); err != nil {
					slog.Warn("reconcile: compose up",
						"instance_id", inst.ID, "err", err, "output", out)
					continue
				}
			}
			// Re-assert Caddy + mDNS. Track Avahi replay outcome for the
			// startup summary log (covers both "brain restart while host-agent
			// was running" and "both restart together" cases).
			avahiTotal++
			if ok := m.reassertRouting(ctx, inst); ok {
				avahiOK++
			} else {
				avahiFail++
			}
		case "stopped":
			if actual[inst.ID] {
				slog.Info("reconcile: stopping drifted instance",
					"instance_id", inst.ID, "reason", "containers up but state=stopped")
				if out, err := m.docker.ComposeStop(ctx, m.instanceDir(inst.ID), "malmo-"+inst.ID); err != nil {
					slog.Warn("reconcile: compose stop",
						"instance_id", inst.ID, "err", err, "output", out)
				}
			}
		}
	}
	if avahiTotal > 0 {
		slog.Info("avahi replay", "total", avahiTotal, "ok", avahiOK, "failed", avahiFail)
	}

	for id := range actual {
		if !seen[id] {
			slog.Info("reconcile: tearing down orphan",
				"instance_id", id, "reason", "no SQLite row")
			m.teardownOrphan(ctx, id)
		}
	}
	return nil
}

// reassertRouting re-publishes mDNS and re-registers the Caddy route for a
// running instance. Returns true if the Avahi publish succeeded, false
// otherwise. Best-effort: failures are logged and do not block startup.
func (m *Manager) reassertRouting(ctx context.Context, inst store.Instance) bool {
	man, err := m.loadInstanceManifest(inst.ID)
	if err != nil {
		slog.Warn("reconcile: load manifest, skipping routing",
			"instance_id", inst.ID, "err", err)
		return false
	}
	avahiOK := true
	if _, err := m.host.Publish(ctx, inst.Slug); err != nil {
		slog.Warn("reconcile: mDNS publish",
			"instance_id", inst.ID, "slug", inst.Slug, "err", err)
		avahiOK = false
	}
	upstream := fmt.Sprintf("malmo-%s-%s:%d", inst.ID, man.MainService, man.MainPort)
	host := inst.Slug + ".malmo.local"
	if err := m.caddy.AddRoute(ctx, inst.ID, host, upstream); err != nil {
		slog.Warn("reconcile: caddy route",
			"instance_id", inst.ID, "host", host, "upstream", upstream, "err", err)
	}
	return avahiOK
}

func (m *Manager) teardownOrphan(ctx context.Context, id string) {
	// Prefer compose if the instance dir survived; otherwise remove containers
	// by label and drop the per-app network directly.
	if _, err := os.Stat(m.composeFile(id)); err == nil {
		if out, err := m.docker.ComposeDown(ctx, m.instanceDir(id), "malmo-"+id); err != nil {
			slog.Warn("reconcile: compose down orphan",
				"instance_id", id, "err", err, "output", out)
		}
	} else {
		if err := m.docker.RemoveContainersByInstance(ctx, id); err != nil {
			slog.Warn("reconcile: remove orphan containers",
				"instance_id", id, "err", err)
		}
	}
	_ = m.caddy.RemoveRoute(ctx, id)
	_ = m.docker.NetworkRemove(ctx, "malmo-app-"+id)
}

func (m *Manager) loadInstanceManifest(id string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(filepath.Join(m.instanceDir(id), "manifest.yml"))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(b)
}

func (m *Manager) allocateSlug(man *manifest.Manifest) (string, error) {
	cands := man.PreferredSlugs
	if len(cands) == 0 {
		cands = []string{man.ID}
	}
	for _, base := range cands {
		for _, slug := range []string{base, base + "-2", base + "-3"} {
			if reservedSlugs[slug] {
				continue
			}
			taken, err := m.store.SlugTaken(slug)
			if err != nil {
				return "", err
			}
			if !taken {
				return slug, nil
			}
		}
	}
	return "", fmt.Errorf("no free slug among %v", cands)
}

func (m *Manager) emitState(inst store.Instance, prev string) {
	m.bus.Publish(events.AppStateChanged, map[string]any{
		"instance_id": inst.ID, "state": inst.State, "prev": prev,
	})
}

// --- on-disk + compose helpers -------------------------------------------

func (m *Manager) composeFile(id string) string { return filepath.Join(m.instanceDir(id), "compose.yml") }

func (m *Manager) writeInstanceDir(id string, man *manifest.Manifest, composeBytes []byte) error {
	dir := m.instanceDir(id)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		return err
	}
	manBytes, _ := yaml.Marshal(man)
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), manBytes, 0o644); err != nil {
		return err
	}
	return os.WriteFile(m.composeFile(id), composeBytes, 0o644)
}

// writeOverride generates compose.override.yml per APP_LIFECYCLE.md "override
// file contents": cap_drop ALL, no-new-privileges, forced restart, network
// attachment, plus the `image: name@sha256:…` pin per service (digest pinning
// — APP_LIFECYCLE.md). main_service additionally joins the ingress network
// with a per-instance alias so Caddy can reach exactly this instance.
func (m *Manager) writeOverride(id string, man *manifest.Manifest, composeBytes []byte, pins []servicePin) error {
	svcNames, err := composeServices(composeBytes)
	if err != nil {
		return err
	}
	pinBySvc := make(map[string]string, len(pins))
	for _, p := range pins {
		pinBySvc[p.Service] = p.PinnedRef()
	}
	appNet := "malmo-app-" + id
	services := map[string]any{}
	for _, svc := range svcNames {
		nets := map[string]any{appNet: nil}
		if svc == man.MainService {
			nets[ingressNetwork] = map[string]any{
				"aliases": []string{fmt.Sprintf("malmo-%s-%s", id, man.MainService)},
			}
		}
		entry := map[string]any{
			"cap_drop":     []string{"ALL"},
			"security_opt": []string{"no-new-privileges:true"},
			"restart":      "unless-stopped",
			"networks":     nets,
			// Labels let the reconciler find managed containers and map them
			// back to instances (APP_LIFECYCLE.md # an app instance is a
			// compose project).
			"labels": map[string]string{
				"malmo.managed":     "true",
				"malmo.instance_id": id,
				"malmo.manifest_id": man.ID,
			},
		}
		if ref, ok := pinBySvc[svc]; ok {
			entry["image"] = ref
		}
		services[svc] = entry
	}
	override := map[string]any{
		"services": services,
		"networks": map[string]any{
			appNet:         map[string]any{"external": true},
			ingressNetwork: map[string]any{"external": true},
		},
	}
	out, err := yaml.Marshal(override)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.instanceDir(id), "compose.override.yml"), out, 0o644)
}

func (m *Manager) writeEnv(id, slug string) error {
	dataDir, _ := filepath.Abs(filepath.Join(m.instanceDir(id), "data"))
	env := strings.Join([]string{
		"MALMO_INSTANCE_ID=" + id,
		"MALMO_APP_URL=http://" + slug + ".malmo.local",
		"MALMO_DATA_DIR=" + dataDir,
		"",
	}, "\n")
	return os.WriteFile(filepath.Join(m.instanceDir(id), ".env"), []byte(env), 0o644)
}

func composeServices(composeBytes []byte) ([]string, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose services: %w", err)
	}
	if len(doc.Services) == 0 {
		return nil, fmt.Errorf("compose has no services")
	}
	names := make([]string, 0, len(doc.Services))
	for n := range doc.Services {
		names = append(names, n)
	}
	return names, nil
}

func newInstanceID(manifestID string) string {
	return fmt.Sprintf("%s-%s", manifestID, time.Now().Format("20060102t150405"))
}
