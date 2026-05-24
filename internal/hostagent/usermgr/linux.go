// Package usermgr implements hostagent.UserManager backed by Linux shell-outs
// (useradd, chpasswd). It is intentionally isolated so that the shared
// internal/hostagent package has no /etc/shadow dependency; only
// cmd/host-agent-real imports it.
package usermgr

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// LinuxUserManager implements hostagent.UserManager against the local system.
//
// Behavior of UpsertPassword:
//   - Looks up the user via user.Lookup.
//   - If missing: shells out to `useradd --create-home --shell <Shell>
//     --gid <PrimaryGroup> <slug>` to create the account.
//   - Sets the password via `chpasswd` reading "<slug>:<password>\n" on stdin.
//     chpasswd goes through PAM, which honors Samba sync if the host has
//     `unix password sync = yes` configured (see AUTH.md # Samba password
//     backend).
//
// UID assignment: relies on /etc/login.defs (UID_MIN ≥ 3000 on a malmo OS,
// per FIRST_RUN.md # Identity & display names). Not forced via -u so the
// system picks the next free UID in range.
//
// Use-case folder creation (Photos/, Documents/, ...) is NOT done here —
// that's STORAGE.md territory and tracked as a follow-up in
// docs/progress/0015-host-agent-set-password.md.
type LinuxUserManager struct {
	// Shell is the login shell to assign to new users. Empty → "/bin/bash".
	Shell string
	// PrimaryGroup is the primary group for new users. Empty → "malmo".
	// The host is expected to already have this group present (provisioned
	// by the box build, not by host-agent).
	PrimaryGroup string
	// AdminGroup is the Linux group that conveys sudo / shell-rescue
	// capability. Empty → "sudo" (per USERS_AND_GROUPS.md # Roles and the
	// CLAUDE.md "admins in `sudo`" load-bearing decision).
	AdminGroup string
}

func (m *LinuxUserManager) adminGroup() string {
	if m.AdminGroup == "" {
		return "sudo"
	}
	return m.AdminGroup
}

// UpsertPassword implements hostagent.UserManager.
func (m *LinuxUserManager) UpsertPassword(slug, password string) error {
	if slug == "" {
		return fmt.Errorf("usermgr: empty slug")
	}
	_, lookupErr := user.Lookup(slug)
	if lookupErr != nil {
		// user.UnknownUserError is the only error we treat as "create";
		// any other lookup failure (e.g., nss config broken) should fail loud.
		var unknown user.UnknownUserError
		if !errors.As(lookupErr, &unknown) {
			return fmt.Errorf("usermgr: lookup %q: %w", slug, lookupErr)
		}
		if err := runCmd("useradd", m.useraddArgs(slug)...); err != nil {
			return fmt.Errorf("usermgr: useradd %q: %w", slug, err)
		}
	}
	if err := runChpasswd(slug, password); err != nil {
		return fmt.Errorf("usermgr: chpasswd %q: %w", slug, err)
	}
	return nil
}

// useraddArgs builds the argument list for the useradd shell-out. Exposed for
// unit testing — the integration test exercises the real command.
func (m *LinuxUserManager) useraddArgs(slug string) []string {
	shell := m.Shell
	if shell == "" {
		shell = "/bin/bash"
	}
	gid := m.PrimaryGroup
	if gid == "" {
		gid = "malmo"
	}
	return []string{"--create-home", "--shell", shell, "--gid", gid, slug}
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// SetRole implements hostagent.UserManager. admin → ensure slug is in the
// admin group (gpasswd -a, idempotent on re-add). member → ensure slug is NOT
// in the admin group (gpasswd -d, with a pre-check so demoting a non-member is
// a no-op instead of an error).
//
// Demotion does not kill live SSH sessions for the demoted admin; group
// membership is cached at session start. Accepted in USERS_AND_GROUPS.md #
// Known sharp edges.
func (m *LinuxUserManager) SetRole(slug, role string) error {
	if slug == "" {
		return fmt.Errorf("usermgr: empty slug")
	}
	group := m.adminGroup()
	switch role {
	case "admin":
		if err := runCmd("gpasswd", "-a", slug, group); err != nil {
			return fmt.Errorf("usermgr: gpasswd -a %q %q: %w", slug, group, err)
		}
		return nil
	case "member":
		in, err := isInGroup(slug, group)
		if err != nil {
			return fmt.Errorf("usermgr: check membership %q in %q: %w", slug, group, err)
		}
		if !in {
			return nil
		}
		if err := runCmd("gpasswd", "-d", slug, group); err != nil {
			return fmt.Errorf("usermgr: gpasswd -d %q %q: %w", slug, group, err)
		}
		return nil
	default:
		return fmt.Errorf("usermgr: bad role %q", role)
	}
}

// isInGroup reports whether slug is listed as a member of group in /etc/group.
// Parses the file directly rather than using user.LookupGroupId+gids because
// /etc/group's member list is the authoritative source for supplementary
// memberships like `sudo`, and we don't depend on nsswitch / nscd lookups.
func isInGroup(slug, group string) (bool, error) {
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return false, err
	}
	return parseGroupMembership(b, slug, group), nil
}

// parseGroupMembership is the pure parser, exported for unit testing.
// /etc/group format: name:passwd:gid:user1,user2,...
//
// Returns false for an empty slug — `strings.Split("", ",")` yields `[""]`,
// which would otherwise match the empty member field of a group like
// `malmo:x:3000:` and surprise callers. Upstream guards already reject empty
// slugs, but the helper should be safe in isolation.
func parseGroupMembership(content []byte, slug, group string) bool {
	if slug == "" {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != group {
			continue
		}
		for _, m := range strings.Split(fields[3], ",") {
			if strings.TrimSpace(m) == slug {
				return true
			}
		}
		return false
	}
	return false
}

// DeleteUser implements hostagent.UserManager. Shells out to
// `userdel -r -f <slug>`:
//   - -r removes the home dir + mail spool. Per the v1 product call
//     (docs/progress/0017-host-agent-delete-user.md): a deleted account loses
//     its data with it. The "transfer / preserve files" flow is a future
//     follow-up that will branch BEFORE calling this.
//   - -f forces removal even if the user owns running processes (the SSH
//     session matrix is documented in 0017); without -f userdel exits 8 on a
//     logged-in user, turning routine deletes into 500s.
//
// Idempotent per BRAIN_HOST_PROTOCOL.md # Auth endpoints: unknown user returns
// nil (the brain handler maps that to 200).
//
// nscd caveat: host-agent-real is CGO-built, so user.Lookup goes through
// glibc's getpwnam_r and honors nscd's passwd cache when nscd is running.
// A just-deleted user could still appear cached (we'd call userdel again,
// harmless under -f); a just-created user could appear cached as missing
// (UpsertPassword would re-run useradd → "already exists"). nscd is not
// installed on a stock malmo box; if that ever changes, switch to direct
// /etc/passwd parsing (same shape as isInGroup).
func (m *LinuxUserManager) DeleteUser(slug string) error {
	if slug == "" {
		return fmt.Errorf("usermgr: empty slug")
	}
	if _, err := user.Lookup(slug); err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return fmt.Errorf("usermgr: lookup %q: %w", slug, err)
	}
	if err := runCmd("userdel", "-r", "-f", slug); err != nil {
		return fmt.Errorf("usermgr: userdel %q: %w", slug, err)
	}
	return nil
}

func runChpasswd(slug, password string) error {
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader(slug + ":" + password + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chpasswd: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

