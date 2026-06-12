// Package lifecycle implements the app install/uninstall transaction
// (APP_LIFECYCLE.md). Docker driver is the `docker compose` CLI. The brain
// holds the author's compose verbatim and layers a generated
// compose.override.yml for isolation + appliance behavior.
package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/molmaos/molma/internal/admission"
	"github.com/molmaos/molma/internal/catalog"
	"github.com/molmaos/molma/internal/events"
	"github.com/molmaos/molma/internal/hostclient"
	"github.com/molmaos/molma/internal/manifest"
	"github.com/molmaos/molma/internal/protocol"
	"github.com/molmaos/molma/internal/store"

	"gopkg.in/yaml.v3"
)

const ingressNetwork = "molma-ingress"

// Folder-source election values (the installer's per-folder choice). Mirrors
// the api package's source constants; kept local so lifecycle doesn't import
// the API layer.
const (
	sourcePersonal = "personal"
	sourceShared   = "shared"
)

// folderDir maps a taxonomy folder name to its capitalized on-disk directory
// (STORAGE.md # user content). Personal source binds <home>/<dir>, shared binds
// /srv/molma/shared/<dir>.
var folderDir = map[string]string{
	"photos": "Photos", "documents": "Documents", "movies": "Movies",
	"music": "Music", "notes": "Notes", "downloads": "Downloads",
}

// FolderMount is one resolved per-folder election the override generator binds.
// The manifest declares the folder + mode; the installer elects Source
// (personal|shared) and, for a pick-subfolder folder, a Subfolder. Built and
// validated by the API layer (internal/api resolveElections) — lifecycle treats
// it as authoritative and only cross-references the manifest for the mount mode.
type FolderMount struct {
	Folder    string // taxonomy name (lowercase): photos|documents|movies|music|notes|downloads
	Source    string // sourcePersonal | sourceShared
	Subfolder string // optional relative subpath under the folder (pick-subfolder)

	// Target is the in-container destination for a Door-2 grant — the path the
	// admin typed because a pasted compose has no author to map MOLMA_FOLDER_<NAME>
	// (DASHBOARD.md # Folder grants carry an explicit destination path). Empty for
	// a store (Door-1) mount, which keeps the fixed `/molma/<folder>` convention.
	Target string
}

// isolation is the resolved per-instance identity + folder binds writeOverride
// and writeEnv stamp onto every service. Every instance carries one: folder apps
// run as the owner's UID/GID with mounts populated; folderless apps run as the
// brain's own effective identity with mounts empty (folder-bind paths are no-ops).
type isolation struct {
	uid, gid  int    // container runtime identity (compose user:)
	sharedGID int    // molma-shared GID for group_add on shared-source mounts
	home      string // owner home dir (personal scope); "" for household
	mounts    []FolderMount
}

// hostSource resolves the host path bound for one mount: the owner's
// <home>/<Folder>/ for a personal source, /srv/molma/shared/<Folder>/ for a
// shared source, narrowed by Subfolder when present.
func (it *isolation) hostSource(mt FolderMount) string {
	base := filepath.Join("/srv/molma/shared", folderDir[mt.Folder])
	if mt.Source == sourcePersonal {
		base = filepath.Join(it.home, folderDir[mt.Folder])
	}
	if mt.Subfolder != "" {
		base = filepath.Join(base, mt.Subfolder)
	}
	return base
}

// containerDest is where a mount lands inside the container: the admin-typed
// Door-2 `target` when set, else the fixed `/molma/<folder>` a store app's
// author maps via MOLMA_FOLDER_<NAME> (DASHBOARD.md # Folder grants carry an
// explicit destination path).
func containerDest(mt FolderMount) string {
	if mt.Target != "" {
		return mt.Target
	}
	return "/molma/" + mt.Folder
}

var reservedSlugs = map[string]bool{
	"api": true, "admin": true, "dashboard": true, "molma": true,
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

	// instLocks serializes lifecycle ops on a single existing instance
	// (APP_LIFECYCLE.md # concurrency — one op at a time per instance). Stop,
	// Start, and Uninstall all take the per-id lock so a stop can't race an
	// uninstall (or each other). Install allocates a fresh id, so it has nothing
	// to contend with and skips the lock.
	locksMu   sync.Mutex
	instLocks map[string]*sync.Mutex
}

func NewManager(st *store.Store, cat *catalog.Catalog, host HostDriver, cd CaddyDriver, docker DockerDriver, bus *events.Bus, stateDir string) *Manager {
	return &Manager{
		store: st, catalog: cat, host: host, caddy: cd, docker: docker,
		admit: admission.Check, bus: bus, stateDir: stateDir,
		healthWait: healthWaitTimeout, healthPoll: 2 * time.Second,
		instLocks: map[string]*sync.Mutex{},
	}
}

