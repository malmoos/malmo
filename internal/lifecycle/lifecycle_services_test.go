package lifecycle

// Managed-service provisioning (SERVICE_PROVISIONING.md # Tier 1): an app that
// declares `services.database: {type: postgres}` gets a per-app database+role in
// the shared Postgres instance, with credentials injected as
// MOLMA_SERVICE_DATABASE_*. Driven against the fake docker driver (whose Exec
// default succeeds — pg_isready ready, psql CREATE ok).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/molmaos/molma/internal/store"
)

const dbManifest = `
id: dbapp
manifest_version: 1
name: DB App
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 8080
preferred_slugs: [dbapp]
services:
  database:
    type: postgres
    version: "15"
permissions:
  internet: false
  lan: false
`

const dbCompose = `
services:
  app:
    image: traefik/whoami:v1.10.3
    environment:
      POSTGRES_URL: ${MOLMA_SERVICE_DATABASE_DSN}
`

// installDBApp installs dbapp (manifest id overridable so two can coexist in one
// env) and returns the instance.
func installDBApp(t *testing.T, e *testEnv, id string) store.Instance {
	t.Helper()
	return installDBAppKind(t, e, id, "postgres", "15")
}

// installDBAppKind is installDBApp with the declared service type/version
// overridable, so the MySQL-family tests share the same fixture.
func installDBAppKind(t *testing.T, e *testEnv, id, kind, version string) store.Instance {
	t.Helper()
	man := strings.Replace(dbManifest, "id: dbapp", "id: "+id, 1)
	man = strings.Replace(man, "preferred_slugs: [dbapp]", "preferred_slugs: ["+id+"]", 1)
	man = strings.Replace(man, "type: postgres", "type: "+kind, 1)
	man = strings.Replace(man, `version: "15"`, `version: "`+version+`"`, 1)
	e.writeCatalogApp(t, id, dbCompose, man)
	e.docker.digests[testImage] = testDigest
	inst, err := e.m.Install(context.Background(), id,
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err != nil {
		t.Fatalf("install %s: %v", id, err)
	}
	return inst
}

// hasCall reports whether any recorded docker call's flattened form contains sub.
func (f *fakeDocker) hasCall(sub string) bool {
	for _, c := range f.Calls() {
		if strings.Contains(c.String(), sub) {
			return true
		}
	}
	return false
}

func (f *fakeDocker) countMethod(name string) int {
	n := 0
	for _, m := range f.methods() {
		if m == name {
			n++
		}
	}
	return n
}

func TestInstallProvisionsPostgres(t *testing.T) {
	e := newTestEnv(t)
	inst := installDBApp(t, e, "dbapp")

	// A shared Postgres-15 service instance was recorded and started.
	if _, err := e.store.GetServiceInstance("postgres", "15"); err != nil {
		t.Fatalf("service instance not recorded: %v", err)
	}
	if !e.docker.hasCall("ServiceUp(") {
		t.Fatalf("ServiceUp never called; calls: %v", e.docker.Calls())
	}

	// A per-app grant was persisted.
	grants, err := e.store.GetServiceGrants(inst.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants = %v, err %v", grants, err)
	}
	g := grants[0]
	if g.LogicalName != "database" || g.Kind != "postgres" || g.Version != "15" {
		t.Fatalf("grant = %+v", g)
	}

	// The brain issued CREATE ROLE / CREATE DATABASE via docker-exec psql.
	if !e.docker.hasCall("CREATE DATABASE") {
		t.Fatalf("no provisioning psql call; calls: %v", e.docker.Calls())
	}

	// The .env carries the injected credential family.
	env := readInstanceEnv(t, e, inst.ID)
	dsn := envValue(env, "MOLMA_SERVICE_DATABASE_DSN")
	wantHost := "postgres-15.molma.internal"
	if !strings.HasPrefix(dsn, "postgres://") || !strings.Contains(dsn, wantHost) {
		t.Fatalf("DSN = %q, want postgres:// … %s", dsn, wantHost)
	}
	if got := envValue(env, "MOLMA_SERVICE_DATABASE_HOST"); got != wantHost {
		t.Fatalf("HOST = %q, want %q", got, wantHost)
	}
	if got := envValue(env, "MOLMA_SERVICE_DATABASE_NAME"); got != g.DBName {
		t.Fatalf("NAME = %q, want %q", got, g.DBName)
	}

	// The app service is attached to the service network in the override.
	override := readInstanceFile(t, e, inst.ID, "compose.override.yml")
	if !strings.Contains(override, "molma-svc-postgres-15") {
		t.Fatalf("override missing service network:\n%s", override)
	}
}

func TestSecondAppReusesServiceInstance(t *testing.T) {
	e := newTestEnv(t)
	installDBApp(t, e, "dbappa")
	upsAfterFirst := e.docker.countMethod("ServiceUp")
	installDBApp(t, e, "dbappb")

	// The shared Postgres instance is spun up once, not per app: the second
	// install finds the existing service_instances row and skips ServiceUp.
	if got := e.docker.countMethod("ServiceUp"); got != upsAfterFirst {
		t.Fatalf("ServiceUp called %d times total, want %d (reuse)", got, upsAfterFirst)
	}
}

func TestUninstallDropsServiceDB(t *testing.T) {
	e := newTestEnv(t)
	inst := installDBApp(t, e, "dbapp")
	grants, _ := e.store.GetServiceGrants(inst.ID)
	dbName := grants[0].DBName

	if err := e.m.Uninstall(context.Background(), inst.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !e.docker.hasCall("DROP DATABASE") || !e.docker.hasCall(dbName) {
		t.Fatalf("uninstall did not drop %s; calls: %v", dbName, e.docker.Calls())
	}
}

func TestInstallProvisionsMySQLFamily(t *testing.T) {
	for _, tc := range []struct {
		kind, version, stem, client string
	}{
		// The version dot folds to a dash in every derived name (compose project
		// names reject dots); mariadb provisions via its own-named client binary.
		{"mysql", "8.0", "mysql-8-0", "mysql -uroot"},
		{"mariadb", "11.4", "mariadb-11-4", "mariadb -uroot"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e := newTestEnv(t)
			inst := installDBAppKind(t, e, "dbapp", tc.kind, tc.version)

			if _, err := e.store.GetServiceInstance(tc.kind, tc.version); err != nil {
				t.Fatalf("service instance not recorded: %v", err)
			}
			if !e.docker.hasCall("ServiceUp(") {
				t.Fatalf("ServiceUp never called; calls: %v", e.docker.Calls())
			}

			grants, err := e.store.GetServiceGrants(inst.ID)
			if err != nil || len(grants) != 1 {
				t.Fatalf("grants = %v, err %v", grants, err)
			}
			g := grants[0]
			if g.Kind != tc.kind || g.Version != tc.version {
				t.Fatalf("grant = %+v", g)
			}

			// Provisioning ran the engine's own client with CREATE DATABASE +
			// CREATE USER + GRANT.
			if !e.docker.hasCall(tc.client) || !e.docker.hasCall("CREATE DATABASE") ||
				!e.docker.hasCall("CREATE USER") || !e.docker.hasCall("GRANT ALL PRIVILEGES") {
				t.Fatalf("no MySQL provisioning call; calls: %v", e.docker.Calls())
			}

			// The injected family carries the dot-folded host, port 3306, and a
			// mysql:// DSN for both engines (one wire protocol).
			env := readInstanceEnv(t, e, inst.ID)
			wantHost := tc.stem + ".molma.internal"
			if got := envValue(env, "MOLMA_SERVICE_DATABASE_HOST"); got != wantHost {
				t.Fatalf("HOST = %q, want %q", got, wantHost)
			}
			if got := envValue(env, "MOLMA_SERVICE_DATABASE_PORT"); got != "3306" {
				t.Fatalf("PORT = %q, want 3306", got)
			}
			dsn := envValue(env, "MOLMA_SERVICE_DATABASE_DSN")
			if !strings.HasPrefix(dsn, "mysql://") || !strings.Contains(dsn, wantHost+":3306/"+g.DBName) {
				t.Fatalf("DSN = %q, want mysql:// … %s:3306/%s", dsn, wantHost, g.DBName)
			}

			override := readInstanceFile(t, e, inst.ID, "compose.override.yml")
			if !strings.Contains(override, "molma-svc-"+tc.stem) {
				t.Fatalf("override missing service network:\n%s", override)
			}
		})
	}
}

func TestUninstallDropsMySQLDB(t *testing.T) {
	e := newTestEnv(t)
	inst := installDBAppKind(t, e, "dbapp", "mysql", "8.0")
	grants, _ := e.store.GetServiceGrants(inst.ID)
	dbName := grants[0].DBName

	if err := e.m.Uninstall(context.Background(), inst.ID); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !e.docker.hasCall("DROP DATABASE") || !e.docker.hasCall("DROP USER") || !e.docker.hasCall(dbName) {
		t.Fatalf("uninstall did not drop %s; calls: %v", dbName, e.docker.Calls())
	}
}

func readInstanceEnv(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	return readInstanceFile(t, e, id, ".env")
}

func readInstanceFile(t *testing.T, e *testEnv, id, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.stateDir, "instances", id, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
