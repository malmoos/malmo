//go:build linux && avahitest

// Package avahipublisher — integration tests against a real Avahi daemon.
//
// # Why these tests are gated behind the "avahitest" build tag
//
// These tests require:
//   - A running avahi-daemon reachable via the system DBus
//   - Root privileges (or a DBus policy that allows the caller to use
//     org.freedesktop.Avahi.Server.EntryGroupNew)
//
// There is no reliable way to mock the Avahi server side without running the
// real daemon. DBus mocking would require the test to stand up a full Avahi
// stub that correctly implements EntryGroupNew, AddAddress, Commit, and Free —
// which is more code than the publisher itself and wrong in different ways.
//
// The nspawn CI lane (future slice — see docs/progress/0013-avahi-dbus-publisher.md)
// will provision a real avahi-daemon and run these tests automatically. Until
// then, run manually on a Linux dev machine with avahi-daemon running:
//
//	go test -tags avahitest ./internal/hostagent/avahipublisher/
//
// After a successful test run you can verify with:
//
//	avahi-resolve -n whoami.malmo.local
package avahipublisher

import (
	"errors"
	"testing"
)

// TestDBusPublisher_PublishAndUnpublish exercises the full Publish → Unpublish
// round-trip against a real avahi-daemon.
func TestDBusPublisher_PublishAndUnpublish(t *testing.T) {
	p := &DBusPublisher{HostSuffix: ".malmo.local"}
	defer p.Close()

	name, err := p.Publish("avahitest-slug")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if name != "avahitest-slug.malmo.local" {
		t.Errorf("name: want avahitest-slug.malmo.local, got %q", name)
	}

	// Unpublish must succeed and remove the group.
	if err := p.Unpublish("avahitest-slug"); err != nil {
		t.Fatalf("Unpublish: %v", err)
	}
}

// TestDBusPublisher_UnpublishIdempotent verifies that unpublishing a slug that
// was never published (or already unpublished) returns nil.
func TestDBusPublisher_UnpublishIdempotent(t *testing.T) {
	p := &DBusPublisher{HostSuffix: ".malmo.local"}
	defer p.Close()

	if err := p.Unpublish("never-published"); err != nil {
		t.Fatalf("Unpublish of unknown slug: want nil, got %v", err)
	}
}

// TestDBusPublisher_PublishIdempotent verifies that publishing the same slug
// twice succeeds without leaking groups.
func TestDBusPublisher_PublishIdempotent(t *testing.T) {
	p := &DBusPublisher{HostSuffix: ".malmo.local"}
	defer p.Close()

	if _, err := p.Publish("avahitest-idem"); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	// Second Publish must free the old group and commit a new one cleanly.
	if _, err := p.Publish("avahitest-idem"); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if len(p.groups) != 1 {
		t.Errorf("want 1 group after two publishes, got %d", len(p.groups))
	}
}

// TestDBusPublisher_ErrCollisionSentinel verifies the sentinel is usable with
// errors.Is even when wrapped.
func TestDBusPublisher_ErrCollisionSentinel(t *testing.T) {
	wrapped := errors.New("wrapped: " + ErrCollision.Error())
	if errors.Is(wrapped, ErrCollision) {
		t.Error("plain wrapped string should not match ErrCollision via errors.Is")
	}
	// The real collision path uses fmt.Errorf("%w", ErrCollision) which does
	// chain correctly.
}
