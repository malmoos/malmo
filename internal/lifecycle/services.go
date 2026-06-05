package lifecycle

// Managed data services — Tier 1 (SERVICE_PROVISIONING.md). The brain runs one
// shared container per service type+version (lazy spinup), and provisions a
// per-app database+role inside it. Credentials are injected back into the app
// as MOLMA_SERVICE_<NAME>_*. v1 supports Postgres; Redis is staged after.
//
// Provisioning runs through `docker exec <svc-container> psql` (DockerDriver.Exec)
// rather than a Go SQL client, so the brain never joins the service's Docker
// network (DECISIONS.md 2026-06-02 — control plane off app-reachable networks).
// Inside the official postgres image the local unix socket trusts the postgres
// superuser, so no password is needed for the exec'd psql.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/molmaos/molma/internal/manifest"
	"github.com/molmaos/molma/internal/store"
)

// serviceReadyTimeout bounds the lazy-spinup readiness wait (a cold Postgres
// container initialising its data dir). Generous: first boot runs initdb.
const serviceReadyTimeout = 90 * time.Second

// servicePort is the in-container port each managed service kind listens on.
var servicePort = map[string]int{"postgres": 5432}

// serviceTag maps a managed kind to the upstream image repo; the version is the
// tag. v1 pins by tag (digest-pinning the service image is deferred, NEXT.md).
var serviceImageRepo = map[string]string{"postgres": "postgres"}

// serviceName is the "<kind>-<version>" stem used for the container name, the
// Docker network, and the in-network DNS alias.
func serviceName(kind, version string) string { return kind + "-" + version }

// serviceContainerName is the brain's docker-exec management handle.
func serviceContainerName(kind, version string) string {
	return "molma-svc-" + serviceName(kind, version)
}

// serviceNetworkName is the dedicated internal network apps attach to in order
// to reach this service; no declaration → no membership → no reachability.
func serviceNetworkName(kind, version string) string {
	return "molma-svc-" + serviceName(kind, version)
}

// serviceDNSAlias is the host apps put in their DSN. Matches the name
// SERVICE_PROVISIONING.md states verbatim (e.g. postgres-15.molma.internal).
func serviceDNSAlias(kind, version string) string {
	return serviceName(kind, version) + ".molma.internal"
}

func (m *Manager) serviceDir(kind, version string) string {
	return filepath.Join(m.stateDir, "services", serviceName(kind, version))
}

