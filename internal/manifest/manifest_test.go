package manifest

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseHappy(t *testing.T) {
	src := []byte(`
id: whoami
manifest_version: 1
name: Whoami
version: "1.10"
compose_file: compose.yml
main_service: whoami
main_port: 80
`)
	m, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.ID != "whoami" || m.MainPort != 80 {
		t.Fatalf("parse: %+v", m)
	}
}

func TestParseSecretsNormalizesBytes(t *testing.T) {
	src := []byte(`
id: kan
manifest_version: 1
name: Kan
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 3000
secrets:
  - name: auth
  - name: weak_key
    bytes: 4
  - name: big
    bytes: 64
`)
	m, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]int{"auth": DefaultSecretBytes, "weak_key": MinSecretBytes, "big": 64}
	for _, s := range m.Secrets {
		if want[s.Name] != s.Bytes {
			t.Errorf("secret %q: bytes = %d, want %d", s.Name, s.Bytes, want[s.Name])
		}
	}
}

func TestParseRejectsBadSecrets(t *testing.T) {
	cases := map[string]string{
		"bad name":      "- name: Auth",
		"dup names":     "- name: auth\n  - name: auth",
		"leading digit": "- name: 2fa",
		"hyphen":        "- name: auth-key",
	}
	for label, block := range cases {
		src := []byte(`
id: kan
manifest_version: 1
name: Kan
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 3000
secrets:
  ` + block + `
`)
		if _, err := Parse(src); err == nil {
			t.Errorf("%s: Parse accepted invalid secrets, want error", label)
		}
	}
}

func TestParseServicesHappy(t *testing.T) {
	src := []byte(`
id: kan
manifest_version: 1
name: Kan
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 3000
services:
  database:
    type: postgres
    version: "15"
  cache:
    type: redis
    version: "7"
    name: kan_cache
`)
	m, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := m.Services["database"]; got.Type != "postgres" || got.Version != "15" {
		t.Errorf("database = %+v", got)
	}
	// Redis parses (schema-valid) even though v1 doesn't provision it yet.
	if got := m.Services["cache"]; got.Type != "redis" || got.Version != "7" {
		t.Errorf("cache = %+v", got)
	}
}

func TestParseServicesMySQLFamily(t *testing.T) {
	for _, dep := range []struct{ typ, version string }{
		{"mysql", "8.0"}, {"mysql", "8.4"}, {"mariadb", "10.11"}, {"mariadb", "11.4"},
	} {
		src := []byte(`
id: ghost
manifest_version: 1
name: Ghost
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 2368
services:
  database:
    type: ` + dep.typ + `
    version: "` + dep.version + `"
`)
		m, err := Parse(src)
		if err != nil {
			t.Fatalf("%s %s: parse: %v", dep.typ, dep.version, err)
		}
		if got := m.Services["database"]; got.Type != dep.typ || got.Version != dep.version {
			t.Errorf("%s %s: database = %+v", dep.typ, dep.version, got)
		}
	}
}

func TestParseRejectsBadServices(t *testing.T) {
	cases := map[string]string{
		"unknown type":    "database:\n    type: mongodb\n    version: \"7\"",
		"bad pg version":  "database:\n    type: postgres\n    version: \"13\"",
		"bad redis ver":   "cache:\n    type: redis\n    version: \"6\"",
		"bad mysql ver":   "database:\n    type: mysql\n    version: \"5.7\"",
		"major-only ver":  "database:\n    type: mysql\n    version: \"8\"",
		"bad mariadb ver": "database:\n    type: mariadb\n    version: \"10.6\"",
		"missing version": "database:\n    type: postgres",
		"bad key":         "My_DB:\n    type: postgres\n    version: \"15\"",
	}
	for label, block := range cases {
		src := []byte(`
id: kan
manifest_version: 1
name: Kan
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 3000
services:
  ` + block + `
`)
		if _, err := Parse(src); err == nil {
			t.Errorf("%s: Parse accepted invalid services, want error", label)
		}
	}
}

