// Package avahipublisher — pure-Go unit tests (no build tag, no DBus needed).
//
// This file covers slug validation (the one piece of pure-Go logic shared
// across platforms). Local-IP detection and DBus integration are covered in
// dbus_linux_test.go (avahitest build tag, Linux only).
//
// Why no default-runnable tests for DBus? DBus mocking is overkill for v1, and
// there is no reliable way to fake the Avahi server side without a real
// avahi-daemon. The nspawn CI lane (future slice) will host that coverage.
// See docs/progress/0013-avahi-dbus-publisher.md.
package avahipublisher

import (
	"errors"
	"testing"
)

// --- Slug validation ---------------------------------------------------------

func TestSlugRE_RejectsInvalidSlugs(t *testing.T) {
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
			if slugRE.MatchString(slug) {
				t.Errorf("slugRE accepted invalid slug %q; want rejection", slug)
			}
		})
	}
}

func TestSlugRE_AcceptsValidSlugs(t *testing.T) {
	validSlugs := []string{
		"whoami",
		"my-app",
		"app123",
		"a",
		"123",
	}
	for _, slug := range validSlugs {
		t.Run(slug, func(t *testing.T) {
			if !slugRE.MatchString(slug) {
				t.Errorf("slugRE rejected valid slug %q; want acceptance", slug)
			}
		})
	}
}

// TestPublish_RejectsInvalidSlug exercises the Publish/Unpublish slug guards
// without needing a real DBus connection — the validation fires before any
// network call.
func TestPublish_RejectsInvalidSlug(t *testing.T) {
	p := &DBusPublisher{HostSuffix: ".malmo.local"}

	badSlugs := []string{"", "UPPER", "has.dot", "has space", "../etc/passwd"}
	for _, slug := range badSlugs {
		t.Run(slug, func(t *testing.T) {
			_, err := p.Publish(slug)
			if err == nil {
				t.Errorf("Publish(%q): want error for invalid slug, got nil", slug)
			}
		})
	}
}

// TestErrCollision_Sentinel verifies the sentinel can be used with errors.Is.
func TestErrCollision_Sentinel(t *testing.T) {
	if !errors.Is(ErrCollision, ErrCollision) {
		t.Error("errors.Is(ErrCollision, ErrCollision) should be true")
	}
	if errors.Is(errors.New("other"), ErrCollision) {
		t.Error("errors.Is(other, ErrCollision) should be false")
	}
}
