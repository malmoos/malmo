package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/malmoos/malmo/internal/hostagent/updatetarget"
)

// credsDir points CREDENTIALS_DIRECTORY at a fresh directory holding the given
// credentials, the way systemd does for a unit with ImportCredential=.
func credsDir(t *testing.T, creds map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range creds {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o400); err != nil {
			t.Fatalf("write credential %s: %v", name, err)
		}
	}
	t.Setenv(credentialsDirEnv, dir)
}

// noCreds is the un-provisioned case: systemd passed this unit nothing, so it
// set no CREDENTIALS_DIRECTORY at all.
func noCreds(t *testing.T) {
	t.Helper()
	t.Setenv(credentialsDirEnv, "")
}

// unreadableCred makes the named credential exist but fail to read: the entry is
// there, so it is not the absent case, and opening it cannot produce its bytes.
//
// A directory is what stands in for the unreadable file. Mode 0o000 would be the
// obvious choice and is the wrong one: root ignores it, so the test would quietly
// assert nothing whenever the suite runs as root. os.ReadFile on a directory
// fails for every user.
func unreadableCred(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
		t.Fatalf("make unreadable credential %s: %v", name, err)
	}
	t.Setenv(credentialsDirEnv, dir)
}

func TestReadCredential(t *testing.T) {
	t.Run("no credentials directory", func(t *testing.T) {
		noCreds(t)
		v, ok, err := readCredential(credUpdateTargetURL)
		if err != nil || ok || v != "" {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, nil)", v, ok, err)
		}
	})

	t.Run("directory without this credential", func(t *testing.T) {
		credsDir(t, map[string]string{"malmo.seed": "{}"})
		v, ok, err := readCredential(credUpdateTargetURL)
		if err != nil || ok || v != "" {
			t.Fatalf("got (%q, %v, %v), want (\"\", false, nil)", v, ok, err)
		}
	})

	t.Run("present, trailing newline trimmed", func(t *testing.T) {
		credsDir(t, map[string]string{credUpdateTargetURL: "https://example.test/target\n"})
		v, ok, err := readCredential(credUpdateTargetURL)
		if err != nil {
			t.Fatalf("readCredential: %v", err)
		}
		if !ok || v != "https://example.test/target" {
			t.Fatalf("got (%q, %v), want (%q, true)", v, ok, "https://example.test/target")
		}
	})

	// Present but unreadable is an error, not the absent case. The caller asked
	// for something that is there and did not get it, and the two settings then
	// make opposite calls about what to do with that.
	t.Run("present but unreadable", func(t *testing.T) {
		unreadableCred(t, credUpdateTargetURL)
		v, ok, err := readCredential(credUpdateTargetURL)
		if err == nil {
			t.Fatalf("got (%q, %v, nil), want an error", v, ok)
		}
		if ok {
			t.Fatal("an unreadable credential must not report ok")
		}
	})
}

