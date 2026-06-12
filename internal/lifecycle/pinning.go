package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/malmoos/malmo/internal/manifest"
	"github.com/malmoos/malmo/internal/store"
	"gopkg.in/yaml.v3"
)

// servicePin is the resolved digest pin for one compose service
// (APP_LIFECYCLE.md # image digest pinning).
type servicePin struct {
	Service string
	Image   string // original `image:` ref from the author's compose
	Digest  string // `sha256:…`
}

// PinnedRef returns the `name@sha256:…` form to write into the override.
func (p servicePin) PinnedRef() string {
	return repoOf(p.Image) + "@" + p.Digest
}

// resolveImages pulls each service's image, reads the registry RepoDigest,
// verifies any catalog promise (Door-1), and returns the per-service pin in
// stable order. Door-2 callers pass a manifest with an empty Images map; that
// path is pure TOFU. Failures here happen before any compose up — they roll
// back the partial install cleanly.
func resolveImages(ctx context.Context, docker DockerDriver, man *manifest.Manifest, composeBytes []byte) ([]servicePin, error) {
	svcImages, err := serviceImages(composeBytes)
	if err != nil {
		return nil, err
	}

	// Pull each unique image once.
	seen := map[string]string{} // image → digest
	for _, img := range svcImages {
		if _, done := seen[img]; done {
			continue
		}
		digest, err := pullAndResolve(ctx, docker, img)
		if err != nil {
			return nil, err
		}
		seen[img] = digest
	}

	// Verify against the catalog's promised digests, if any
	// (APP_STORE.md # Trust model — catalog binds version→bytes).
	for img, gotDigest := range seen {
		promised, ok := man.Images[img]
		if !ok {
			continue
		}
		if promised.Digest != gotDigest {
			return nil, fmt.Errorf("catalog digest mismatch for %s: catalog promised %s, registry served %s",
				img, promised.Digest, gotDigest)
		}
	}

	pins := make([]servicePin, 0, len(svcImages))
	for svc, img := range svcImages {
		pins = append(pins, servicePin{Service: svc, Image: img, Digest: seen[img]})
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Service < pins[j].Service })
	return pins, nil
}

// pullAndResolve pulls the image and returns the registry content digest
// (`sha256:…`). If the image is already in digest form, the pull is still done
// (to ensure the bytes are local) and the supplied digest is returned.
func pullAndResolve(ctx context.Context, docker DockerDriver, image string) (string, error) {
	if d, ok := digestOf(image); ok {
		if err := docker.Pull(ctx, image); err != nil {
			return "", err
		}
		return d, nil
	}
	if err := docker.Pull(ctx, image); err != nil {
		return "", err
	}
	repoDigests, err := docker.ImageInspect(ctx, image)
	if err != nil {
		return "", err
	}
	repo := repoOf(image)
	for _, rd := range repoDigests {
		name, digest, ok := strings.Cut(rd, "@")
		if !ok {
			continue
		}
		if name == repo {
			return digest, nil
		}
	}
	return "", fmt.Errorf("no RepoDigest for %s matched repo %s (got %v) — image may be local-only",
		image, repo, repoDigests)
}

// serviceImages returns service → `image:` reference for every service in the
// compose. A service without an `image:` is an error here: admission already
// rejects `build:`, so by this point every service must declare an image.
func serviceImages(composeBytes []byte) (map[string]string, error) {
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose for images: %w", err)
	}
	out := make(map[string]string, len(doc.Services))
	for name, svc := range doc.Services {
		if strings.TrimSpace(svc.Image) == "" {
			return nil, fmt.Errorf("service %q has no image", name)
		}
		out[name] = svc.Image
	}
	return out, nil
}

// repoOf returns the registry repo portion of an image reference, stripping
// both `:tag` and `@sha256:…` suffixes. The tag colon is distinguished from a
// port colon by checking whether a `/` follows it.
func repoOf(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		return image[:at]
	}
	if colon := strings.LastIndex(image, ":"); colon > 0 && !strings.Contains(image[colon:], "/") {
		return image[:colon]
	}
	return image
}

// digestOf returns the `sha256:…` portion if the image is already pinned by
// digest (`name@sha256:…`), otherwise ("", false).
func digestOf(image string) (string, bool) {
	if at := strings.Index(image, "@"); at >= 0 {
		return image[at+1:], true
	}
	return "", false
}

// toInstanceImages converts pins into the row form persisted in SQLite.
func toInstanceImages(pins []servicePin) []store.InstanceImage {
	out := make([]store.InstanceImage, len(pins))
	for i, p := range pins {
		out[i] = store.InstanceImage{Service: p.Service, Image: p.Image, Digest: p.Digest}
	}
	return out
}