// serviceNetworkNames returns the set of service networks an app's declared
// services require — one per distinct kind+version. writeOverride attaches every
// app service to these so any container in the app's compose can reach the DB.
func serviceNetworkNames(services map[string]manifest.ServiceDep) []string {
	seen := map[string]bool{}
	var out []string
	for _, key := range sortedServiceKeys(services) {
		dep := services[key]
		n := serviceNetworkName(dep.Type, dep.Version)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func sortedServiceKeys(services map[string]manifest.ServiceDep) []string {
	keys := make([]string, 0, len(services))
	for k := range services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// provisionServices provisions every managed service the manifest declares and
// returns the resulting grants for persistence. For each declaration it ensures
// the shared service instance is running, then creates a dedicated database+role
// inside it. Returns nil for a manifest with no services. Postgres only in v1;
// any other (schema-valid) type is a terminal install error until its
// provisioning lands.
func (m *Manager) provisionServices(ctx context.Context, instanceID, manifestID string, services map[string]manifest.ServiceDep) ([]store.ServiceGrant, error) {
	if len(services) == 0 {
		return nil, nil
	}
	var grants []store.ServiceGrant
	for _, key := range sortedServiceKeys(services) {
		dep := services[key]
		if dep.Type != "postgres" {
			return nil, fmt.Errorf("managed service %q (%s) is not provisioned yet", key, dep.Type)
		}
		if err := m.ensureServiceInstance(ctx, dep.Type, dep.Version); err != nil {
			return nil, fmt.Errorf("ensure %s-%s: %w", dep.Type, dep.Version, err)
		}
		dbName := sanitizeIdent(manifestID) + "_" + randSuffix()
		pw, err := randSecret(24)
		if err != nil {
			return nil, fmt.Errorf("service password: %w", err)
		}
		if err := m.provisionPostgresDB(ctx, dep.Version, dbName, pw); err != nil {
			return nil, fmt.Errorf("provision %s db: %w", key, err)
		}
		grants = append(grants, store.ServiceGrant{
			LogicalName: key, Kind: dep.Type, Version: dep.Version,
			DBName: dbName, RoleName: dbName, Password: pw,
		})
		slog.Info("provisioned managed service",
			"instance_id", instanceID, "service", key, "kind", dep.Type, "version", dep.Version)
	}
	return grants, nil
}

// ensureServiceInstance starts the shared service container of a kind+version if
// it isn't already recorded (lazy spinup). Idempotent: an existing row means the
// instance was spun up before and the reconcile pass keeps it running.
func (m *Manager) ensureServiceInstance(ctx context.Context, kind, version string) error {
	if _, err := m.store.GetServiceInstance(kind, version); err == nil {
		return nil
	} else if err != store.ErrNotFound {
		return err
	}

	superuserPW, err := randSecret(24)
	if err != nil {
		return err
	}
	if err := m.writeServiceDir(kind, version, superuserPW); err != nil {
		return err
	}
	netName := serviceNetworkName(kind, version)
	if err := m.docker.NetworkCreate(ctx, netName, true); err != nil {
		return fmt.Errorf("create service network: %w", err)
	}
	if out, err := m.docker.ServiceUp(ctx, m.serviceDir(kind, version), netName); err != nil {
		return fmt.Errorf("service up: %w\n%s", err, out)
	}
	if err := m.waitServiceReady(ctx, kind, version); err != nil {
		return err
	}
	if err := m.store.CreateServiceInstance(store.ServiceInstance{
		Kind: kind, Version: version, SuperuserPassword: superuserPW,
		State: "running", CreatedAt: time.Now(),
	}); err != nil {
		return err
	}
	slog.Info("started managed service instance", "kind", kind, "version", version)
	return nil
}

// waitServiceReady polls the service container's own readiness probe until it
// passes or the timeout elapses. For Postgres that's `pg_isready` over the local
// socket (exit 0 = accepting connections), which also clears the initdb window.
func (m *Manager) waitServiceReady(ctx context.Context, kind, version string) error {
	container := serviceContainerName(kind, version)
	deadline := time.Now().Add(serviceReadyTimeout)
	var last string
	for {
		out, err := m.docker.Exec(ctx, container, []string{"pg_isready", "-U", "postgres", "-q"})
		if err == nil {
			return nil
		}
		last = strings.TrimSpace(out)
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s (last: %s)", container, serviceReadyTimeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.healthPoll):
		}
	}
}

// provisionPostgresDB creates a login role and an owned database inside the
// shared Postgres container via docker-exec psql. ON_ERROR_STOP makes the
// multi-statement run fail fast; the role owns only its own database, which is
// the per-app isolation boundary (SERVICE_PROVISIONING.md # Per-app isolation).
func (m *Manager) provisionPostgresDB(ctx context.Context, version, dbName, password string) error {
	container := serviceContainerName("postgres", version)
	// Each statement goes in its own -c: CREATE DATABASE cannot run inside a
	// transaction block, and psql wraps a single multi-statement -c in one
	// transaction. Repeated -c runs each in its own (ON_ERROR_STOP still halts
	// the run on the first failure). The role owns only its own database — the
	// per-app isolation boundary.
	if out, err := m.docker.Exec(ctx, container, []string{
		"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1",
		"-c", fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s'`, dbName, password),
		"-c", fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, dbName, dbName),
	}); err != nil {
		return fmt.Errorf("psql: %w\n%s", err, out)
	}
	return nil
}

// dropServiceGrants reverses provisionServices: it drops each grant's database
// and role inside the shared instance. Best-effort — a failure is logged, never
// fatal (uninstall must always complete). FORCE terminates any straggler
// connection (the app's own containers are already down by call time).
func (m *Manager) dropServiceGrants(ctx context.Context, instanceID string, grants []store.ServiceGrant) {
	for _, g := range grants {
		if g.Kind != "postgres" {
			continue
		}
		container := serviceContainerName(g.Kind, g.Version)
		// Separate -c per statement: DROP DATABASE, like CREATE, cannot run inside
		// a transaction block. FORCE terminates any straggler connection.
		if out, err := m.docker.Exec(ctx, container, []string{
			"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1",
			"-c", fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, g.DBName),
			"-c", fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, g.RoleName),
		}); err != nil {
			slog.Warn("drop managed-service db", "instance_id", instanceID,
				"service", g.LogicalName, "db", g.DBName, "err", err, "output", strings.TrimSpace(out))
			continue
		}
		slog.Info("dropped managed-service db", "instance_id", instanceID, "service", g.LogicalName)
	}
}