func TestParseRejectsNonKebabSlugs(t *testing.T) {
	// Each slug must stay parseable in the `<slug>--<user>` scheme
	// (DASHBOARD.md # instance naming).
	for _, bad := range []string{"whoami-", "-whoami", "who--ami", "xn--abc", "Whoami", "who_ami"} {
		src := []byte(`
id: ` + bad + `
manifest_version: 1
name: Whoami
version: "1.10"
compose_file: compose.yml
main_service: whoami
main_port: 80
`)
		if _, err := Parse(src); err == nil {
			t.Fatalf("slug %q accepted, want rejection", bad)
		}
	}
}

func TestParseRejectsMissingFields(t *testing.T) {
	base := map[string]string{
		"id":               "whoami",
		"manifest_version": "1",
		"name":             "Whoami",
		"version":          `"1.0"`,
		"compose_file":     "compose.yml",
		"main_service":     "whoami",
		"main_port":        "80",
	}
	for omit := range base {
		t.Run("missing_"+omit, func(t *testing.T) {
			var sb strings.Builder
			for k, v := range base {
				if k == omit {
					continue
				}
				sb.WriteString(k)
				sb.WriteString(": ")
				sb.WriteString(v)
				sb.WriteString("\n")
			}
			_, err := Parse([]byte(sb.String()))
			if err == nil {
				t.Fatalf("want error for missing %s, got nil", omit)
			}
			if !strings.Contains(err.Error(), omit) {
				t.Fatalf("error %q doesn't name field %q", err.Error(), omit)
			}
		})
	}
}

func TestParseRejectsUnsupportedManifestVersion(t *testing.T) {
	src := []byte(`
id: x
manifest_version: 99
name: X
version: "1"
compose_file: c.yml
main_service: x
main_port: 80
`)
	_, err := Parse(src)
	if err == nil || !strings.Contains(err.Error(), "manifest_version") {
		t.Fatalf("want manifest_version error, got %v", err)
	}
}

func TestSynthesizeSingleServiceInferred(t *testing.T) {
	compose := []byte(`
services:
  only:
    image: nginx
`)
	m, _, err := Synthesize("My App", compose, "", 8080, Permissions{Internet: true})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if m.MainService != "only" {
		t.Fatalf("MainService = %q, want %q", m.MainService, "only")
	}
	if !strings.HasPrefix(m.ID, "my-app-") {
		t.Fatalf("ID = %q, want my-app-<entropy>", m.ID)
	}
	if m.PreferredSlugs[0] != "my-app" {
		t.Fatalf("PreferredSlugs = %v", m.PreferredSlugs)
	}
	if !m.Permissions.Internet {
		t.Fatalf("custom apps default internet=on, got %+v", m.Permissions)
	}
}

func TestSynthesizeAmbiguousServiceRejected(t *testing.T) {
	compose := []byte(`
services:
  a: {image: x}
  b: {image: y}
`)
	_, _, err := Synthesize("App", compose, "", 80, Permissions{Internet: true})
	if err == nil || !strings.Contains(err.Error(), "main service") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
}

func TestSynthesizeBadMainServiceRejected(t *testing.T) {
	compose := []byte(`
services:
  a: {image: x}
`)
	_, _, err := Synthesize("App", compose, "ghost", 80, Permissions{Internet: true})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want missing-service error, got %v", err)
	}
}

func TestSynthesizeEmptyName(t *testing.T) {
	_, _, err := Synthesize("   ", []byte(`services: {a: {image: x}}`), "", 80, Permissions{Internet: true})
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want name error, got %v", err)
	}
}

func TestSynthesizeMissingPort(t *testing.T) {
	_, _, err := Synthesize("App", []byte(`services: {a: {image: x}}`), "", 0, Permissions{Internet: true})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("want port error, got %v", err)
	}
}

func TestSynthesizeUnusableSlugName(t *testing.T) {
	_, _, err := Synthesize("!!!", []byte(`services: {a: {image: x}}`), "", 80, Permissions{Internet: true})
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("want slug error, got %v", err)
	}
}

