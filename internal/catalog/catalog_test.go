package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// writeApp drops a manifest.yml + compose.yml under root/<id>/ for a fixture.
func writeApp(t *testing.T, root, id, manifestYML string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), []byte(manifestYML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services:\n  web:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListPopulatesFootprint is the #70 surface: a catalog entry carries the
// coarse footprint (summed image bytes + verbatim estimated_state) so the store
// grid renders sizes without a second fetch.
func TestListPopulatesFootprint(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "sizer", `id: sizer
manifest_version: 1
name: Sizer
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
storage:
  estimated_size: 10GB
images:
  app/one:1:
    digest: sha256:a
    download_bytes: 100
    disk_bytes: 400
  app/two:2:
    digest: sha256:b
    download_bytes: 30
    disk_bytes: 70
`)

	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	f := entries[0].Footprint
	if f.ImageDownloadBytes != 130 || f.ImageDiskBytes != 470 {
		t.Fatalf("footprint image bytes wrong: %+v", f)
	}
	if f.EstimatedState != "10GB" {
		t.Fatalf("estimated_state not carried: %q", f.EstimatedState)
	}
}

// TestListFootprintZeroWhenNoImages is the no-images manifest (whoami-style):
// the footprint is present and zeroed, never absent.
func TestListFootprintZeroWhenNoImages(t *testing.T) {
	root := t.TempDir()
	writeApp(t, root, "bare", `id: bare
manifest_version: 1
name: Bare
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`)

	entries, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	f := entries[0].Footprint
	if f.ImageDownloadBytes != 0 || f.ImageDiskBytes != 0 || f.EstimatedState != "" {
		t.Fatalf("want zero footprint, got %+v", f)
	}
}
