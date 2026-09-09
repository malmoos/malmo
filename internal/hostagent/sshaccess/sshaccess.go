// Package sshaccess implements hostagent.SSHAccessManager against a real sshd.
// It is kept out of the shared internal/hostagent package so that package has no
// sshd or systemd dependency; only cmd/host-agent-real imports it.
//
// The unit of work is one account's full desired state
// (BRAIN_HOST_PROTOCOL.md # SSH access). Three things happen per call, in this
// order, and the order is the point:
//
//  1. The account's authorized_keys file is written (or removed).
//  2. The sshd drop-in is re-rendered from the whole enabled set and validated
//     with `sshd -t` before it is allowed to take effect.
//  3. The daemon is started or stopped so that :22 is open exactly while at
//     least one account is enabled (BUILD.md # SSH).
//
// Keys are written before the config so that an account never becomes reachable
// a moment before the key that authenticates it exists. Validation happens
// before the reload so a bad render cannot take a running sshd down and lock out
// the accounts that were already working.
package sshaccess

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/malmoos/malmo/internal/protocol"
)

// DefaultDropInPath is the sshd config fragment malmo owns. It is rendered
// whole on every change, never line-edited: the file is a projection of the
// enabled set, so anything hand-added to it is not preserved (which is also why
// the brain surfaces drift here rather than silently re-applying — see the
// asymmetric drift policy in BRAIN_HOST_PROTOCOL.md # B).
const DefaultDropInPath = "/etc/ssh/sshd_config.d/malmo-allowed.conf"

// DefaultUnit is the systemd unit for sshd on Debian.
const DefaultUnit = "ssh.service"

// Manager applies SSH access on the local system.
type Manager struct {
	// DropInPath is the sshd config fragment to render. Empty → DefaultDropInPath.
	DropInPath string
	// Unit is the sshd systemd unit. Empty → DefaultUnit.
	Unit string
	// Runner runs a command and returns its combined output. Empty → exec.
	// Present so the tests can drive the render and lifecycle logic without a
	// real sshd or systemd; nothing else swaps it.
	Runner func(name string, args ...string) ([]byte, error)
	// Lookup resolves an account to its home directory and ids. Empty →
	// user.Lookup. Swapped only by the tests, which have no malmo accounts on the
	// machine running them.
	Lookup func(username string) (*user.User, error)
}

func (m *Manager) dropInPath() string {
	if m.DropInPath != "" {
		return m.DropInPath
	}
	return DefaultDropInPath
}

func (m *Manager) unit() string {
	if m.Unit != "" {
		return m.Unit
	}
	return DefaultUnit
}

func (m *Manager) run(name string, args ...string) ([]byte, error) {
	if m.Runner != nil {
		return m.Runner(name, args...)
	}
	return exec.Command(name, args...).CombinedOutput()
}

// account is one enabled account as the drop-in records it. The rendered file is
// the only persistent store of the enabled set on the host: host-agent keeps no
// state of its own, so a restart re-reads reality rather than trusting a cache.
type account struct {
	Username        string
	KeyCount        int
	RequirePassword bool
}

// SetAccess applies one account's full desired state.
func (m *Manager) SetAccess(req protocol.SetSSHAccessRequest) error {
	if req.User == "" {
		return fmt.Errorf("sshaccess: user is required")
	}

	current, err := m.readDropIn()
	if err != nil {
		return err
	}

	if err := m.writeKeys(req.User, req.AuthorizedKeys, req.Enabled); err != nil {
		return err
	}

	next := make([]account, 0, len(current)+1)
	for _, a := range current {
		if a.Username != req.User {
			next = append(next, a)
		}
	}
	if req.Enabled {
		next = append(next, account{
			Username:        req.User,
			KeyCount:        len(req.AuthorizedKeys),
			RequirePassword: req.RequirePassword,
		})
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Username < next[j].Username })

	if err := m.writeDropIn(next); err != nil {
		return err
	}
	return m.applyDaemon(len(next) > 0)
}