func TestSynthesizeNoServicesInCompose(t *testing.T) {
	_, _, err := Synthesize("App", []byte(`services: {}`), "", 80, Permissions{Internet: true})
	if err == nil {
		t.Fatalf("want error, got nil")
	}
}

func TestInferMainPort(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		main    string
		want    int
	}{
		{
			name: "single expose as string",
			compose: `
services:
  web:
    image: nginx
    expose: ["8080"]
`,
			main: "web", want: 8080,
		},
		{
			name: "single expose as int",
			compose: `
services:
  web:
    image: nginx
    expose:
      - 3000
`,
			main: "web", want: 3000,
		},
		{
			name: "reads only the named main service",
			compose: `
services:
  web:
    image: nginx
    expose: ["80"]
  db:
    image: postgres
    expose: ["5432"]
`,
			main: "db", want: 5432,
		},
		{
			name: "no expose declared → ask",
			compose: `
services:
  web:
    image: nginx
`,
			main: "web", want: 0,
		},
		{
			name: "several exposed ports are ambiguous → ask",
			compose: `
services:
  web:
    image: nginx
    expose: ["80", "443"]
`,
			main: "web", want: 0,
		},
		{
			name: "non-numeric expose (range) → ask",
			compose: `
services:
  web:
    image: nginx
    expose: ["8000-8005"]
`,
			main: "web", want: 0,
		},
		{
			name: "out-of-range port → ask",
			compose: `
services:
  web:
    image: nginx
    expose: ["70000"]
`,
			main: "web", want: 0,
		},
		{
			name:    "unknown main service → 0",
			compose: `services: {web: {image: nginx, expose: ["80"]}}`,
			main:    "ghost", want: 0,
		},
		{
			name:    "invalid YAML → 0, never panics",
			compose: `:::not yaml`,
			main:    "web", want: 0,
		},
		// ports: container-side mining (the mapping itself is admission-rejected;
		// we read its container side for the prefill only — DASHBOARD.md # Main port).
		{
			name:    "ports host:container → container side",
			compose: `services: {web: {image: nginx, ports: ["8080:80"]}}`,
			main:    "web", want: 80,
		},
		{
			name:    "ports with bind ip → container side",
			compose: `services: {web: {image: nginx, ports: ["127.0.0.1:8080:80"]}}`,
			main:    "web", want: 80,
		},
		{
			name:    "ports container-only → that port",
			compose: `services: {web: {image: nginx, ports: ["80"]}}`,
			main:    "web", want: 80,
		},
		{
			name:    "ports with proto suffix → stripped",
			compose: `services: {web: {image: nginx, ports: ["8080:80/tcp"]}}`,
			main:    "web", want: 80,
		},
		{
			name: "ports long syntax → target",
			compose: `
services:
  web:
    image: nginx
    ports:
      - target: 80
        published: 8080
`,
			main: "web", want: 80,
		},
		{
			name:    "expose preferred over ports",
			compose: `services: {web: {image: nginx, expose: ["80"], ports: ["9000:443"]}}`,
			main:    "web", want: 80,
		},
		{
			name:    "ports range → ask",
			compose: `services: {web: {image: nginx, ports: ["3000-3005:3000-3005"]}}`,
			main:    "web", want: 0,
		},
		{
			name:    "several ports are ambiguous → ask",
			compose: `services: {web: {image: nginx, ports: ["80:80", "443:443"]}}`,
			main:    "web", want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InferMainPort([]byte(c.compose), c.main); got != c.want {
				t.Fatalf("InferMainPort = %d, want %d", got, c.want)
			}
		})
	}
}

func TestParseFoldersTargetValidation(t *testing.T) {
	// A Door-2 grant's explicit in-container destination must be an absolute path
	// with no traversal; store grants omit it (DASHBOARD.md # Folder grants).
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, target: /photoprism/originals }\n")); err != nil {
		t.Fatalf("absolute target rejected: %v", err)
	}
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, target: data/photos }\n")); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("want relative-target error, got %v", err)
	}
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, target: /data/../etc }\n")); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("want traversal-target error, got %v", err)
	}
}

