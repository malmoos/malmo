package lifecycle

// BYO outgoing mail (SERVICE_PROVISIONING.md # BYO outgoing mail). The brain
// injects an admin-registered SMTP provider into a bound app's .env as
// MALMO_MAIL_* — writeEnv stamps it at install, RebindMail re-stamps it later.
// No malmo-run relay: the app dials the provider itself over its declared
// internet permission.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/malmoos/malmo/internal/manifest"
	"github.com/malmoos/malmo/internal/store"
)

// mailEnvLines renders a provider as the MALMO_MAIL_* env lines: the discrete
// fields plus a Symfony-style DSN, since apps differ in what they consume.
func mailEnvLines(p store.MailProvider) []string {
	return []string{
		"MALMO_MAIL_HOST=" + p.Host,
		"MALMO_MAIL_PORT=" + strconv.Itoa(p.Port),
		"MALMO_MAIL_USER=" + p.Username,
		"MALMO_MAIL_PASSWORD=" + p.Password,
		"MALMO_MAIL_FROM=" + p.FromAddress,
		"MALMO_MAIL_ENCRYPTION=" + p.Encryption,
		// Boolean projections of the encryption mode, for apps that take two
		// separate flags rather than the string or DSN — STARTTLS vs implicit
		// TLS as distinct booleans (e.g. Django's EMAIL_USE_TLS / EMAIL_USE_SSL,
		// which Paperless surfaces). Compose can't derive these from the string.
		"MALMO_MAIL_USE_TLS=" + strconv.FormatBool(p.Encryption == store.MailEncryptionSTARTTLS),
		"MALMO_MAIL_USE_SSL=" + strconv.FormatBool(p.Encryption == store.MailEncryptionTLS),
		"MALMO_MAIL_DSN=" + mailDSN(p),
	}
}

// mailAppEnvLines resolves each manifest-declared mail.env var to its token for
// the box's current mail state and renders it as `APP_VAR=token` (#302). Unlike
// the MALMO_MAIL_* family — injected only when bound — these are emitted in both
// states, so an app whose mail switch is a boot-validated enum is present and
// valid even unbound (bound == nil): `encryption` reads none, `bound` reads
// unbound. Keys are sorted so a rewrite produces a byte-stable .env. Validation
// guarantees every declared map covers its domain, so the lookup can't miss.
func mailAppEnvLines(mail *manifest.Mail, bound *store.MailProvider) []string {
	if mail == nil || len(mail.Env) == 0 {
		return nil
	}
	names := make([]string, 0, len(mail.Env))
	for name := range mail.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		em := mail.Env[name]
		var domainValue string
		switch em.From {
		case manifest.MailFromEncryption:
			domainValue = store.MailEncryptionNone // unbound ⇒ no encryption
			if bound != nil {
				domainValue = bound.Encryption
			}
		case manifest.MailFromBound:
			domainValue = manifest.MailUnbound
			if bound != nil {
				domainValue = manifest.MailBound
			}
		}
		lines = append(lines, name+"="+em.Map[domainValue])
	}
	return lines
}

// mailDSN renders a provider as a Symfony-style SMTP URL
// (smtp[s]://user:pass@host:port). Implicit TLS is the smtps scheme; starttls
// and none both stay smtp:// — SMTP URL consumers negotiate STARTTLS
// opportunistically, and apps needing the exact mode read the discrete
// MALMO_MAIL_ENCRYPTION var instead. url.URL escapes credentials, so a
// password with @ / : survives the round trip.
func mailDSN(p store.MailProvider) string {
	u := url.URL{Scheme: "smtp", Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.Encryption == store.MailEncryptionTLS {
		u.Scheme = "smtps"
	}
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

// RebindMail changes (or, with providerID == "", clears) an instance's
// outgoing-mail binding and re-stamps the MALMO_MAIL_* lines in its .env. A
// running instance's containers are recreated with `compose up -d` — env is
// read only at container create — so the change takes effect immediately; a
// stopped instance picks it up on its next Start (same op). Brain commits
// first: if the recreate fails the binding and .env already hold the desired
// state and the instance is marked pending-recreate, so the reconcile pass
// brings the container to the new binding — whether it fell over or kept running
// on stale env (the already-up path retries while the marker is set, #268).
func (m *Manager) RebindMail(ctx context.Context, id, providerID string) error {
	defer m.lockInstance(id)()
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	man, err := m.loadInstanceManifest(id)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	// Same backstop as install: only mail-declaring apps can be bound (the API
	// validates first; this keeps the transaction owner authoritative).
	if providerID != "" && man.Mail == nil {
		return fmt.Errorf("%w: %s", ErrNoMailSupport, man.ID)
	}

	if providerID == "" {
		if err := m.store.DeleteInstanceMailBinding(id); err != nil {
			return fmt.Errorf("unbind mail provider: %w", err)
		}
	} else if err := m.store.SetInstanceMailBinding(id, providerID); err != nil {
		return fmt.Errorf("bind mail provider: %w", err)
	}
	if err := m.rewriteEnvMail(id, man.Mail); err != nil {
		return fmt.Errorf("rewrite env: %w", err)
	}

	if inst.State != "running" {
		slog.Info("app mail binding updated (applies at next start)",
			"instance_id", id, "name", inst.Name)
		return nil
	}
	if err := m.recreateRunning(ctx, inst); err != nil {
		return err
	}
	slog.Info("app mail binding updated", "instance_id", id, "name", inst.Name)
	return nil
}

// rewriteEnvMail re-stamps only the mail-owned lines of an instance's .env from
// the current binding — the MALMO_MAIL_* family plus any manifest-declared
// mail.env vars (#302) — leaving every other line byte-identical, so unlike a
// full writeEnv it needs no install-time isolation state and a stable secret
// can't be re-rolled by accident. The declared vars are stamped under the app's
// own names, so they're stripped by name (not the MALMO_MAIL_ prefix) and
// re-resolved for the new state — including unbound, which the MALMO_MAIL_*
// family drops but the declared enum vars keep (with their unbound tokens).
func (m *Manager) rewriteEnvMail(id string, mail *manifest.Mail) error {
	path := filepath.Join(m.instanceDir(id), ".env")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	if mail != nil {
		for name := range mail.Env {
			declared[name] = true
		}
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.HasPrefix(l, "MALMO_MAIL_") {
			continue
		}
		if name, _, ok := strings.Cut(l, "="); ok && declared[name] {
			continue
		}
		kept = append(kept, l)
	}
	mp, err := m.store.GetInstanceMailProvider(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var bound *store.MailProvider
	if err == nil {
		bound = &mp
		kept = append(kept, mailEnvLines(mp)...)
	}
	kept = append(kept, mailAppEnvLines(mail, bound)...)
	env := strings.Join(append(kept, ""), "\n")
	return os.WriteFile(path, []byte(env), 0o644)
}