// State reports what the host actually has: the daemon's run state from systemd,
// and the enabled set from the rendered drop-in.
func (m *Manager) State() (protocol.SSHState, error) {
	accounts, err := m.readDropIn()
	if err != nil {
		return protocol.SSHState{}, err
	}
	users := make([]protocol.SSHUserState, 0, len(accounts))
	for _, a := range accounts {
		users = append(users, protocol.SSHUserState{
			Username:        a.Username,
			KeyCount:        a.KeyCount,
			RequirePassword: a.RequirePassword,
		})
	}
	// `systemctl is-active` exits non-zero for every not-running state, so the
	// error is the answer here and is deliberately not propagated: "inactive" is
	// not a failure to read state.
	out, _ := m.run("systemctl", "is-active", m.unit())
	return protocol.SSHState{
		DaemonRunning: strings.TrimSpace(string(out)) == "active",
		Users:         users,
	}, nil
}

// writeKeys writes (or removes) an account's authorized_keys. The file and its
// ~/.ssh directory are owned by the account and mode 0600/0700, because sshd
// refuses to read a key file that is group- or world-writable, and because the
// user must be able to manage it from their own shell.
func (m *Manager) writeKeys(username string, keys []string, enabled bool) error {
	lookup := m.Lookup
	if lookup == nil {
		lookup = user.Lookup
	}
	u, err := lookup(username)
	if err != nil {
		return fmt.Errorf("sshaccess: lookup %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("sshaccess: uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("sshaccess: gid %q: %w", u.Gid, err)
	}

	sshDir := filepath.Join(u.HomeDir, ".ssh")
	keyFile := filepath.Join(sshDir, "authorized_keys")

	// Disabling removes the keys rather than leaving them behind: the account is
	// off, and a stale key file is a credential nobody is tracking.
	if !enabled || len(keys) == 0 {
		if err := os.Remove(keyFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("sshaccess: remove %s: %w", keyFile, err)
		}
		return nil
	}

	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("sshaccess: mkdir %s: %w", sshDir, err)
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return fmt.Errorf("sshaccess: chown %s: %w", sshDir, err)
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		return fmt.Errorf("sshaccess: chmod %s: %w", sshDir, err)
	}

	body := strings.Join(keys, "\n") + "\n"
	if err := writeFileAtomic(keyFile, []byte(body), 0o600, uid, gid); err != nil {
		return fmt.Errorf("sshaccess: write %s: %w", keyFile, err)
	}
	return nil
}

// writeDropIn renders the config for the whole enabled set and refuses to
// install it unless `sshd -t` accepts it. The candidate is written next to the
// real path (same directory, so the rename is atomic and on the same
// filesystem), tested, then moved into place.
func (m *Manager) writeDropIn(accounts []account) error {
	path := m.dropInPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sshaccess: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, []byte(render(accounts)), 0o644, -1, -1); err != nil {
		return fmt.Errorf("sshaccess: write %s: %w", path, err)
	}
	if out, err := m.run("sshd", "-t"); err != nil {
		return fmt.Errorf("sshaccess: sshd -t rejected the rendered config: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyDaemon brings sshd to the state the enabled set implies. With no enabled
// account the unit is stopped and disabled, which is what closes :22 — on hosted
// that is the only control over the port (ENVIRONMENT.md # Access & files).
//
// Reload rather than restart when it should be running: a restart drops live
// sessions, and an admin fixing something over SSH is exactly who is most likely
// to be connected while this runs.
func (m *Manager) applyDaemon(shouldRun bool) error {
	unit := m.unit()
	if !shouldRun {
		if out, err := m.run("systemctl", "disable", "--now", unit); err != nil {
			return fmt.Errorf("sshaccess: stop %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if out, err := m.run("systemctl", "enable", "--now", unit); err != nil {
		return fmt.Errorf("sshaccess: start %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	if out, err := m.run("systemctl", "reload", unit); err != nil {
		return fmt.Errorf("sshaccess: reload %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}
