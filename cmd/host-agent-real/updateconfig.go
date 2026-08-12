package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/malmoos/malmo/internal/hostagent/updatetarget"
)

// This file resolves the two per-box update settings: which update target this
// box reads, and the window it applies in. Both can arrive three ways, and the
// precedence is the same for both:
//
//	systemd credential  >  environment variable  >  built-in default
//
// The credential wins because it is the **per-box fact**, injected at provision
// time down the same path malmo.seed already uses (ENVIRONMENT.md
// # Provisioning). A hosted box has no SSH (ENVIRONMENT.md # Access & files), so
// after boot there is no other way in; without this, a provisioned box could
// never be pointed at a candidate release. The environment variable stays below
// it as the local hand-edit: an operator drop-in, or the CI boot proof pointing
// the loop at a server inside the guest.
//
// **Nothing here is build-tagged, on purpose.** The target URL only matters on
// the hosted build, but `go vet ./...` and `go test ./...` both run untagged, so
// anything behind `//go:build hosted` is neither vetted nor tested by CI. The tag
// stays on the call site (updatetarget_hosted.go); the logic lives here.
const (
	credUpdateTargetURL = "malmo.update_target_url"
	credUpdateWindow    = "malmo.update_window"

	envUpdateTargetURL = "MALMO_UPDATE_TARGET_URL"
	envUpdateWindow    = "MALMO_UPDATE_WINDOW"
)

// Which of the three sources won, for the startup log.
const (
	fromCredential = "credential"
	fromEnv        = "env"
	fromDefault    = "default"
)

// credentialsDirEnv is systemd's handle on the credentials it passed this unit.
// systemd sets it only when the unit imports at least one credential that was
// actually delivered.
const credentialsDirEnv = "CREDENTIALS_DIRECTORY"

// readCredential reads the systemd credential called name.
//
// ok is false when this unit was passed no credentials at all, or none by that
// name. That is the ordinary case (a box provisioned without either credential
// keeps the fleet behaviour), so it is not an error. A credential that is there
// but cannot be read is, because the caller asked for something that exists and
// did not get it.
func readCredential(name string) (value string, ok bool, err error) {
	dir := os.Getenv(credentialsDirEnv)
	if dir == "" {
		return "", false, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read credential %s: %w", name, err)
	}
	// Trimmed: a credential written by a provisioner is text, and a trailing
	// newline is the most likely way to hand over a URL that then does not parse.
	return strings.TrimSpace(string(b)), true, nil
}

// updateTargetURL resolves the update-target endpoint for this box. An empty
// target means updatetarget.DefaultURL, the fleet endpoint.
//
// **A credential that is present but unusable is an error, never a fallback.**
// A box carrying this credential was deliberately pointed somewhere by whoever
// provisioned it; silently reading the fleet default instead would move a box
// that is meant to be pinned to a candidate straight onto stable, which is the
// one outcome pinning exists to prevent. The caller refuses rather than updates.
//
// Only the credential is validated. The environment variable is the operator's
// own hand-edit on a box they can already reach, and it has never been checked;
// tightening it is a separate change, not a side effect of this one.
func updateTargetURL() (target, from string, err error) {
	raw, ok, err := readCredential(credUpdateTargetURL)
	if err != nil {
		return "", "", err
	}
	if ok {
		if err := checkTargetURL(raw); err != nil {
			return "", "", err
		}
		return raw, fromCredential, nil
	}
	if v := os.Getenv(envUpdateTargetURL); v != "" {
		return v, fromEnv, nil
	}
	return "", fromDefault, nil
}

// checkTargetURL rejects anything the box could not actually fetch from. It is
// deliberately shallow (an absolute http or https URL with a host) because the point
// is to catch a provisioning mistake (an empty file, a hostname with no scheme,
// a pasted shell fragment), not to police where a box may be pointed.
func checkTargetURL(s string) error {
	if s == "" {
		return fmt.Errorf("credential %s is empty", credUpdateTargetURL)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("credential %s is not a URL: %w", credUpdateTargetURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("credential %s must be an http or https URL, got %q", credUpdateTargetURL, s)
	}
	if u.Host == "" {
		return fmt.Errorf("credential %s has no host: %q", credUpdateTargetURL, s)
	}
	return nil
}

// updateWindow resolves the window an update may start in.
//
// Unlike the target URL, an unreadable window falls back with a warning. The two
// are not symmetric: a wrong window can only apply an update at the wrong hour,
// while a wrong target sends the box to the wrong version. Losing a box's
// updates over a mistyped clock reading would be the worse trade.
func updateWindow() (updatetarget.Window, string) {
	raw, from := windowSetting()
	w, err := updatetarget.ParseWindow(raw)
	if err != nil {
		// A box with a mistyped window must not lose its updates, and must not
		// silently use a window nobody chose either: say so and take the default.
		slog.Warn("update window is not readable; using the default", "err", err, "from", from)
		return updatetarget.DefaultWindow, fromDefault
	}
	return w, from
}

// windowSetting returns the raw window setting and where it came from. An empty
// credential counts as absent rather than as "use the default": it carries no
// instruction, so the environment variable below it should still be heard.
func windowSetting() (raw, from string) {
	v, ok, err := readCredential(credUpdateWindow)
	if err != nil {
		slog.Warn("update window credential is not readable; falling back", "err", err)
	} else if ok && v != "" {
		return v, fromCredential
	}
	if v := os.Getenv(envUpdateWindow); v != "" {
		return v, fromEnv
	}
	return "", fromDefault
}
