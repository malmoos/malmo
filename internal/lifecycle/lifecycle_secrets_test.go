package lifecycle

// Per-app secret generation + injection (SERVICE_PROVISIONING.md # Env-var
// injection): the brain generates a CSPRNG value for each declared secret,
// persists it, and re-emits it as MALMO_SECRET_<NAME> in the instance .env.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/malmoos/malmo/internal/manifest"
	"github.com/malmoos/malmo/internal/store"
)

const secretsManifest = `
id: secretapp
manifest_version: 1
name: Secret App
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 8080
preferred_slugs: [secretapp]
permissions:
  internet: false
  lan: false
secrets:
  - name: auth
`

const secretsCompose = `
services:
  app:
    image: traefik/whoami:v1.10.3
`

// installSecretApp installs secretapp and returns the instance plus its .env.
func installSecretApp(t *testing.T, e *testEnv) (store.Instance, string) {
	t.Helper()
	e.writeCatalogApp(t, "secretapp", secretsCompose, secretsManifest)
	e.docker.digests[testImage] = testDigest
	inst, err := e.m.Install(context.Background(), "secretapp",
		Owner{UserID: "u_admin", Username: "admin"}, store.ScopeHousehold, nil, "", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(e.stateDir, "instances", inst.ID, ".env"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	return inst, string(env)
}

func TestInstallInjectsGeneratedSecret(t *testing.T) {
	e := newTestEnv(t)
	inst, env := installSecretApp(t, e)

	// The .env carries MALMO_SECRET_AUTH with a non-empty generated value.
	val := envValue(env, "MALMO_SECRET_AUTH")
	if val == "" {
		t.Fatalf("env missing non-empty MALMO_SECRET_AUTH, got:\n%s", env)
	}
	// It matches what was persisted (so re-emit is store-backed, not re-rolled).
	secrets, err := e.store.GetInstanceSecrets(inst.ID)
	if err != nil {
		t.Fatalf("get secrets: %v", err)
	}
	if len(secrets) != 1 || secrets[0].Name != "auth" || secrets[0].Value != val {
		t.Fatalf("persisted secrets %v don't match env value %q", secrets, val)
	}
}

func TestSecretsAreUniquePerInstance(t *testing.T) {
	// Separate envs: instance IDs are second-resolution timestamps, so two
	// installs in one env within the same second would collide on the PK.
	_, env1 := installSecretApp(t, newTestEnv(t))
	_, env2 := installSecretApp(t, newTestEnv(t))
	if v1, v2 := envValue(env1, "MALMO_SECRET_AUTH"), envValue(env2, "MALMO_SECRET_AUTH"); v1 == v2 {
		t.Fatalf("two installs reused the same secret %q", v1)
	}
}

func TestGenerateSecretsEntropy(t *testing.T) {
	decls := []manifest.Secret{{Name: "auth", Bytes: 32}, {Name: "k", Bytes: 16}}
	got, err := generateSecrets(decls)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 secrets, got %d", len(got))
	}
	// base64url(no pad) of 32 bytes is 43 chars, of 16 bytes is 22.
	if l := len(got[0].Value); l != 43 {
		t.Errorf("32-byte secret encoded to %d chars, want 43", l)
	}
	if l := len(got[1].Value); l != 22 {
		t.Errorf("16-byte secret encoded to %d chars, want 22", l)
	}
}

// envValue pulls the value of KEY=... from a .env blob, or "" if absent.
func envValue(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}
