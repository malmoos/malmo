package manifest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Synthesize builds a manifest for a user-pasted (Door-2) compose
// (APP_MANIFEST.md # Custom container — synthetic manifest). It infers the main
// service when the compose has exactly one; otherwise the caller must name it.
// Custom apps default to internet-on, no managed services.
func Synthesize(name string, composeBytes []byte, mainService string, mainPort int) (*Manifest, []byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil, fmt.Errorf("a name is required")
	}
	if mainPort == 0 {
		return nil, nil, fmt.Errorf("a main port is required")
	}

	services, err := ComposeServiceNames(composeBytes)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case mainService == "" && len(services) == 1:
		mainService = services[0]
	case mainService == "":
		return nil, nil, fmt.Errorf("compose has %d services (%s) — specify which is the main service",
			len(services), strings.Join(services, ", "))
	default:
		if !contains(services, mainService) {
			return nil, nil, fmt.Errorf("main service %q is not in the compose (services: %s)",
				mainService, strings.Join(services, ", "))
		}
	}

	slug := slugify(name)
	if slug == "" {
		return nil, nil, fmt.Errorf("name %q has no usable characters for a slug", name)
	}

	man := &Manifest{
		ID:              slug + "-" + entropy(),
		ManifestVersion: 1,
		Name:            name,
		Version:         "custom",
		ComposeFile:     "compose.yml",
		MainService:     mainService,
		MainPort:        mainPort,
		PreferredSlugs:  []string{slug},
		Permissions:     Permissions{Internet: true},
	}
	if err := man.validate(); err != nil {
		return nil, nil, err
	}
	return man, composeBytes, nil
}

// ComposeServiceNames returns the service names declared under `services:` in a
// compose document, erroring if the bytes aren't valid YAML or declare no
// services. Two consumers: Synthesize (Door-2 main-service inference) and the
// `malmo manifest lint` CLI (confirming a manifest's main_service exists in its
// sibling compose).
func ComposeServiceNames(composeBytes []byte) ([]string, error) {
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("compose is not valid YAML: %w", err)
	}
	if len(doc.Services) == 0 {
		return nil, fmt.Errorf("compose declares no services")
	}
	names := make([]string, 0, len(doc.Services))
	for n := range doc.Services {
		names = append(names, n)
	}
	return names, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func entropy() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
