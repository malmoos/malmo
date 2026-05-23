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
