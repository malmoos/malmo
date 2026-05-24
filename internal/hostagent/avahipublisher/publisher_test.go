package avahipublisher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPublisher(t *testing.T) *FilePublisher {
	t.Helper()
	return &FilePublisher{
		Dir:        t.TempDir(),
		HostSuffix: ".malmo.local",
	}
}

// TestPublish_WritesExpectedXML checks the full file content for a known slug.
func TestPublish_WritesExpectedXML(t *testing.T) {
	p := newPublisher(t)
	name, err := p.Publish("whoami")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if name != "whoami.malmo.local" {
		t.Errorf("name: want whoami.malmo.local, got %q", name)
	}

	path := filepath.Join(p.Dir, "app-whoami.service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	content := string(data)

	wantFragments := []string{
		`<?xml version="1.0" standalone='no'?>`,
		`<!DOCTYPE service-group SYSTEM "avahi-service.dtd">`,
		`<service-group>`,
		`<name replace-wildcards="no">app-whoami</name>`,
		`<host-name>whoami.malmo.local</host-name>`,
		`<type>_malmo-app._tcp</type>`,
		`<port>0</port>`,
	}
	for _, frag := range wantFragments {
		if !strings.Contains(content, frag) {
			t.Errorf("file missing expected fragment %q\nfull content:\n%s", frag, content)
		}
	}
}

// TestPublish_FileMode checks that the written file has mode 0644.
func TestPublish_FileMode(t *testing.T) {
	p := newPublisher(t)
	if _, err := p.Publish("myapp"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	path := filepath.Join(p.Dir, "app-myapp.service")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode: want 0644, got %04o", fi.Mode().Perm())
	}
}

// TestPublish_OverwriteClean checks that publishing the same slug twice
// succeeds and leaves exactly one file.
func TestPublish_OverwriteClean(t *testing.T) {
	p := newPublisher(t)

	if _, err := p.Publish("notes"); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if _, err := p.Publish("notes"); err != nil {
		t.Fatalf("second Publish: %v", err)
	}

	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("want 1 file after two publishes for same slug, got %d", len(entries))
	}
}

// TestUnpublish_RemovesFile checks that Unpublish deletes the service file.
func TestUnpublish_RemovesFile(t *testing.T) {
	p := newPublisher(t)
	if _, err := p.Publish("photos"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := p.Unpublish("photos"); err != nil {
		t.Fatalf("Unpublish: %v", err)
	}

	path := filepath.Join(p.Dir, "app-photos.service")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("want file gone after Unpublish; stat err: %v", err)
	}
}

// TestUnpublish_MissingFileIsNoOp checks idempotency: unpublishing a slug
// with no service file must return nil.
func TestUnpublish_MissingFileIsNoOp(t *testing.T) {
	p := newPublisher(t)
	if err := p.Unpublish("never-published"); err != nil {
		t.Fatalf("Unpublish of missing slug: want nil, got %v", err)
	}
}

// TestPublish_ReturnedNameEqualsSlugPlusHostSuffix verifies the name contract.
func TestPublish_ReturnedNameEqualsSlugPlusHostSuffix(t *testing.T) {
	p := newPublisher(t)
	name, err := p.Publish("music")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := "music" + p.HostSuffix
	if name != want {
		t.Errorf("want %q, got %q", want, name)
	}
}

// TestSlugValidation_RejectsInvalidSlugs checks that path-traversal and other
// malformed slugs are rejected with a clear error — never reaching the filesystem.
func TestSlugValidation_RejectsInvalidSlugs(t *testing.T) {
	p := newPublisher(t)

	badSlugs := []string{
		"../../etc/passwd",
		"slug/with/slash",
		"UPPERCASE",
		"has space",
		"has.dot",
		"",
	}

	for _, slug := range badSlugs {
		t.Run(slug, func(t *testing.T) {
			if _, err := p.Publish(slug); err == nil {
				t.Errorf("Publish(%q): want error for invalid slug, got nil", slug)
			}
			if err := p.Unpublish(slug); err == nil {
				t.Errorf("Unpublish(%q): want error for invalid slug, got nil", slug)
			}
		})
	}
}

// TestSlugValidation_AcceptsValidSlugs verifies representative valid slugs.
func TestSlugValidation_AcceptsValidSlugs(t *testing.T) {
	p := newPublisher(t)

	validSlugs := []string{
		"whoami",
		"my-app",
		"app123",
		"a",
		"123",
	}

	for _, slug := range validSlugs {
		t.Run(slug, func(t *testing.T) {
			if _, err := p.Publish(slug); err != nil {
				t.Errorf("Publish(%q): want nil, got %v", slug, err)
			}
		})
	}
}
