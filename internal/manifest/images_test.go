package manifest

import "testing"

// imagesBase is a minimal valid manifest the images-block cases append to.
const imagesBase = `id: sizer
manifest_version: 1
name: Sizer
version: "1.0"
compose_file: compose.yml
main_service: web
main_port: 80
`

func TestParseImagesObjectForm(t *testing.T) {
	m, err := Parse([]byte(imagesBase + `images:
  traefik/whoami:v1.10.3:
    digest: sha256:abc
    download_bytes: 2850040
    disk_bytes: 2853860
`))
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := m.Images["traefik/whoami:v1.10.3"]
	if !ok {
		t.Fatalf("image key absent, got %v", m.Images)
	}
	if ref.Digest != "sha256:abc" || ref.DownloadBytes != 2850040 || ref.DiskBytes != 2853860 {
		t.Fatalf("object form not parsed: %+v", ref)
	}
}

// TestParseImagesLegacyScalarForm pins the back-compat shorthand: a pre-#69
// manifest with `image:tag: sha256:…` (digest only) still parses, sizes zero.
func TestParseImagesLegacyScalarForm(t *testing.T) {
	m, err := Parse([]byte(imagesBase + `images:
  traefik/whoami:v1.10.3: sha256:abc
`))
	if err != nil {
		t.Fatal(err)
	}
	ref := m.Images["traefik/whoami:v1.10.3"]
	if ref.Digest != "sha256:abc" {
		t.Fatalf("scalar digest not parsed: %+v", ref)
	}
	if ref.DownloadBytes != 0 || ref.DiskBytes != 0 {
		t.Fatalf("scalar form must leave sizes zero, got %+v", ref)
	}
}

func TestFootprintSumsAndHoistsEstimatedSize(t *testing.T) {
	m, err := Parse([]byte(imagesBase + `storage:
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
`))
	if err != nil {
		t.Fatal(err)
	}
	f := m.Footprint()
	if f.ImageDownloadBytes != 130 || f.ImageDiskBytes != 470 {
		t.Fatalf("footprint sums wrong: %+v", f)
	}
	if f.EstimatedState != "10GB" {
		t.Fatalf("estimated_state not hoisted: %q", f.EstimatedState)
	}
}

// TestFootprintNoStorageOmitsState is the unset-estimated_size case: no storage
// block ⇒ empty EstimatedState (omitted by the omitempty tag downstream).
func TestFootprintNoStorageOmitsState(t *testing.T) {
	m, err := Parse([]byte(imagesBase + `images:
  app/one:1:
    digest: sha256:a
    download_bytes: 5
    disk_bytes: 9
`))
	if err != nil {
		t.Fatal(err)
	}
	f := m.Footprint()
	if f.EstimatedState != "" {
		t.Fatalf("want empty EstimatedState, got %q", f.EstimatedState)
	}
	if f.ImageDownloadBytes != 5 || f.ImageDiskBytes != 9 {
		t.Fatalf("footprint wrong: %+v", f)
	}
}

func TestComposeImagesDistinctSorted(t *testing.T) {
	// Two services share one image (gateway+dashboard pattern); a third is
	// distinct. Want the deduped, sorted set.
	imgs, err := ComposeImages([]byte(`services:
  dashboard:
    image: nous/hermes:v2
  gateway:
    image: nous/hermes:v2
  db:
    image: postgres:16
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nous/hermes:v2", "postgres:16"}
	if len(imgs) != len(want) {
		t.Fatalf("got %v, want %v", imgs, want)
	}
	for i := range want {
		if imgs[i] != want[i] {
			t.Fatalf("got %v, want %v", imgs, want)
		}
	}
}

func TestComposeImagesRejects(t *testing.T) {
	if _, err := ComposeImages([]byte(`version: "3"`)); err == nil {
		t.Fatal("want error on compose with no services")
	}
	if _, err := ComposeImages([]byte(`services:
  web:
    command: ["x"]
`)); err == nil {
		t.Fatal("want error on service with no image")
	}
}