func TestPermissionsOverlayRoundTrip(t *testing.T) {
	in := Permissions{
		Internet: true,
		GPU:      true,
		Devices:  []string{"/dev/dri"},
		Folders:  []Folder{{Folder: "photos", Mode: "write", Scope: "whole", Target: "/photoprism/originals"}},
	}
	y, err := RenderPermissionsOverlay(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := ParsePermissionsOverlay(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Internet || !got.GPU || len(got.Devices) != 1 || len(got.Folders) != 1 {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
	if f := got.Folders[0]; f.Folder != "photos" || f.Mode != "write" || f.Target != "/photoprism/originals" {
		t.Fatalf("folder round-trip wrong: %+v", f)
	}
}

func TestParsePermissionsOverlayValidatesAndCoaches(t *testing.T) {
	// Empty overlay → empty (all-off) permission set, no error.
	if p, err := ParsePermissionsOverlay([]byte("   ")); err != nil || p.Internet {
		t.Fatalf("empty overlay = (%+v, %v), want zero perms, no error", p, err)
	}
	// Same validation gate as the form path: a relative folder target is rejected.
	bad := []byte("permissions:\n  folders:\n    - { folder: photos, target: rel/path }\n")
	if _, err := ParsePermissionsOverlay(bad); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("want target error, got %v", err)
	}
	// An unknown key surfaces instead of silently reading as false (typo coaching).
	if _, err := ParsePermissionsOverlay([]byte("permissions:\n  interent: true\n")); err == nil {
		t.Fatalf("want unknown-field error for typo'd key, got nil")
	}
	// Not valid YAML → a clear parse error.
	if _, err := ParsePermissionsOverlay([]byte(":::not yaml")); err == nil {
		t.Fatalf("want YAML error, got nil")
	}
}

// withPerms wraps a permissions block in an otherwise-valid manifest so the
// folder tests exercise Parse end-to-end.
func withPerms(perms string) []byte {
	return []byte(`
id: app
manifest_version: 1
name: App
version: "1"
compose_file: compose.yml
main_service: app
main_port: 80
permissions:
` + perms)
}

func TestParseFoldersDefaults(t *testing.T) {
	// mode defaults to read, scope to whole; devices/gpu parse.
	m, err := Parse(withPerms(`
  gpu: true
  devices: [/dev/ttyUSB0, /dev/dri]
  folders:
    - { folder: photos }
    - { folder: notes, mode: write, scope: pick-subfolder, default: Notes/Obsidian }
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.Permissions.GPU || len(m.Permissions.Devices) != 2 {
		t.Fatalf("gpu/devices not parsed: %+v", m.Permissions)
	}
	photos := m.Permissions.Folders[0]
	if photos.Mode != "read" || photos.Scope != "whole" {
		t.Fatalf("defaults not applied: %+v", photos)
	}
	notes := m.Permissions.Folders[1]
	if notes.Mode != "write" || notes.Scope != "pick-subfolder" || notes.Default != "Notes/Obsidian" {
		t.Fatalf("notes folder parsed wrong: %+v", notes)
	}
}

func TestParseFoldersRejectsUnknownName(t *testing.T) {
	_, err := Parse(withPerms("  folders:\n    - { folder: secrets }\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown folder") {
		t.Fatalf("want unknown-folder error, got %v", err)
	}
}

func TestParseFoldersRejectsBadModeAndScope(t *testing.T) {
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, mode: delete }\n")); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("want mode error, got %v", err)
	}
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, scope: some }\n")); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("want scope error, got %v", err)
	}
}

func TestParseFoldersRejectsDefaultMisuse(t *testing.T) {
	// default only valid with pick-subfolder; no traversal/absolute paths.
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, default: Sub }\n")); err == nil || !strings.Contains(err.Error(), "default is only valid") {
		t.Fatalf("want default-with-whole error, got %v", err)
	}
	if _, err := Parse(withPerms("  folders:\n    - { folder: photos, scope: pick-subfolder, default: ../etc }\n")); err == nil || !strings.Contains(err.Error(), "relative subpath") {
		t.Fatalf("want traversal error, got %v", err)
	}
}

// withManifest wraps extra top-level YAML in an otherwise-valid manifest so the
// health_probe tests exercise Parse end-to-end.
func withManifest(extra string) []byte {
	return []byte(`id: app
manifest_version: 1
name: App
version: "1"
compose_file: compose.yml
main_service: app
main_port: 80
` + extra)
}

func TestParseHealthProbeAbsentIsNil(t *testing.T) {
	m, err := Parse(withManifest(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.HealthProbe != nil {
		t.Fatalf("absent health_probe must parse to nil, got %+v", m.HealthProbe)
	}
}

// Shorthand `health_probe: /healthz` expands to the object {path: /healthz},
// with the start_period default applied and healthy_status left empty (any <500).
func TestParseHealthProbeShorthand(t *testing.T) {
	m, err := Parse(withManifest("health_probe: /healthz\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.HealthProbe == nil {
		t.Fatal("health_probe must parse to a non-nil object")
	}
	if m.HealthProbe.Path != "/healthz" {
		t.Errorf("Path = %q, want /healthz", m.HealthProbe.Path)
	}
	if m.HealthProbe.StartPeriod != DefaultStartPeriod {
		t.Errorf("StartPeriod = %s, want default %s", m.HealthProbe.StartPeriod, DefaultStartPeriod)
	}
	if len(m.HealthProbe.HealthyStatus) != 0 {
		t.Errorf("HealthyStatus = %v, want empty (default any <500)", m.HealthProbe.HealthyStatus)
	}
}

func TestParseHealthProbeFullForm(t *testing.T) {
	m, err := Parse(withManifest("health_probe:\n  path: /up\n  healthy_status: [200, 204]\n  start_period: 30s\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := m.HealthProbe
	if p == nil || p.Path != "/up" {
		t.Fatalf("Path not parsed: %+v", p)
	}
	if !reflect.DeepEqual(p.HealthyStatus, []int{200, 204}) {
		t.Errorf("HealthyStatus = %v, want [200 204]", p.HealthyStatus)
	}
	if p.StartPeriod != 30*time.Second {
		t.Errorf("StartPeriod = %s, want 30s", p.StartPeriod)
	}
}

func TestParseHealthProbeRejectsBadPath(t *testing.T) {
	// Empty and relative paths are rejected — the probe GETs an absolute path
	// through the app's Caddy route.
	for _, bad := range []string{"healthz", `""`} {
		if _, err := Parse(withManifest("health_probe:\n  path: " + bad + "\n")); err == nil || !strings.Contains(err.Error(), "path") {
			t.Errorf("path %q accepted, want rejection (err=%v)", bad, err)
		}
	}
}

func TestParseHealthProbeRejectsBadStatus(t *testing.T) {
	if _, err := Parse(withManifest("health_probe:\n  path: /healthz\n  healthy_status: [200, 999]\n")); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("want healthy_status error, got %v", err)
	}
}

func TestParseHealthProbeRejectsBadDuration(t *testing.T) {
	if _, err := Parse(withManifest("health_probe:\n  path: /healthz\n  start_period: soon\n")); err == nil || !strings.Contains(err.Error(), "start_period") {
		t.Fatalf("want start_period parse error, got %v", err)
	}
}

// The probe must survive the marshal→parse round-trip that writeInstanceDir /
// loadInstanceManifest does (the on-disk manifest.yml). start_period in
// particular must re-parse from its duration-string form, not raw nanoseconds.
func TestHealthProbeRoundTrip(t *testing.T) {
	orig, err := Parse(withManifest("health_probe:\n  path: /up\n  healthy_status: [200]\n  start_period: 45s\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse marshaled manifest: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(got.HealthProbe, orig.HealthProbe) {
		t.Fatalf("round-trip changed probe:\n got  %+v\n want %+v", got.HealthProbe, orig.HealthProbe)
	}
}
