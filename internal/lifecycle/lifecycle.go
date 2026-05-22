// Package lifecycle implements the app install/uninstall transaction
// (APP_LIFECYCLE.md). Docker driver is the `docker compose` CLI. The brain
// holds the author's compose verbatim and layers a generated
// compose.override.yml for isolation + appliance behavior.
package lifecycle

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/malmo/malmo/internal/caddy"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/hostclient"
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
	host     *hostclient.Client
	caddy    *caddy.Client
	bus      *events.Bus
	stateDir string // e.g. ./.dev/state -> instances under <stateDir>/instances/<id>
}

func NewManager(st *store.Store, cat *catalog.Catalog, host *hostclient.Client, cd *caddy.Client, bus *events.Bus, stateDir string) *Manager {
	return &Manager{store: st, catalog: cat, host: host, caddy: cd, bus: bus, stateDir: stateDir}
}

// EnsureIngress creates the shared ingress network and the Caddy server block.
// Called once at brain startup. Best-effort: dev runs without Docker/Caddy
// should still let the API come up.
func (m *Manager) EnsureIngress(ctx context.Context, caddyListen string) {
	if err := createNetwork(ctx, ingressNetwork, false); err != nil {
		log.Printf("ensure ingress network: %v", err)
	}
	if err := m.caddy.EnsureServer(ctx, caddyListen); err != nil {
		log.Printf("ensure caddy server (continuing; routes will retry): %v", err)
	}
}

func (m *Manager) instanceDir(id string) string {
	return filepath.Join(m.stateDir, "instances", id)
}

// Install runs the install transaction for a catalog manifest_id.
func (m *Manager) Install(ctx context.Context, manifestID string, progress func(step string)) (store.Instance, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	// 1-2. Parse + validate manifest (compose admission is a follow-up).
	step("loading_manifest")
	man, composeBytes, err := m.catalog.Load(manifestID)
	if err != nil {
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
		log.Printf("install %s failed: %v — rolling back", id, cause)
		_ = m.teardown(context.Background(), inst, true)
		_ = m.store.Delete(id)
		return store.Instance{}, cause
	}

	// 4. Create instance dir tree (manifest + compose verbatim, data/).
	step("writing_instance_dir")
	if err := m.writeInstanceDir(id, man, composeBytes); err != nil {
		return rollback(fmt.Errorf("instance dir: %w", err))
	}

	// 5. Generate override + .env.
	step("generating_override")
	if err := m.writeOverride(id, man, composeBytes); err != nil {
		return rollback(fmt.Errorf("override: %w", err))
	}
	if err := m.writeEnv(id, slug); err != nil {
		return rollback(fmt.Errorf("env: %w", err))
	}

	// 7. Create per-app network.
	step("creating_network")
	appNet := "malmo-app-" + id
	if err := createNetwork(ctx, appNet, !man.Permissions.Internet); err != nil {
		return rollback(fmt.Errorf("create network: %w", err))
	}

	// 8. Publish mDNS + register the Caddy route pointing at a splash page, so
	// the hostname is reachable immediately (APP_LIFECYCLE.md # register early,
	// with a splash) instead of returning connection-refused for ~120s.
	host := slug + ".malmo.local"
	step("publishing_mdns")
	pub, err := m.host.Publish(ctx, slug)
	if err != nil {
		log.Printf("mDNS publish failed (continuing): %v", err)
	} else {
		_ = m.store.SetMDNSName(id, pub.Name)
		inst.MDNSName = pub.Name
	}
	step("registering_route")
	if err := m.caddy.AddSplashRoute(ctx, id, host, man.Name, "starting"); err != nil {
		log.Printf("caddy splash route failed (continuing): %v", err)
	}

	// 9. docker compose up -d.
	step("compose_up")
	if out, err := m.compose(ctx, id, "up", "-d"); err != nil {
		return rollback(fmt.Errorf("compose up: %w\n%s", err, out))
	}

	// 10. Wait for main_service healthy. Failures here do NOT roll back: the
	// instance dir is kept for inspection and the route flips to a "failed"
	// splash (APP_LIFECYCLE.md install transaction, steps 10-11 failure).
	step("waiting_healthy")
	if err := m.waitHealthy(ctx, id, man.MainService, healthWaitTimeout); err != nil {
		_ = m.caddy.AddSplashRoute(ctx, id, host, man.Name, "failed")
		_ = m.store.SetState(id, "failed")
		inst.State = "failed"
		m.emitState(inst, "installing")
		log.Printf("install %s: main_service not healthy: %v", id, err)
		return store.Instance{}, fmt.Errorf("%s did not become healthy: %w", man.Name, err)
	}

	// 11. Flip the Caddy upstream from splash to the real container.
	step("flipping_route")
	upstream := fmt.Sprintf("malmo-%s-%s:%d", id, man.MainService, man.MainPort)
	if err := m.caddy.AddRoute(ctx, id, host, upstream); err != nil {
		log.Printf("caddy upstream flip failed (continuing): %v", err)
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
	log.Printf("installed %s (%s) at http://%s -> %s", man.Name, id, host, upstream)
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
		running, health, err := inspectMainService(ctx, id, mainService)
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
		case <-time.After(2 * time.Second):
		}
	}
}