func TestUpdateTargetURL(t *testing.T) {
	const cred = "https://candidate.example.test/api/updates/target"
	const env = "http://127.0.0.1:9"

	t.Run("credential beats the env var", func(t *testing.T) {
		credsDir(t, map[string]string{credUpdateTargetURL: cred})
		t.Setenv(envUpdateTargetURL, env)
		target, from, err := updateTargetURL()
		if err != nil {
			t.Fatalf("updateTargetURL: %v", err)
		}
		if target != cred || from != fromCredential {
			t.Fatalf("got (%q, %q), want (%q, %q)", target, from, cred, fromCredential)
		}
	})

	t.Run("env var used when no credential", func(t *testing.T) {
		noCreds(t)
		t.Setenv(envUpdateTargetURL, env)
		target, from, err := updateTargetURL()
		if err != nil {
			t.Fatalf("updateTargetURL: %v", err)
		}
		if target != env || from != fromEnv {
			t.Fatalf("got (%q, %q), want (%q, %q)", target, from, env, fromEnv)
		}
	})

	// The regression that matters most: a box provisioned the way every box is
	// provisioned today must still read the fleet endpoint.
	t.Run("neither leaves the fleet default untouched", func(t *testing.T) {
		noCreds(t)
		t.Setenv(envUpdateTargetURL, "")
		target, from, err := updateTargetURL()
		if err != nil {
			t.Fatalf("updateTargetURL: %v", err)
		}
		if target != "" || from != fromDefault {
			t.Fatalf("got (%q, %q), want (\"\", %q)", target, from, fromDefault)
		}
		// Empty is what HTTPSource reads as "the fleet endpoint".
		if (updatetarget.HTTPSource{URL: target}).URL != "" {
			t.Fatal("an absent credential must leave HTTPSource on its default URL")
		}
	})

	// An unusable credential is refused. It must not fall through to the env var
	// or to the fleet default: a box meant to be pinned must not join stable.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"whitespace only", "  \n"},
		{"no scheme", "candidate.example.test/api/updates/target"},
		{"wrong scheme", "ftp://candidate.example.test/target"},
		{"no host", "https:///api/updates/target"},
		{"not a url", "https://exa mple.test/\x7f"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			credsDir(t, map[string]string{credUpdateTargetURL: tc.body})
			t.Setenv(envUpdateTargetURL, env)
			target, from, err := updateTargetURL()
			if err == nil {
				t.Fatalf("got (%q, %q, nil), want an error", target, from)
			}
			if target != "" {
				t.Fatalf("a refused credential resolved to %q; it must resolve to nothing", target)
			}
		})
	}

	// An unreadable credential is refused for the same reason an unparseable one
	// is: the box was pointed somewhere and we cannot honour it, so reading the
	// fleet endpoint instead would move a pinned box onto stable.
	t.Run("refuses an unreadable credential", func(t *testing.T) {
		unreadableCred(t, credUpdateTargetURL)
		t.Setenv(envUpdateTargetURL, env)
		target, _, err := updateTargetURL()
		if err == nil {
			t.Fatal("an unreadable credential must be refused, not resolved")
		}
		if target != "" {
			t.Fatalf("an unreadable credential fell back to %q; it must fall back to nothing", target)
		}
	})
}

func TestUpdateWindow(t *testing.T) {
	credWindow := updatetarget.Window{Start: 1 * time.Hour, End: 2 * time.Hour}
	envWindow := updatetarget.Window{Start: 5 * time.Hour, End: 6 * time.Hour}

	t.Run("credential beats the env var", func(t *testing.T) {
		credsDir(t, map[string]string{credUpdateWindow: "01:00-02:00\n"})
		t.Setenv(envUpdateWindow, "05:00-06:00")
		w, from := updateWindow()
		if w != credWindow || from != fromCredential {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, credWindow, fromCredential)
		}
	})

	t.Run("env var used when no credential", func(t *testing.T) {
		noCreds(t)
		t.Setenv(envUpdateWindow, "05:00-06:00")
		w, from := updateWindow()
		if w != envWindow || from != fromEnv {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, envWindow, fromEnv)
		}
	})

	t.Run("neither leaves the default window untouched", func(t *testing.T) {
		noCreds(t)
		t.Setenv(envUpdateWindow, "")
		w, from := updateWindow()
		if w != updatetarget.DefaultWindow || from != fromDefault {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, updatetarget.DefaultWindow, fromDefault)
		}
	})

	// Unlike the target URL, a mistyped window falls back rather than costing the
	// box its updates: a wrong hour cannot send it to a wrong version.
	t.Run("unparseable credential falls back to the default", func(t *testing.T) {
		credsDir(t, map[string]string{credUpdateWindow: "half past three"})
		t.Setenv(envUpdateWindow, "")
		w, from := updateWindow()
		if w != updatetarget.DefaultWindow || from != fromDefault {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, updatetarget.DefaultWindow, fromDefault)
		}
	})

	// The other half of the asymmetry the target URL sets up: the window does not
	// refuse an unreadable credential, it warns and carries on down the chain, so
	// the env var below it is still heard. Losing a box's updates over a clock
	// reading it could not read would be the worse trade.
	t.Run("unreadable credential defers to the env var", func(t *testing.T) {
		unreadableCred(t, credUpdateWindow)
		t.Setenv(envUpdateWindow, "05:00-06:00")
		w, from := updateWindow()
		if w != envWindow || from != fromEnv {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, envWindow, fromEnv)
		}
	})

	// An empty credential carries no instruction, so the env var below it is
	// still heard rather than being shadowed into the default.
	t.Run("empty credential defers to the env var", func(t *testing.T) {
		credsDir(t, map[string]string{credUpdateWindow: "\n"})
		t.Setenv(envUpdateWindow, "05:00-06:00")
		w, from := updateWindow()
		if w != envWindow || from != fromEnv {
			t.Fatalf("got (%v, %q), want (%v, %q)", w, from, envWindow, fromEnv)
		}
	})
}
