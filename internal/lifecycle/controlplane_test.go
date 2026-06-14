package lifecycle

import (
	"context"
	"errors"
	"testing"
)

func TestEnsureControlPlaneUpsTheStack(t *testing.T) {
	docker := newFakeDocker()
	m := &Manager{docker: docker}

	if err := m.EnsureControlPlane(context.Background(), "/var/lib/malmo/control-plane"); err != nil {
		t.Fatalf("EnsureControlPlane: %v", err)
	}
	if !docker.called("ControlPlaneUp") {
		t.Fatalf("ControlPlaneUp not invoked: %v", docker.methods())
	}
	// The fixed project name is what makes the stack idempotent across reboots.
	c := docker.Calls()[0]
	if c.args[0] != "/var/lib/malmo/control-plane" || c.args[1] != controlPlaneProject {
		t.Errorf("ControlPlaneUp args = %v, want [dir %s]", c.args, controlPlaneProject)
	}
}

func TestEnsureControlPlaneErrorPropagates(t *testing.T) {
	docker := newFakeDocker()
	docker.controlPlaneUpErr = errors.New("compose boom")
	m := &Manager{docker: docker}

	if err := m.EnsureControlPlane(context.Background(), "/var/lib/malmo/control-plane"); err == nil {
		t.Fatal("want error when the control-plane compose up fails")
	}
}