// inspectMainService returns (running, healthStatus) for the main_service
// container. healthStatus is "none" when the image declares no healthcheck.
func inspectMainService(ctx context.Context, id, mainService string) (bool, string, error) {
	cid, err := exec.CommandContext(ctx, "docker", "ps", "-q",
		"--filter", "label=malmo.instance_id="+id,
		"--filter", "label=com.docker.compose.service="+mainService).Output()
	container := strings.TrimSpace(string(cid))
	if err != nil || container == "" {
		return false, "", fmt.Errorf("main_service container not found")
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}`,
		container).Output()
	if err != nil {
		return false, "", err
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return false, "", fmt.Errorf("unexpected inspect output: %q", out)
	}
	return parts[0] == "true", parts[1], nil
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
	log.Printf("uninstalled %s (%s)", inst.Name, id)
	return nil
}

// teardown reverses the resources install creates. Each step is best-effort so
// a partial install can always be cleaned up.
func (m *Manager) teardown(ctx context.Context, inst store.Instance, removeDir bool) error {
	if _, err := os.Stat(m.composeFile(inst.ID)); err == nil {
		if out, err := m.compose(ctx, inst.ID, "down", "-v"); err != nil {
			log.Printf("compose down %s: %v\n%s", inst.ID, err, out)
		}
	}
	if err := m.caddy.RemoveRoute(ctx, inst.ID); err != nil {
		log.Printf("caddy remove route %s: %v", inst.ID, err)
	}
	if err := m.host.Unpublish(ctx, inst.Slug); err != nil {
		log.Printf("mDNS unpublish %s: %v", inst.Slug, err)
	}
	_ = removeNetwork(ctx, "malmo-app-"+inst.ID)
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
	actual, err := dockerManagedInstances(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list actual: %w", err)
	}

	seen := map[string]bool{}
	for _, inst := range desired {
		seen[inst.ID] = true
		switch inst.State {
		case "running":
			if !actual[inst.ID] {
				log.Printf("reconcile: %s should be running but has no containers — starting", inst.ID)
				if out, err := m.compose(ctx, inst.ID, "up", "-d"); err != nil {
					log.Printf("reconcile: compose up %s: %v\n%s", inst.ID, err, out)
					continue
				}
			}
			m.reassertRouting(ctx, inst)
		case "stopped":
			if actual[inst.ID] {
				log.Printf("reconcile: %s should be stopped but has containers — stopping", inst.ID)
				if out, err := m.compose(ctx, inst.ID, "stop"); err != nil {
					log.Printf("reconcile: compose stop %s: %v\n%s", inst.ID, err, out)
				}
			}
		}
	}

	for id := range actual {
		if !seen[id] {
			log.Printf("reconcile: orphan containers for %s (no SQLite row) — tearing down", id)
			m.teardownOrphan(ctx, id)
		}
	}
	return nil
}

// reassertRouting re-publishes mDNS and re-registers the Caddy route for a
// running instance. Best-effort: a missing manifest or unreachable Caddy logs
// and continues, matching install's degrade-don't-block posture.
func (m *Manager) reassertRouting(ctx context.Context, inst store.Instance) {
	man, err := m.loadInstanceManifest(inst.ID)
	if err != nil {
		log.Printf("reconcile: load manifest for %s: %v — skipping routing", inst.ID, err)
		return
	}
	if _, err := m.host.Publish(ctx, inst.Slug); err != nil {
		log.Printf("reconcile: mDNS publish %s: %v", inst.Slug, err)
	}
	upstream := fmt.Sprintf("malmo-%s-%s:%d", inst.ID, man.MainService, man.MainPort)
	host := inst.Slug + ".malmo.local"
	if err := m.caddy.AddRoute(ctx, inst.ID, host, upstream); err != nil {
		log.Printf("reconcile: caddy route %s: %v", inst.ID, err)
	}
}

func (m *Manager) teardownOrphan(ctx context.Context, id string) {
	// Prefer compose if the instance dir survived; otherwise remove containers
	// by label and drop the per-app network directly.
	if _, err := os.Stat(m.composeFile(id)); err == nil {
		if out, err := m.compose(ctx, id, "down", "-v"); err != nil {
			log.Printf("reconcile: compose down orphan %s: %v\n%s", id, err, out)
		}
	} else {
		ids, _ := exec.CommandContext(ctx, "docker", "ps", "-aq",
			"--filter", "label=malmo.instance_id="+id).Output()
		for _, cid := range strings.Fields(string(ids)) {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", cid).Run()
		}
	}
	_ = m.caddy.RemoveRoute(ctx, id)
	_ = removeNetwork(ctx, "malmo-app-"+id)
}

func (m *Manager) loadInstanceManifest(id string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(filepath.Join(m.instanceDir(id), "manifest.yml"))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(b)
}

// dockerManagedInstances returns instance_id -> hasRunningContainer for every
// malmo-managed container (running or stopped).
func dockerManagedInstances(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=malmo.managed=true",
		"--format", `{{.Label "malmo.instance_id"}} {{.State}}`).Output()
	if err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		id := parts[0]
		running := len(parts) > 1 && parts[1] == "running"
		res[id] = res[id] || running
	}
	return res, nil
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
// attachment. main_service additionally joins the ingress network with a
// per-instance alias so Caddy can reach exactly this instance.
func (m *Manager) writeOverride(id string, man *manifest.Manifest, composeBytes []byte) error {
	svcNames, err := composeServices(composeBytes)
	if err != nil {
		return err
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
		services[svc] = map[string]any{
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

func (m *Manager) compose(ctx context.Context, id string, args ...string) (string, error) {
	dir := m.instanceDir(id)
	base := []string{
		"compose",
		"-f", "compose.yml",
		"-f", "compose.override.yml",
		"--env-file", ".env",
		"-p", "malmo-" + id,
	}
	cmd := exec.CommandContext(ctx, "docker", append(base, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- docker network helpers (Docker API in spec; CLI here for skeleton) ---

func createNetwork(ctx context.Context, name string, internal bool) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	args = append(args, name)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil && strings.Contains(string(out), "already exists") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func removeNetwork(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "docker", "network", "rm", name).Run()
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
