package hostagent

import (
	"encoding/json"
	"log/slog"
	"os"
)

// fakeState is the on-disk shape of the fake agent's user maps. Password
// hashes are []byte, which encoding/json renders as base64 — fine for a dev
// stand-in for /etc/shadow. Only the fake binary (UserMgr == nil) ever writes
// this; the real agent's source of truth is /etc/shadow.
type fakeState struct {
	Passwords map[string][]byte `json:"passwords"`
	Roles     map[string]string `json:"roles"`
}

// EnablePersistence backs the fake agent's passwords+roles maps with a JSON
// file at path so accounts survive a restart. Without it, a `make dev` restart
// wipes every password while the brain's SQLite keeps the user + session rows,
// so a fresh login (after clearing cookies) fails with "password not
// recognized" even though the user still exists — the bug this closes. See
// docs/dev/running-locally.md # Where state lives.
//
// Only meaningful on the fake path; cmd/host-agent-real never calls it. Loads
// any existing state from path immediately; setPassword/setRole/deleteUser then
// write through via persistLocked.
func (a *Agent) EnablePersistence(path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statePath = path
	return a.loadStateLocked()
}

// loadStateLocked reads statePath into the in-memory maps. A missing file is
// not an error (first run). Caller must hold a.mu.
func (a *Agent) loadStateLocked() error {
	data, err := os.ReadFile(a.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var st fakeState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Passwords != nil {
		a.passwords = st.Passwords
	}
	if st.Roles != nil {
		a.roles = st.Roles
	}
	slog.Info("host-agent (fake) loaded persisted user state", "path", a.statePath, "users", len(a.passwords))
	return nil
}

// persistLocked atomically writes the current maps to statePath. A no-op when
// persistence is disabled (statePath == ""). Failures are logged, not
// propagated: the in-memory mutation already succeeded, and this is a dev fake
// — a write error must not fail the handler. Caller must hold a.mu.
func (a *Agent) persistLocked() {
	if a.statePath == "" {
		return
	}
	data, err := json.Marshal(fakeState{Passwords: a.passwords, Roles: a.roles})
	if err != nil {
		slog.Error("host-agent (fake) marshal user state", "err", err)
		return
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Error("host-agent (fake) write user state", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, a.statePath); err != nil {
		slog.Error("host-agent (fake) rename user state", "path", a.statePath, "err", err)
	}
}
