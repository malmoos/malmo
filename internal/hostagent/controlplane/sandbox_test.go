package controlplane

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every control-plane container runs the same sandbox an app container gets:
// all capabilities dropped, no-new-privileges, a read-only root
// (APP_ISOLATION.md # Capabilities & privilege, CONTROL_PLANE.md # Caddy, #431).
// For apps the brain writes that override itself, so the code enforces it. The
// control plane declares it by hand in a compose file, where a later edit can
// drop a line and nothing would notice — the stack still starts, just without
// the sandbox. This test is what notices.
//
// It reads the committed dev/control-plane/compose.yml, the same file the image
// build stages onto a box (see stageRealCompose).
func TestComposeServicesAreSandboxed(t *testing.T) {
	src := repoPath("dev", "control-plane", "compose.yml")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	var doc struct {
		Services map[string]struct {
			ReadOnly    bool     `yaml:"read_only"`
			CapDrop     []string `yaml:"cap_drop"`
			CapAdd      []string `yaml:"cap_add"`
			SecurityOpt []string `yaml:"security_opt"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	if len(doc.Services) == 0 {
		t.Fatalf("%s declares no services", src)
	}
	for name, svc := range doc.Services {
		if !svc.ReadOnly {
			t.Errorf("service %q: read_only is not set", name)
		}
		if len(svc.CapDrop) != 1 || svc.CapDrop[0] != "ALL" {
			t.Errorf("service %q: cap_drop = %v, want [ALL]", name, svc.CapDrop)
		}
		// Both services are a Caddy binding :80 inside the container, so both
		// need exactly this one capability back and no other.
		if len(svc.CapAdd) != 1 || svc.CapAdd[0] != "NET_BIND_SERVICE" {
			t.Errorf("service %q: cap_add = %v, want [NET_BIND_SERVICE]", name, svc.CapAdd)
		}
		if len(svc.SecurityOpt) != 1 || svc.SecurityOpt[0] != "no-new-privileges:true" {
			t.Errorf("service %q: security_opt = %v, want [no-new-privileges:true]", name, svc.SecurityOpt)
		}
	}
}
