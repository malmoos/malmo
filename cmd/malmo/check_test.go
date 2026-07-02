package main

import (
	"context"
	"strings"
	"testing"

	"github.com/malmoos/malmo/internal/admission"
)

// check is exercised with admission.CheckStructure (the daemon-free admission
// path) so these stay hermetic — same reason admission's own tests use it.

// --- check passes on representative catalog samples (testdata/) -----------

func TestCheck_RealSamples(t *testing.T) {
	for _, p := range []string{
		"testdata/whoami/manifest.yml",
		"testdata/files-demo/manifest.yml",
	} {
		if err := check(context.Background(), admission.CheckStructure, p); err != nil {
			t.Errorf("check(%s): want clean, got %v", p, err)
		}
	}
}

// --- check runs admission, not just schema -------------------------------

func TestCheck_RejectsOnAdmission(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		wantMsg string // substring the admission error must contain
	}{
		{
			name: "host ports",
			compose: `services:
  web:
    image: nginx:1.0
    ports:
      - "8080:80"
`,
			wantMsg: "host ports",
		},
		{
			name: "named volume",
			compose: `services:
  web:
    image: nginx:1.0
    volumes:
      - db_data:/var/lib/data
`,
			wantMsg: "named volume",
		},
		{
			name: "privileged",
			compose: `services:
  web:
    image: nginx:1.0
    privileged: true
`,
			wantMsg: "privileged",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// validManifest/validCompose live in main_test.go; here the manifest is
			// schema-valid so the failure must come from admission, not lint.
			mp := writeApp(t, validManifest, tc.compose)
			err := check(context.Background(), admission.CheckStructure, mp)
			if err == nil {
				t.Fatalf("want an admission error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q does not name the admission problem (want substring %q)", err, tc.wantMsg)
			}
		})
	}
}

// --- check runs lint first: a schema error fails before admission ---------

func TestCheck_RejectsOnSchema(t *testing.T) {
	bad := strings.Replace(validManifest, "id: test-app", "id: Test_App", 1)
	err := check(context.Background(), admission.CheckStructure, writeApp(t, bad, validCompose))
	if err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("bad slug: want a schema error naming kebab-case, got %v", err)
	}
}