// lockInstance acquires the per-instance lock (creating it on first use) and
// returns the unlock func. Callers `defer unlock()`. See instLocks.
func (m *Manager) lockInstance(id string) func() {
	m.locksMu.Lock()
	lk, ok := m.instLocks[id]
	if !ok {
		lk = &sync.Mutex{}
		m.instLocks[id] = lk
	}
	m.locksMu.Unlock()
	lk.Lock()
	return lk.Unlock
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
// Owner identifies the user an instance is installed for. Username is the
// trailing label in a personal instance's `<slug>--<user>` slug; UserID is the
// stable owner reference persisted on the row.
type Owner struct {
	UserID   string
	Username string
}

func (m *Manager) Install(ctx context.Context, manifestID string, owner Owner, scope string, mounts []FolderMount, mailProviderID string, progress func(step string)) (store.Instance, error) {
	man, composeBytes, err := m.catalog.Load(manifestID)
	if err != nil {
		return store.Instance{}, err
	}
	return m.install(ctx, man, composeBytes, owner, scope, mounts, mailProviderID, progress)
}

// CustomSpec is a user-pasted (Door-2) app: a raw compose plus the bits the
// brain can't infer — the elected permission set (DASHBOARD.md # Permissions),
// which the API layer resolves from the form (or the Edit-as-YAML overlay)
// before it reaches here.
type CustomSpec struct {
	Name        string
	Compose     string
	MainService string // optional if the compose has exactly one service
	MainPort    int
	Permissions manifest.Permissions
}

// InstallCustom synthesizes a manifest from a pasted compose (APP_MANIFEST.md #
// Custom container — synthetic manifest) and installs it through the same
// transaction as catalog apps. The synthetic manifest carries the admin-elected
// permissions verbatim, so the folder grants it declares drive the same
// isolation/bind machinery a store app uses.
func (m *Manager) InstallCustom(ctx context.Context, spec CustomSpec, owner Owner, scope string, progress func(step string)) (store.Instance, error) {
	man, composeBytes, err := manifest.Synthesize(spec.Name, []byte(spec.Compose), spec.MainService, spec.MainPort, spec.Permissions)
	if err != nil {
		return store.Instance{}, err
	}
	return m.install(ctx, man, composeBytes, owner, scope, customMounts(man.Permissions.Folders, scope), "", progress)
}

// customMounts resolves a Door-2 manifest's folder grants into FolderMounts.
// Unlike the catalog path, where the installer elects each folder's source per
// folder (internal/api resolveElections), a Door-2 paste has no per-folder
// election UI: the source follows the install scope — a personal install reads
// the owner's `~/<Folder>/`, a household install the shared tree. The grant's
// admin-typed `target` is the in-container destination the bind lands at.
func customMounts(folders []manifest.Folder, scope string) []FolderMount {
	src := sourceShared
	if scope == store.ScopePersonal {
		src = sourcePersonal
	}
	mounts := make([]FolderMount, len(folders))
	for i, f := range folders {
		mounts[i] = FolderMount{Folder: f.Folder, Source: src, Target: f.Target}
	}
	return mounts
}

// install is the shared transaction both doors converge on (APP_MANIFEST.md #
// one model, two doors): a manifest + verbatim compose pair, whether loaded
// from the catalog or synthesized from a pasted compose.
func (m *Manager) install(ctx context.Context, man *manifest.Manifest, composeBytes []byte, owner Owner, scope string, mounts []FolderMount, mailProviderID string, progress func(step string)) (store.Instance, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	// A mail-provider election is only meaningful for an app that declares
	// mail support — the API validates this against the install plan, so this
	// is the transaction owner's backstop, checked before any state is written.
	if mailProviderID != "" && man.Mail == nil {
		return store.Instance{}, fmt.Errorf("app %q does not declare mail support", man.ID)
	}

	// 1-2. Manifest validated by the caller; admit the compose + the manifest's
	// isolation declarations. Admission runs for BOTH doors and writes no state
	// on rejection (APP_LIFECYCLE.md # admission policy). CheckManifest is pure,
	// so it doesn't go through the Admitter seam (which exists only to skip the
	// docker-CLI syntax pass in tests).
	step("admitting_compose")
	if err := m.admit(ctx, composeBytes); err != nil {
		return store.Instance{}, err
	}
	if err := admission.CheckManifest(man); err != nil {
		return store.Instance{}, err
	}

	// 3. Allocate slug, write SQLite row (state: installing). Household instances
	// take the bare slug; personal instances take `<slug>--<user>`
	// (DASHBOARD.md # instance naming).
	step("allocating_slug")
	slug, err := m.allocateSlug(man, scope, owner.Username)
	if err != nil {
		return store.Instance{}, err
	}
	id := newInstanceID(man.ID)
	inst := store.Instance{
		ID: id, ManifestID: man.ID, Name: man.Name, Slug: slug,
		Version: man.Version, State: "installing",
		OwnerUserID: owner.UserID, Scope: scope, CreatedAt: time.Now(),
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
		// Drop any managed-service db/role provisioned before the failure, while
		// the grant rows still exist (Delete cascades them away). Empty for a
		// rollback that fires before step 5c.
		if grants, err := m.store.GetServiceGrants(id); err == nil && len(grants) > 0 {
			m.dropServiceGrants(context.Background(), id, grants)
		}
		// Return any allocated app-service identity to the host's band, read
		// back from the row before Delete erases it. Zero for a rollback that
		// fires before step 6 (or for any non-service_user instance).
		if row, err := m.store.Get(id); err == nil && row.ServiceUID != 0 {
			m.releaseServiceIdentity(context.Background(), id, row.ServiceUID)
		}
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

	// 5b. Generate the manifest's declared per-app secrets once and persist them
	// (SERVICE_PROVISIONING.md # Env-var injection). Generated before the .env is
	// written so writeEnv can re-emit them from the store; persisting (not just
	// writing .env) is what keeps a token-signing secret stable if the instance
	// dir is later regenerated. Folderless/Door-2 manifests declare none.
	step("generating_secrets")
	secrets, err := generateSecrets(man.Secrets)
	if err != nil {
		return rollback(fmt.Errorf("generate secrets: %w", err))
	}
	if err := m.store.SetInstanceSecrets(id, secrets); err != nil {
		return rollback(fmt.Errorf("persist secrets: %w", err))
	}

	// 5c. Provision the manifest's declared managed services (Postgres in v1):
	// ensure the shared instance is running and create a per-app database+role in
	// it (SERVICE_PROVISIONING.md # Provisioning protocol). Persisted before the
	// override+env so writeOverride can attach the app to the service network and
	// writeEnv can re-emit the credentials as MOLMA_SERVICE_<NAME>_*. On a later
	// rollback the created db/role is dropped (rollback reads grants from store).
	step("provisioning_services")
	grants, err := m.provisionServices(ctx, id, man.ID, man.Services)
	if err != nil {
		return rollback(fmt.Errorf("provision services: %w", err))
	}
	if err := m.store.SetServiceGrants(id, grants); err != nil {
		m.dropServiceGrants(context.Background(), id, grants)
		return rollback(fmt.Errorf("persist service grants: %w", err))
	}

	// 5d. Bind the elected outgoing-mail provider before writeEnv reads it
	// (SERVICE_PROVISIONING.md # BYO outgoing mail). The FK catches a provider
	// deleted between the API's validation and here; rollback's instance
	// Delete cascades the binding away. No election ⇒ no row ⇒ no MOLMA_MAIL_*.
	if mailProviderID != "" {
		step("binding_mail_provider")
		if err := m.store.SetInstanceMailBinding(id, mailProviderID); err != nil {
			return rollback(fmt.Errorf("bind mail provider: %w", err))
		}
	}

	// 6. Resolve the per-instance isolation (container identity + folder binds).
	// EVERY instance runs as a resolved UID/GID via the compose `user:` field,
	// because a cap_drop:ALL container has no CAP_DAC_OVERRIDE and can only write
	// its private ./data bind when it runs as that dir's owner (APP_ISOLATION.md
	// # User content). Folder apps run as the owner's UID/GID (personal) or the
	// shared molma-app identity (household) and additionally bind use-case
	// folders. Folderless apps (and Door-2 custom apps) run as the brain's own
	// effective UID/GID — the owner of the ./data dir writeInstanceDir just
	// created (root under the production brain; the dev user under the native
	// inner-loop brain, so the bind stays writable there too) — unless the
	// manifest declares service_user: true, which swaps in a dedicated
	// host-allocated identity (APP_ISOLATION.md # Runtime identity & data
	// ownership).
	iso := isolation{uid: os.Geteuid(), gid: os.Getegid()}
	if len(man.Permissions.Folders) > 0 {
		wk, err := m.host.WellKnownIdentity(ctx)
		if err != nil {
			return rollback(fmt.Errorf("resolve host identity: %w", err))
		}
		iso.sharedGID, iso.mounts = wk.MolmaSharedGID, mounts
		if scope == store.ScopeHousehold {
			iso.uid, iso.gid = wk.MolmaAppUID, wk.MolmaAppGID
		} else {
			rh, err := m.host.ResolveHome(ctx, owner.Username)
			if errors.Is(err, hostclient.ErrUnknownUser) {
				// The owner was deleted between the install-plan call and the
				// commit — a terminal install error, not a retryable host fault.
				return rollback(fmt.Errorf("owner account %q no longer exists", owner.Username))
			}
			if err != nil {
				return rollback(fmt.Errorf("resolve owner home: %w", err))
			}
			iso.uid, iso.gid, iso.home = rh.UID, rh.GID, rh.HomePath
		}
	} else if man.ServiceUser {
		// Folderless app that writes its data as a non-root user: allocate a
		// dedicated identity from the host's app-service band and persist it on
		// the instance row — stable for the life of the instance (recreations
		// reuse the row + override; nothing re-allocates), released at
		// uninstall. Persisted before the chown/override so every later failure
		// path can read it back for release.
		alloc, err := m.host.AllocateAppServiceIdentity(ctx, id)
		if err != nil {
			return rollback(fmt.Errorf("allocate app-service identity: %w", err))
		}
		if err := m.store.SetServiceIdentity(id, alloc.UID, alloc.GID); err != nil {
			// The row never carried the allocation, so rollback can't see it —
			// release directly (mirrors the service-grants persist-failure path).
			m.releaseServiceIdentity(context.Background(), id, alloc.UID)
			return rollback(fmt.Errorf("persist app-service identity: %w", err))
		}
		inst.ServiceUID, inst.ServiceGID = alloc.UID, alloc.GID
		iso.uid, iso.gid = alloc.UID, alloc.GID
	}

	// Align the private ./data bind's ownership with the container's runtime
	// identity so the cap_drop:ALL app can write its own state. No-op when the
	// runtime UID already owns the dir (default folderless apps run as the
	// brain's euid, which created it). Under the production brain (euid 0) a
	// chown failure is a real fault; under the unprivileged native dev brain it
	// cannot chown ./data to a host-agent-assigned UID it does not own — folder
	// or service_user identities alike — so that case is downgraded to a warning
	// (default folderless apps, the common dev path, are unaffected).
	if err := os.Chown(filepath.Join(m.instanceDir(id), "data"), iso.uid, iso.gid); err != nil {
		if os.Geteuid() == 0 {
			return rollback(fmt.Errorf("chown data dir: %w", err))
		}
		slog.Warn("data dir chown skipped under unprivileged brain",
			"instance_id", id, "uid", iso.uid, "gid", iso.gid, "err", err)
	}

	// 7. Generate override (with pins + isolation) + .env.
	step("generating_override")
	if err := m.writeOverride(id, man, composeBytes, pins, iso); err != nil {
		return rollback(fmt.Errorf("override: %w", err))
	}
	if err := m.writeEnv(id, slug, iso); err != nil {
		return rollback(fmt.Errorf("env: %w", err))
	}

	// 8. Create per-app network.
	step("creating_network")
	appNet := "molma-app-" + id
	if err := m.docker.NetworkCreate(ctx, appNet, !man.Permissions.Internet); err != nil {
		return rollback(fmt.Errorf("create network: %w", err))
	}

	// 9. Publish mDNS + register the Caddy route pointing at a splash page, so
	// the hostname is reachable immediately (APP_LIFECYCLE.md # register early,
	// with a splash) instead of returning connection-refused for ~120s.
	//
	// The published name is authoritative: Publish may return a box-qualified
	// collision-fallback ("<slug>-<box>.local") that differs from the primary
	// "<slug>.local", so the Caddy route and the displayed URL must follow
	// pub.Name. We reconstruct the primary name only if publish failed, so the
	// route still exists (host-header routing keeps working even when mDNS
	// resolution doesn't).
	host := slug + protocol.AppHostSuffix
	step("publishing_mdns")
	pub, err := m.host.Publish(ctx, slug)
	if err != nil {
		slog.Warn("mDNS publish failed (continuing)",
			"instance_id", id, "slug", slug, "err", err)
	} else {
		host = pub.Name
		_ = m.store.SetMDNSName(id, pub.Name)
		inst.MDNSName = pub.Name
	}
	step("registering_route")
	if err := m.caddy.AddSplashRoute(ctx, id, host, man.Name, "starting"); err != nil {
		slog.Warn("caddy splash route failed (continuing)",
			"instance_id", id, "host", host, "err", err)
	}

	// 10. docker compose up -d, bounded by the health-wait budget. A buggy app
	// whose completion gate never completes makes `compose up -d` block forever
	// (#92); the timeout turns that into a clean install failure + rollback
	// instead of a wedged brain, independent of the layer-1 job detection.
	step("compose_up")
	upCtx, cancelUp := context.WithTimeout(ctx, m.healthWait)
	out, upErr := m.docker.ComposeUp(upCtx, m.instanceDir(id), "molma-"+id)
	cancelUp()
	if upErr != nil {
		return rollback(fmt.Errorf("compose up: %w\n%s", upErr, out))
	}

	// 11. Wait for main_service healthy. Failures here do NOT roll back: the
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

	// 12. Flip the Caddy upstream from splash to the real container.
	step("flipping_route")
	upstream := fmt.Sprintf("molma-%s-%s:%d", id, man.MainService, man.MainPort)
	if err := m.caddy.AddRoute(ctx, id, host, upstream); err != nil {
		slog.Warn("caddy upstream flip failed (continuing)",
			"instance_id", id, "host", host, "upstream", upstream, "err", err)
	}

	// 13. Mark running.
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
	defer m.lockInstance(id)()
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	// Capture the pinned images before Delete cascades the instance_images rows
	// away; the reclaim runs after the row is gone (best-effort, never fatal).
	images, err := m.store.GetInstanceImages(id)
	if err != nil {
		slog.Warn("uninstall: read pinned images for reclaim", "instance_id", id, "err", err)
	}
	// Same for managed-service grants: capture before the cascade so the per-app
	// database+role can be dropped after the app's containers are down
	// (SERVICE_PROVISIONING.md # At app uninstall). The shared service instance is
	// left running — grace-shutdown is deferred (NEXT.md).
	grants, err := m.store.GetServiceGrants(id)
	if err != nil {
		slog.Warn("uninstall: read service grants for drop", "instance_id", id, "err", err)
	}
	_ = m.store.SetState(id, "uninstalling")
	m.emitState(inst, inst.State)
	if err := m.teardown(ctx, inst, true); err != nil {
		return err
	}
	if err := m.store.Delete(id); err != nil {
		return err
	}
	m.dropServiceGrants(ctx, id, grants)
	// Return the instance's allocated app-service identity to the host's band
	// (captured in inst before the row was deleted; zero for non-service_user
	// instances). After teardown, so no container is still running as the UID.
	if inst.ServiceUID != 0 {
		m.releaseServiceIdentity(ctx, id, inst.ServiceUID)
	}
	m.bus.Publish(events.AppUninstalled, map[string]any{"instance_id": id})
	slog.Info("app uninstalled", "instance_id", id, "name", inst.Name)
	m.reclaimImages(ctx, id, images)
	return nil
}

// ErrNotRunning / ErrNotStopped are returned by Stop/Start when the instance
// is not in the state the transition requires. The API maps them to 409 so the
// UI can tell "illegal transition" apart from a missing app (404) or a host
// fault (500). ErrNoMailSupport is RebindMail's parallel: binding a provider
// to an app whose manifest has no mail block, mapped to 422.
var (
	ErrNotRunning    = errors.New("app is not running")
	ErrNotStopped    = errors.New("app is not stopped")
	ErrNoMailSupport = errors.New("app does not declare mail support")
)

// Stop halts an instance's containers without removing them
// (APP_LIFECYCLE.md # stop, start, uninstall): `docker compose stop`, never
// `down` — data, network, Caddy route, and mDNS all stay in place. The Caddy
// route flips to the "stopped" splash so the hostname serves a styled page
// instead of a connection error. Legal only from `running`.
func (m *Manager) Stop(ctx context.Context, id string) error {
	defer m.lockInstance(id)()
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	if inst.State != "running" {
		return fmt.Errorf("%w (state=%s)", ErrNotRunning, inst.State)
	}
	// Commit desired state FIRST (brain-commits-first, same as Start). If
	// ComposeStop then fails, the row already reads `stopped`, so the reconcile
	// pass sees "stopped but containers up" and retries the stop — converging on
	// the user's intent. The reverse order (stop, then write) would leave a
	// `running` row on a write failure, and reconcile would *restart* the
	// containers, silently undoing the stop.
	if err := m.store.SetState(id, "stopped"); err != nil {
		return fmt.Errorf("set state stopped: %w", err)
	}
	inst.State = "stopped"
	if out, err := m.docker.ComposeStop(ctx, m.instanceDir(id), "molma-"+id); err != nil {
		return fmt.Errorf("compose stop: %w\n%s", err, out)
	}
	// Best-effort splash flip — the route already exists; a failure here leaves
	// the real upstream pointing at now-stopped containers (which Caddy will fail
	// to reach), so it's degraded UX, not a reason to fail the stop.
	host := routeHost(inst)
	man, err := m.loadInstanceManifest(id)
	appName := inst.Name
	if err == nil {
		appName = man.Name
	}
	if err := m.caddy.AddSplashRoute(ctx, id, host, appName, "stopped"); err != nil {
		slog.Warn("stop: caddy splash flip failed (continuing)",
			"instance_id", id, "host", host, "err", err)
	}
	m.emitState(inst, "running")
	slog.Info("app stopped", "instance_id", id, "name", inst.Name)
	return nil
}

// Start brings a stopped instance back up (APP_LIFECYCLE.md # stop, start,
// uninstall). It uses `docker compose up -d` rather than `compose start` — the
// same op the reconcile pass uses — so dependency ordering and one-shot
// completion-gate jobs (#92) behave exactly as on install, and the op is
// idempotent. Legal only from `stopped`.
//
// State is written to `running` BEFORE the docker op (brain-commits-first): a
// crash mid-start leaves a `running` row the reconcile pass finishes, the same
// recovery path a reboot takes. The Caddy route flips to the "starting" splash,
// then to the real upstream once main_service is healthy.
func (m *Manager) Start(ctx context.Context, id string) error {
	defer m.lockInstance(id)()
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	if inst.State != "stopped" {
		return fmt.Errorf("%w (state=%s)", ErrNotStopped, inst.State)
	}
	man, err := m.loadInstanceManifest(id)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	host := routeHost(inst)

	// Commit desired state first, then flip the route to the starting splash so
	// the hostname stops serving the "stopped" page the moment the user acts.
	if err := m.store.SetState(id, "running"); err != nil {
		return fmt.Errorf("set state running: %w", err)
	}
	inst.State = "running"
	if err := m.caddy.AddSplashRoute(ctx, id, host, man.Name, "starting"); err != nil {
		slog.Warn("start: caddy splash flip failed (continuing)",
			"instance_id", id, "host", host, "err", err)
	}

	// Two serial budgets, matching the install transaction (steps 10-11): the
	// `up -d` is bounded by healthWait so a never-completing completion gate (#92)
	// fails cleanly instead of wedging, then waitHealthy spends a fresh healthWait
	// on the health poll. Worst-case wall time is therefore ~2×healthWait — the
	// same as install, deliberately so the two paths behave identically.
	upCtx, cancelUp := context.WithTimeout(ctx, m.healthWait)
	out, upErr := m.docker.ComposeUp(upCtx, m.instanceDir(id), "molma-"+id)
	cancelUp()
	if upErr != nil {
		return m.startFailed(ctx, inst, host, man.Name, fmt.Errorf("compose up: %w\n%s", upErr, out))
	}
	if err := m.waitHealthy(ctx, id, man.MainService, m.healthWait); err != nil {
		return m.startFailed(ctx, inst, host, man.Name, fmt.Errorf("%s did not become healthy: %w", man.Name, err))
	}

	// Healthy — flip the splash to the real container.
	upstream := fmt.Sprintf("molma-%s-%s:%d", id, man.MainService, man.MainPort)
	if err := m.caddy.AddRoute(ctx, id, host, upstream); err != nil {
		slog.Warn("start: caddy upstream flip failed (continuing)",
			"instance_id", id, "host", host, "upstream", upstream, "err", err)
	}
	m.emitState(inst, "stopped")
	slog.Info("app started", "instance_id", id, "name", inst.Name, "upstream", upstream)
	return nil
}

// startFailed parks a start that came up but never went healthy in the same
// `failed` state install uses (APP_LIFECYCLE.md install transaction, steps
// 10-11 failure): instance dir kept for inspection, route flipped to the
// "failed" splash. The containers are left up — Docker keeps retrying — so the
// app can recover without a manual start.
func (m *Manager) startFailed(ctx context.Context, inst store.Instance, host, appName string, cause error) error {
	// Both flips are best-effort but must leave a trace — a silent failure here
	// strands the route on the "starting" splash (wrong page, no log) or leaves
	// the row at `running` while the app is down (reconcile retries, but the
	// operator gets no signal). Mirror the warn-and-continue pattern in Stop.
	if err := m.caddy.AddSplashRoute(ctx, inst.ID, host, appName, "failed"); err != nil {
		slog.Warn("startFailed: caddy splash flip failed", "instance_id", inst.ID, "host", host, "err", err)
	}
	if err := m.store.SetState(inst.ID, "failed"); err != nil {
		slog.Warn("startFailed: set state failed (row stays running; reconcile will retry)",
			"instance_id", inst.ID, "err", err)
	}
	inst.State = "failed"
	m.emitState(inst, "running")
	slog.Warn("app start failed", "instance_id", inst.ID, "name", inst.Name, "err", cause)
	return cause
}

// routeHost is the hostname an instance's Caddy route is keyed on: the published
// mDNS name when we have one (it may be the box-qualified collision fallback),
// else the reconstructed primary `<slug>.local`. Mirrors the fallback chain in
// install + reassertRouting so the three never disagree.
func routeHost(inst store.Instance) string {
	if inst.MDNSName != "" {
		return inst.MDNSName
	}
	return inst.Slug + protocol.AppHostSuffix
}

// releaseServiceIdentity returns an allocated app-service identity to the
// host's band. Best-effort like dropServiceGrants: a failed release leaks one
// band slot (the host-side molma-svc account stays for manual cleanup) and is
// logged, but never blocks an uninstall or rollback.
func (m *Manager) releaseServiceIdentity(ctx context.Context, id string, uid int) {
	if err := m.host.ReleaseAppServiceIdentity(ctx, uid); err != nil {
		slog.Warn("release app-service identity", "instance_id", id, "uid", uid, "err", err)
		return
	}
	slog.Info("released app-service identity", "instance_id", id, "uid", uid)
}

// reclaimImages removes the just-uninstalled instance's pinned images from the
// local Docker store, skipping any image another installed instance still
// references (APP_LIFECYCLE.md # stop, start, uninstall). Call AFTER the
// instance row is deleted, so "still referenced" is simply every remaining
// instance_images row. Best-effort: a held image (rmi refused) is logged, never
// fatal. Periodic / update-orphaned image GC is out of scope (NEXT.md #
// Container image cleanup).
func (m *Manager) reclaimImages(ctx context.Context, instanceID string, images []store.InstanceImage) {
	if len(images) == 0 {
		return
	}
	inUse, err := m.inUseImageRefs()
	if err != nil {
		slog.Warn("reclaim images: list in-use refs", "instance_id", instanceID, "err", err)
		return
	}
	done := map[string]bool{}
	for _, img := range images {
		ref := repoOf(img.Image) + "@" + img.Digest
		if inUse[ref] || done[ref] {
			continue
		}
		done[ref] = true
		if err := m.docker.RemoveImage(ctx, ref); err != nil {
			slog.Warn("reclaim images: rmi", "instance_id", instanceID, "image", ref, "err", err)
			continue
		}
		slog.Info("reclaimed image", "instance_id", instanceID, "image", ref)
	}
}

// inUseImageRefs is the set of pinned `repo@sha256:…` references held by all
// currently-installed instances — the "don't remove" guard for reclaimImages.
func (m *Manager) inUseImageRefs() (map[string]bool, error) {
	insts, err := m.store.List()
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for _, inst := range insts {
		imgs, err := m.store.GetInstanceImages(inst.ID)
		if err != nil {
			return nil, err
		}
		for _, img := range imgs {
			refs[repoOf(img.Image)+"@"+img.Digest] = true
		}
	}
	return refs, nil
}

// teardown reverses the resources install creates. Each step is best-effort so
// a partial install can always be cleaned up.
func (m *Manager) teardown(ctx context.Context, inst store.Instance, removeDir bool) error {
	if _, err := os.Stat(m.composeFile(inst.ID)); err == nil {
		if out, err := m.docker.ComposeDown(ctx, m.instanceDir(inst.ID), "molma-"+inst.ID); err != nil {
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
	_ = m.docker.NetworkRemove(ctx, "molma-app-"+inst.ID)
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
	// Re-assert the shared managed-service instances first, so an app drifted to
	// "no containers" comes back up against a live database.
	m.reconcileServices(ctx)

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
				if out, err := m.docker.ComposeUp(ctx, m.instanceDir(inst.ID), "molma-"+inst.ID); err != nil {
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
				if out, err := m.docker.ComposeStop(ctx, m.instanceDir(inst.ID), "molma-"+inst.ID); err != nil {
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
	// Use the freshly published name (which may be the box-qualified collision
	// fallback) for the route. Persist it if it changed since install; fall
	// back to the stored name, then to the reconstructed primary, so the route
	// always exists even when mDNS publish fails.
	host := inst.MDNSName
	if pub, err := m.host.Publish(ctx, inst.Slug); err != nil {
		slog.Warn("reconcile: mDNS publish",
			"instance_id", inst.ID, "slug", inst.Slug, "err", err)
		avahiOK = false
	} else {
		host = pub.Name
		if pub.Name != inst.MDNSName {
			_ = m.store.SetMDNSName(inst.ID, pub.Name)
		}
	}
	if host == "" {
		host = inst.Slug + protocol.AppHostSuffix
	}
	upstream := fmt.Sprintf("molma-%s-%s:%d", inst.ID, man.MainService, man.MainPort)
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
		if out, err := m.docker.ComposeDown(ctx, m.instanceDir(id), "molma-"+id); err != nil {
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
	_ = m.docker.NetworkRemove(ctx, "molma-app-"+id)
}

func (m *Manager) loadInstanceManifest(id string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(filepath.Join(m.instanceDir(id), "manifest.yml"))
	if err != nil {
		return nil, err
	}
	return manifest.Parse(b)
}

// InstanceManifest returns the parsed manifest the installer persisted for an
// installed instance. Thin export so callers don't duplicate the instance-dir
// path layout.
func (m *Manager) InstanceManifest(id string) (*manifest.Manifest, error) {
	return m.loadInstanceManifest(id)
}

// MainContainerName returns the container name of an instance's main service —
// "molma-<id>-<MainService>", the same project+service stem used for the Caddy
// upstream alias. The per-app Logs tail keys on it (the brain hands it to
// host-agent's journal follow, which matches Docker's journald CONTAINER_NAME).
// writeOverride pins the running container to exactly this name (no compose
// replica suffix), so the exact match holds on a real host.
func (m *Manager) MainContainerName(id string) (string, error) {
	man, err := m.loadInstanceManifest(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("molma-%s-%s", id, man.MainService), nil
}

// allocateSlug derives a free, routable slug from the manifest's preferred
// slugs. The hostname encodes *uniqueness, not ownership* (DASHBOARD.md #
// instance naming): the bare `<base>` is preferred by every instance regardless
// of scope, first-come-first-served — so a single-user box gets clean
// `photos.local`, not `photos--admin.local`. Only on a collision do we
// disambiguate: a personal instance trails the owner (`<base>--<user>`), and a
// household instance (no owner to name) falls back to a numeric suffix. The
// trailing numeric variants are the last-resort for the rare double collision.
func (m *Manager) allocateSlug(man *manifest.Manifest, scope, username string) (string, error) {
	bases := man.PreferredSlugs
	if len(bases) == 0 {
		bases = []string{man.ID}
	}
	for _, base := range bases {
		// Bare first (first-come). Then the owner-qualified form for personal
		// instances. Then numeric, covering household collisions and the same
		// owner installing the same app more than once.
		candidates := []string{base}
		if scope == store.ScopePersonal {
			candidates = append(candidates, base+"--"+username)
		}
		candidates = append(candidates, base+"-2", base+"-3")
		for _, slug := range candidates {
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
	return "", fmt.Errorf("no free slug for %s", man.ID)
}

func (m *Manager) emitState(inst store.Instance, prev string) {
	m.bus.Publish(events.AppStateChanged, map[string]any{
		"instance_id": inst.ID, "state": inst.State, "prev": prev,
	})
}

// --- on-disk + compose helpers -------------------------------------------

func (m *Manager) composeFile(id string) string {
	return filepath.Join(m.instanceDir(id), "compose.yml")
}

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
func (m *Manager) writeOverride(id string, man *manifest.Manifest, composeBytes []byte, pins []servicePin, iso isolation) error {
	svcs, err := parseComposeServices(composeBytes)
	if err != nil {
		return err
	}
	// Services the author designed to terminate must NOT be force-restarted: a
	// one-shot job that Docker restarts never reaches the "completed" state, so a
	// `service_completed_successfully` gate waiting on it hangs `compose up -d`
	// forever (#92). A job is detected from the union of two signals — see
	// isTerminatingJob. main_service is always forced long-running.
	gateTargets := completionGateTargets(svcs)
	pinBySvc := make(map[string]string, len(pins))
	for _, p := range pins {
		pinBySvc[p.Service] = p.PinnedRef()
	}
	// Mount mode is the manifest's say (read→:ro, write→:rw); the election only
	// chose the source, never the mode.
	modeByFolder := make(map[string]string, len(man.Permissions.Folders))
	for _, f := range man.Permissions.Folders {
		modeByFolder[f.Folder] = f.Mode
	}
	appNet := "molma-app-" + id
	// Managed-service networks the app's declared services must reach
	// (SERVICE_PROVISIONING.md # Network architecture). Every service in the app's
	// compose joins them — kan's `migrate` job and `web` both need the DSN — so
	// membership, not a software allowlist, is what gates reachability.
	svcNets := serviceNetworkNames(man.Services)
	services := map[string]any{}
	for svc := range svcs {
		nets := map[string]any{appNet: nil}
		for _, sn := range svcNets {
			nets[sn] = nil
		}
		if svc == man.MainService {
			nets[ingressNetwork] = map[string]any{
				"aliases": []string{fmt.Sprintf("molma-%s-%s", id, man.MainService)},
			}
		}
		entry := map[string]any{
			"cap_drop":     []string{"ALL"},
			"security_opt": []string{"no-new-privileges:true"},
			"networks":     nets,
			// Labels let the reconciler find managed containers and map them
			// back to instances (APP_LIFECYCLE.md # an app instance is a
			// compose project).
			"labels": map[string]string{
				"molma.managed":     "true",
				"molma.instance_id": id,
				"molma.manifest_id": man.ID,
			},
		}
		// Pin the main service's *running* container name to the same
		// molma-<id>-<service> stem as the ingress alias above — without the
		// pin compose appends a replica suffix ("-1"), and Docker's journald
		// driver tags log lines with that suffixed name, so the per-app Logs
		// tail's exact CONTAINER_NAME match (MainContainerName → journalsource)
		// finds nothing on a real host (#83). An explicit container_name makes
		// the service unscalable, which the single-replica main service already
		// is by design; sidecars stay unpinned so the constraint never lands on
		// an author's scalable workers. Same pattern as the managed services'
		// fixed exec handle (services.go).
		if svc == man.MainService {
			entry["container_name"] = fmt.Sprintf("molma-%s-%s", id, man.MainService)
		}
		// Forced restart, EXCEPT for author-declared terminating jobs and
		// completion-gate targets (#92). main_service is always forced — a paranoid
		// or buggy author can't accidentally exempt the actual app. For a real job
		// we omit `restart` from the override so the author's compose.yml value wins
		// verbatim (including the Compose default of "no" when they wrote none).
		if svc == man.MainService || !isTerminatingJob(svcs[svc], gateTargets[svc]) {
			entry["restart"] = "unless-stopped"
		}
		if ref, ok := pinBySvc[svc]; ok {
			entry["image"] = ref
		}
		// Run as the resolved runtime identity (every instance — folderless apps
		// as the brain's euid). Folder apps additionally bind each declared folder
		// at /molma/<folder> from its elected source and join molma-shared when any
		// source is the household tree (APP_ISOLATION.md # User content).
		entry["user"] = fmt.Sprintf("%d:%d", iso.uid, iso.gid)
		volumes := make([]string, 0, len(iso.mounts))
		needShared := false
		for _, mt := range iso.mounts {
			mode := ":ro"
			if modeByFolder[mt.Folder] == "write" {
				mode = ":rw"
			}
			volumes = append(volumes, iso.hostSource(mt)+":"+containerDest(mt)+mode)
			if mt.Source == sourceShared {
				needShared = true
			}
		}
		if len(volumes) > 0 {
			entry["volumes"] = volumes
		}
		if needShared {
			entry["group_add"] = []string{strconv.Itoa(iso.sharedGID)}
		}
		// Device passthrough (APP_ISOLATION.md # Devices). Each declared /dev path
		// is exposed at the same path inside the container. Host-side existence
		// validation is deferred (needs a host capability query).
		if len(man.Permissions.Devices) > 0 {
			devices := make([]string, len(man.Permissions.Devices))
			for i, d := range man.Permissions.Devices {
				devices[i] = d + ":" + d
			}
			entry["devices"] = devices
		}
		services[svc] = entry
	}
	networks := map[string]any{
		appNet:         map[string]any{"external": true},
		ingressNetwork: map[string]any{"external": true},
	}
	for _, sn := range svcNets {
		networks[sn] = map[string]any{"external": true}
	}
	override := map[string]any{
		"services": services,
		"networks": networks,
	}
	out, err := yaml.Marshal(override)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.instanceDir(id), "compose.override.yml"), out, 0o644)
}

func (m *Manager) writeEnv(id, slug string, iso isolation) error {
	dataDir, _ := filepath.Abs(filepath.Join(m.instanceDir(id), "data"))
	lines := []string{
		"MOLMA_INSTANCE_ID=" + id,
		"MOLMA_APP_URL=http://" + slug + protocol.AppHostSuffix,
		"MOLMA_DATA_DIR=" + dataDir,
	}
	// Inject the in-container path for each bound folder (APP_MANIFEST.md #
	// folders) — a store app's compose maps MOLMA_FOLDER_<NAME> to its library
	// path; a Door-2 grant already bound straight to its target, but the var still
	// reflects the real in-container path. Stable regardless of the elected source.
	for _, mt := range iso.mounts {
		lines = append(lines, "MOLMA_FOLDER_"+strings.ToUpper(mt.Folder)+"="+containerDest(mt))
	}
	// Re-emit the instance's generated secrets as MOLMA_SECRET_<NAME>
	// (SERVICE_PROVISIONING.md # Env-var injection). Read from the store rather
	// than regenerated, so the value is stable across every .env rewrite — a
	// token-signing secret that changed here would invalidate all live sessions.
	secrets, err := m.store.GetInstanceSecrets(id)
	if err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}
	for _, sec := range secrets {
		lines = append(lines, "MOLMA_SECRET_"+strings.ToUpper(sec.Name)+"="+sec.Value)
	}
	// Re-emit provisioned managed-service credentials as MOLMA_SERVICE_<NAME>_*
	// (SERVICE_PROVISIONING.md # Env-var injection). HOST is the in-network DNS
	// alias; the app maps these (or the all-in-one DSN) to whatever it expects.
	grants, err := m.store.GetServiceGrants(id)
	if err != nil {
		return fmt.Errorf("load service grants: %w", err)
	}
	for _, g := range grants {
		prefix := "MOLMA_SERVICE_" + strings.ToUpper(g.LogicalName) + "_"
		host := serviceDNSAlias(g.Kind, g.Version)
		port := servicePort[g.Kind]
		dsn := fmt.Sprintf("%s://%s:%s@%s:%d/%s", serviceDSNScheme[g.Kind], g.RoleName, g.Password, host, port, g.DBName)
		lines = append(lines,
			prefix+"HOST="+host,
			prefix+"PORT="+strconv.Itoa(port),
			prefix+"NAME="+g.DBName,
			prefix+"USER="+g.RoleName,
			prefix+"PASSWORD="+g.Password,
			prefix+"DSN="+dsn,
		)
	}
	// Re-emit the bound outgoing-mail provider as MOLMA_MAIL_*
	// (SERVICE_PROVISIONING.md # BYO outgoing mail). Unbound (ErrNotFound) is
	// the common case and injects nothing — a mail-capable app must run
	// without it (manifest validation enforces optional: true).
	mp, err := m.store.GetInstanceMailProvider(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("load mail binding: %w", err)
	}
	if err == nil {
		lines = append(lines, mailEnvLines(mp)...)
	}
	env := strings.Join(append(lines, ""), "\n")
	return os.WriteFile(filepath.Join(m.instanceDir(id), ".env"), []byte(env), 0o644)
}

// generateSecrets draws a fresh CSPRNG value for each declared secret and
// base64url-encodes it (SERVICE_PROVISIONING.md # Env-var injection). Called
// once per install; the result is persisted and thereafter re-emitted verbatim.
// manifest validation has already normalized each Bytes to a safe floor.
func generateSecrets(decls []manifest.Secret) ([]store.InstanceSecret, error) {
	out := make([]store.InstanceSecret, 0, len(decls))
	for _, d := range decls {
		buf := make([]byte, d.Bytes)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("secret %q: %w", d.Name, err)
		}
		out = append(out, store.InstanceSecret{
			Name:  d.Name,
			Value: base64.RawURLEncoding.EncodeToString(buf),
		})
	}
	return out, nil
}

// composeService is the subset of an author compose service the override
// generator needs: its declared restart policy (to decide whether the forced
// `unless-stopped` is safe) and its depends_on conditions (to find which
// services are completion-gate targets — #92).
type composeService struct {
	Restart   string
	DependsOn map[string]string // dep service name → condition (long-form only)
}

// parseComposeServices extracts each author service's restart policy and
// depends_on conditions. depends_on has two shapes in Compose: the short list
// form (`[a, b]`, no conditions) and the long map form
// (`{a: {condition: …}}`). Only the long form can carry
// service_completed_successfully, so the short form is parsed to nothing.
func parseComposeServices(composeBytes []byte) (map[string]composeService, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose services: %w", err)
	}
	if len(doc.Services) == 0 {
		return nil, fmt.Errorf("compose has no services")
	}
	out := make(map[string]composeService, len(doc.Services))
	for name, node := range doc.Services {
		var raw struct {
			Restart   string    `yaml:"restart"`
			DependsOn yaml.Node `yaml:"depends_on"`
		}
		if err := node.Decode(&raw); err != nil {
			return nil, fmt.Errorf("parse service %q: %w", name, err)
		}
		svc := composeService{Restart: raw.Restart}
		if raw.DependsOn.Kind == yaml.MappingNode {
			var deps map[string]struct {
				Condition string `yaml:"condition"`
			}
			if err := raw.DependsOn.Decode(&deps); err != nil {
				return nil, fmt.Errorf("parse service %q depends_on: %w", name, err)
			}
			svc.DependsOn = make(map[string]string, len(deps))
			for dep, d := range deps {
				svc.DependsOn[dep] = d.Condition
			}
		}
		out[name] = svc
	}
	return out, nil
}

// isTerminatingJob reports whether a service was designed to run to completion
// rather than stay up, from the union of two signals (#92): (a) the author set a
// terminating restart policy ("no" or "on-failure"), or (b) the service is the
// target of another service's service_completed_successfully gate — which
// catches an author who omitted restart entirely (Compose default is "no"),
// the case signal (a) alone misses.
func isTerminatingJob(svc composeService, isGateTarget bool) bool {
	// "on-failure" may carry a retry count ("on-failure:5"); match the prefix.
	return svc.Restart == "no" || strings.HasPrefix(svc.Restart, "on-failure") || isGateTarget
}

// completionGateTargets is the set of services that some other service waits on
// with `depends_on: {condition: service_completed_successfully}` — i.e. jobs the
// author designed to run to completion, not stay up. Forcing restart on these
// wedges `compose up -d` forever on the gate (#92).
func completionGateTargets(svcs map[string]composeService) map[string]bool {
	targets := map[string]bool{}
	for _, svc := range svcs {
		for dep, cond := range svc.DependsOn {
			if cond == "service_completed_successfully" {
				targets[dep] = true
			}
		}
	}
	return targets
}

func newInstanceID(manifestID string) string {
	return fmt.Sprintf("%s-%s", manifestID, time.Now().Format("20060102t150405"))
}
