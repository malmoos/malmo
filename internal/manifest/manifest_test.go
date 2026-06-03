package manifest

import (
	"strings"
	"testing"
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
	m, _, err := Synthesize("My App", compose, "", 8080)
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
	_, _, err := Synthesize("App", compose, "", 80)
	if err == nil || !strings.Contains(err.Error(), "main service") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
}

func TestSynthesizeBadMainServiceRejected(t *testing.T) {
	compose := []byte(`
services:
  a: {image: x}
`)
	_, _, err := Synthesize("App", compose, "ghost", 80)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want missing-service error, got %v", err)
	}
}

func TestSynthesizeEmptyName(t *testing.T) {
	_, _, err := Synthesize("   ", []byte(`services: {a: {image: x}}`), "", 80)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want name error, got %v", err)
	}
}

func TestSynthesizeMissingPort(t *testing.T) {
	_, _, err := Synthesize("App", []byte(`services: {a: {image: x}}`), "", 0)
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("want port error, got %v", err)
	}
}

func TestSynthesizeUnusableSlugName(t *testing.T) {
	_, _, err := Synthesize("!!!", []byte(`services: {a: {image: x}}`), "", 80)
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("want slug error, got %v", err)
	}
}

func TestSynthesizeNoServicesInCompose(t *testing.T) {
	_, _, err := Synthesize("App", []byte(`services: {}`), "", 80)
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InferMainPort([]byte(c.compose), c.main); got != c.want {
				t.Fatalf("InferMainPort = %d, want %d", got, c.want)
			}
		})
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
