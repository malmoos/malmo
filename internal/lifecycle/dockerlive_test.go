//go:build dockerlive

package lifecycle

// Real-system verification for managed-service provisioning: drives the actual
// Manager against the real `docker`/`docker compose` CLI and a real Postgres
// container (host/caddy are fakes — they don't touch the new path). Run with:
//
//	go test ./internal/lifecycle/ -tags dockerlive -run TestLivePostgresProvisioning -v
//
// Requires a working Docker daemon and network access to pull postgres + the
// app image. Excluded from the default suite by the build tag.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/molmaos/molma/internal/admission"
	"github.com/molmaos/molma/internal/catalog"
	"github.com/molmaos/molma/internal/events"
	"github.com/molmaos/molma/internal/store"
)

func TestLivePostgresProvisioning(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	catDir := t.TempDir()
	s, err := store.Open(filepath.Join(stateDir, "molma.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cat := catalog.New(catDir)
	docker := NewCLIDocker()
	m := NewManager(s, cat, newFakeHost(), newFakeCaddy(), docker, events.NewBus(), stateDir)
	m.SetAdmitter(admission.Check) // real `docker compose config -q`

	// Real ingress network so the app override's external reference resolves.
	if err := docker.NetworkCreate(ctx, ingressNetwork, false); err != nil {
		t.Fatalf("ingress net: %v", err)
	}

	// A folderless app whose main service is a real, fast-starting image. It
	// declares a Postgres dependency so the brain provisions a DB+role and
	// injects the DSN; we verify the provisioning side-effects directly.
	man := `
id: liveapp
manifest_version: 1
name: Live App
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 80
preferred_slugs: [liveapp]
services:
  database:
    type: postgres
    version: "15"
permissions:
  internet: false
  lan: false
`
	compose := `
services:
  app:
    image: traefik/whoami:v1.10.3
    environment:
      POSTGRES_URL: ${MOLMA_SERVICE_DATABASE_DSN}
`
	writeLiveCatalogApp(t, catDir, "liveapp", compose, man)

	// Cleanup: uninstall the app (drops DB/role), then tear down the shared
	// service container + network the test created.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", serviceContainerName("postgres", "15")).Run()
		_ = exec.Command("docker", "network", "rm", serviceNetworkName("postgres", "15")).Run()
		_ = exec.Command("docker", "network", "rm", ingressNetwork).Run()
		// Postgres writes its data dir as the container's uid (root); remove it via
		// a throwaway container so t.TempDir cleanup doesn't hit permission-denied.
		_ = exec.Command("docker", "run", "--rm", "-v", stateDir+":/s",
			"alpine", "rm", "-rf", "/s/services").Run()
	})

	inst, err := m.Install(ctx, "liveapp",
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	grants, err := s.GetServiceGrants(inst.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants = %v, err %v", grants, err)
	}
	dbName := grants[0].DBName

	// The shared Postgres container is up and actually has the provisioned DB.
	if !pgDatabaseExists(t, dbName) {
		t.Fatalf("database %q not found in the live Postgres", dbName)
	}
	t.Logf("provisioned database %q present in live Postgres", dbName)

	// The role can connect to its own database with the injected password.
	if !pgRoleCanConnect(t, grants[0].RoleName, grants[0].Password, dbName) {
		t.Fatalf("role %q could not connect to %q with injected password", grants[0].RoleName, dbName)
	}
	t.Logf("role %q connects to %q with the injected credentials", grants[0].RoleName, dbName)

	// Uninstall drops the database.
	if err := m.Uninstall(ctx, inst.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if pgDatabaseExists(t, dbName) {
		t.Fatalf("database %q survived uninstall", dbName)
	}
	t.Logf("database %q dropped on uninstall", dbName)
}

// TestLiveMySQLProvisioning mirrors TestLivePostgresProvisioning for the MySQL
// family: a real mysql:8.0 service container is lazily spun up, the per-app
// DB+user is provisioned via docker-exec of the image's own client, the user
// connects over TCP with the injected password, and the DB is dropped on
// uninstall. MariaDB shares the code path (only the image, client binaries, and
// root-password env var differ), so one live engine suffices.
//
//	go test ./internal/lifecycle/ -tags dockerlive -run TestLiveMySQLProvisioning -v -timeout 300s
func TestLiveMySQLProvisioning(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	catDir := t.TempDir()
	s, err := store.Open(filepath.Join(stateDir, "molma.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cat := catalog.New(catDir)
	docker := NewCLIDocker()
	m := NewManager(s, cat, newFakeHost(), newFakeCaddy(), docker, events.NewBus(), stateDir)
	m.SetAdmitter(admission.Check)

	if err := docker.NetworkCreate(ctx, ingressNetwork, false); err != nil {
		t.Fatalf("ingress net: %v", err)
	}

	man := `
id: livemysql
manifest_version: 1
name: Live MySQL App
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 80
preferred_slugs: [livemysql]
services:
  database:
    type: mysql
    version: "8.0"
permissions:
  internet: false
  lan: false
`
	compose := `
services:
  app:
    image: traefik/whoami:v1.10.3
    environment:
      DATABASE_URL: ${MOLMA_SERVICE_DATABASE_DSN}
`
	writeLiveCatalogApp(t, catDir, "livemysql", compose, man)

	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", serviceContainerName("mysql", "8.0")).Run()
		_ = exec.Command("docker", "network", "rm", serviceNetworkName("mysql", "8.0")).Run()
		_ = exec.Command("docker", "network", "rm", ingressNetwork).Run()
		// mysqld chowns its data dir to the mysql user; remove it via a throwaway
		// container so t.TempDir cleanup doesn't hit permission-denied.
		_ = exec.Command("docker", "run", "--rm", "-v", stateDir+":/s",
			"alpine", "rm", "-rf", "/s/services").Run()
	})

	inst, err := m.Install(ctx, "livemysql",
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	grants, err := s.GetServiceGrants(inst.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants = %v, err %v", grants, err)
	}
	dbName := grants[0].DBName

	if !mysqlDatabaseExists(t, dbName) {
		t.Fatalf("database %q not found in the live MySQL", dbName)
	}
	t.Logf("provisioned database %q present in live MySQL", dbName)

	if !mysqlUserCanConnect(t, grants[0].RoleName, grants[0].Password, dbName) {
		t.Fatalf("user %q could not connect to %q with injected password", grants[0].RoleName, dbName)
	}
	t.Logf("user %q connects to %q with the injected credentials", grants[0].RoleName, dbName)

	if err := m.Uninstall(ctx, inst.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if mysqlDatabaseExists(t, dbName) {
		t.Fatalf("database %q survived uninstall", dbName)
	}
	t.Logf("database %q dropped on uninstall", dbName)
}

// mysqlDatabaseExists reports whether a database is present in the live
// container, querying as root via the in-container password env.
func mysqlDatabaseExists(t *testing.T, dbName string) bool {
	t.Helper()
	out, _ := exec.Command("docker", "exec", serviceContainerName("mysql", "8.0"),
		"sh", "-c", `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -N -e "$1"`, "sh",
		"SELECT 1 FROM information_schema.schemata WHERE schema_name='"+dbName+"'").CombinedOutput()
	return strings.TrimSpace(string(out)) == "1"
}

// mysqlUserCanConnect verifies the provisioned user authenticates against its
// DB over TCP with the injected password, exercising the real DSN.
func mysqlUserCanConnect(t *testing.T, user, password, dbName string) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("docker", "exec", "-e", "MYSQL_PWD="+password,
			serviceContainerName("mysql", "8.0"),
			"mysql", "-h127.0.0.1", "--protocol=TCP", "-u"+user, "-D", dbName, "-N", "-e", "SELECT 1").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return true
		}
		if time.Now().After(deadline) {
			t.Logf("connect attempt output: %s", out)
			return false
		}
		time.Sleep(time.Second)
	}
}

// TestLiveKanBoot installs the real `kan` catalog app against a freshly
// provisioned Postgres and asserts it boots — the concrete goal the managed-
// service slice unblocks. Pulls kan's images, so it's the heaviest live test.
//
//	go test ./internal/lifecycle/ -tags dockerlive -run TestLiveKanBoot -v -timeout 600s
//
// Un-skipped by #92: the override no longer force-restarts kan's one-shot
// `migrate` job, so the `web` service's service_completed_successfully gate
// fires and `compose up -d` returns. The managed-Postgres path this slice owns
// is covered by TestLivePostgresProvisioning above.
func TestLiveKanBoot(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	s, err := store.Open(filepath.Join(stateDir, "molma.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Point the catalog at the repo's catalog/ dir (../../catalog from here).
	repoCatalog, err := filepath.Abs(filepath.Join("..", "..", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	docker := NewCLIDocker()
	m := NewManager(s, catalog.New(repoCatalog), newFakeHost(), newFakeCaddy(), docker, events.NewBus(), stateDir)
	m.SetAdmitter(admission.Check)
	if err := docker.NetworkCreate(ctx, ingressNetwork, false); err != nil {
		t.Fatalf("ingress net: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", serviceContainerName("postgres", "15")).Run()
		_ = exec.Command("docker", "network", "rm", serviceNetworkName("postgres", "15")).Run()
		_ = exec.Command("docker", "network", "rm", ingressNetwork).Run()
		_ = exec.Command("docker", "run", "--rm", "-v", stateDir+":/s",
			"alpine", "rm", "-rf", "/s/services").Run()
	})

	inst, err := m.Install(ctx, "kan",
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, nil)
	if err != nil {
		t.Fatalf("install kan: %v", err)
	}
	if inst.State != "running" {
		t.Fatalf("kan end state = %q, want running", inst.State)
	}
	t.Logf("kan booted against provisioned Postgres (instance %s)", inst.ID)

	// The DSN was injected, and kan's migrate job ran against the DB (a kan table
	// exists in the provisioned database).
	grants, _ := s.GetServiceGrants(inst.ID)
	if len(grants) != 1 {
		t.Fatalf("grants = %v", grants)
	}
	if !pgHasUserTables(t, grants[0].DBName) {
		t.Fatalf("no tables in %q — kan migrate did not run against the provisioned DB", grants[0].DBName)
	}
	t.Logf("kan migrate populated the provisioned database %q", grants[0].DBName)

	if err := m.Uninstall(ctx, inst.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
}

// pgHasUserTables reports whether the named DB has any non-system tables.
func pgHasUserTables(t *testing.T, dbName string) bool {
	t.Helper()
	out, _ := exec.Command("docker", "exec", serviceContainerName("postgres", "15"),
		"psql", "-U", "postgres", "-d", dbName, "-tAc",
		"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").CombinedOutput()
	return strings.TrimSpace(string(out)) != "" && strings.TrimSpace(string(out)) != "0"
}

func writeLiveCatalogApp(t *testing.T, catDir, id, compose, man string) {
	t.Helper()
	dir := filepath.Join(catDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pgDatabaseExists reports whether a database is present in the live container.
func pgDatabaseExists(t *testing.T, dbName string) bool {
	t.Helper()
	out, _ := exec.Command("docker", "exec", serviceContainerName("postgres", "15"),
		"psql", "-U", "postgres", "-tAc",
		"SELECT 1 FROM pg_database WHERE datname='"+dbName+"'").CombinedOutput()
	return strings.TrimSpace(string(out)) == "1"
}

// pgRoleCanConnect verifies the provisioned role authenticates against its DB
// over TCP with the injected password (PGPASSWORD), exercising the real DSN.
func pgRoleCanConnect(t *testing.T, role, password, dbName string) bool {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-e", "PGPASSWORD="+password,
		serviceContainerName("postgres", "15"),
		"psql", "-h", "127.0.0.1", "-U", role, "-d", dbName, "-tAc", "SELECT 1")
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return true
		}
		if time.Now().After(deadline) {
			t.Logf("connect attempt output: %s", out)
			return false
		}
		time.Sleep(time.Second)
		cmd = exec.Command("docker", "exec", "-e", "PGPASSWORD="+password,
			serviceContainerName("postgres", "15"),
			"psql", "-h", "127.0.0.1", "-U", role, "-d", dbName, "-tAc", "SELECT 1")
	}
}