// reconcileServices re-asserts every recorded service instance is up at brain
// startup. `restart: unless-stopped` already keeps them alive across a daemon
// restart; this covers the case where the whole host (or Docker) was reset and
// the brain comes back first. Best-effort. Service containers carry the
// molma.service label (not molma.managed=true), so the app-orphan reaper in
// Reconcile never touches them.
func (m *Manager) reconcileServices(ctx context.Context) {
	instances, err := m.store.ListServiceInstances()
	if err != nil {
		slog.Warn("reconcile services: list", "err", err)
		return
	}
	for _, si := range instances {
		netName := serviceNetworkName(si.Kind, si.Version)
		if err := m.docker.NetworkCreate(ctx, netName, true); err != nil {
			slog.Warn("reconcile services: network", "kind", si.Kind, "version", si.Version, "err", err)
		}
		if out, err := m.docker.ServiceUp(ctx, m.serviceDir(si.Kind, si.Version), netName); err != nil {
			slog.Warn("reconcile services: up", "kind", si.Kind, "version", si.Version, "err", err, "output", out)
		}
	}
}

// writeServiceDir lays down the generated compose.yml + .env for a service
// instance under <stateDir>/services/<kind>-<version>/.
func (m *Manager) writeServiceDir(kind, version, superuserPW string) error {
	if kind != "postgres" {
		return fmt.Errorf("managed service %q is not provisioned yet", kind)
	}
	dir := m.serviceDir(kind, version)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		return err
	}
	compose := postgresServiceCompose(version)
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		return err
	}
	env := "POSTGRES_SUPERUSER_PASSWORD=" + superuserPW + "\n"
	return os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600)
}

// postgresServiceCompose renders the shared-Postgres compose. The network is
// external (the brain creates it `--internal` before `up`); the service joins it
// under the postgres-<version>.molma.internal alias apps use in their DSN, and
// pg_isready backs the healthcheck. container_name is the brain's exec handle.
func postgresServiceCompose(version string) string {
	container := serviceContainerName("postgres", version)
	netName := serviceNetworkName("postgres", version)
	alias := serviceDNSAlias("postgres", version)
	image := serviceImageRepo["postgres"] + ":" + version
	return fmt.Sprintf(`services:
  postgres:
    image: %s
    container_name: %s
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${POSTGRES_SUPERUSER_PASSWORD}
    volumes:
      - ./data:/var/lib/postgresql/data
    networks:
      svc:
        aliases:
          - %s
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 3s
      retries: 10
    labels:
      molma.service: %s
networks:
  svc:
    name: %s
    external: true
`, image, container, alias, serviceName("postgres", version), netName)
}

// sanitizeIdent reduces a manifest id to a safe SQL identifier stem: lowercase
// alphanumerics, every other rune folded to '_'. Combined with a random suffix
// it yields the per-app database/role name (e.g. kan → kan_a4f7).
func sanitizeIdent(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "app_" + out
	}
	return out
}

// randSuffix is a short hex disambiguator for per-app db/role names.
func randSuffix() string {
	buf := make([]byte, 2)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// randSecret draws n CSPRNG bytes and base64url-encodes them. Used for the
// service superuser password and per-app role passwords. base64url has no
// shell/SQL-quoting hazards (A–Za–z0–9-_).
func randSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
