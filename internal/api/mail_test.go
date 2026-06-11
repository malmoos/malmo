package api

// API-boundary tests for outgoing-mail providers: admin + elevation fences,
// write-only passwords, audit on success AND failure (the elevation-class
// mutation rule), the test-send path against an in-process SMTP sink, and the
// synchronous pre-checks of the install and rebind wiring. The lifecycle
// effects of a binding (env stamping, recreate) are covered in
// lifecycle_mail_test.go; like the stop/start tests, nothing here lets a job
// body reach the harness's nil Docker driver.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/molmaos/molma/internal/audit"
	"github.com/molmaos/molma/internal/store"
)

// providerBody returns a valid create/update request body.
func providerBody(label string) map[string]any {
	return map[string]any{
		"label": label, "host": "smtp.example.com", "port": 587,
		"username": "box@example.com", "password": "s3cret-pass",
		"from_address": "box@example.com", "encryption": "starttls",
	}
}

// createProvider creates a provider via the API and returns its DTO.
func (h *harness) createProvider(label string) MailProviderDTO {
	h.t.Helper()
	resp := h.do("POST", "/api/v1/mail-providers", providerBody(label))
	if resp.StatusCode != 200 {
		h.t.Fatalf("create provider %q: %d", label, resp.StatusCode)
	}
	return decodeJSON[MailProviderDTO](h.t, resp)
}

// hasAuditEvent reports whether an audit row with the given action, target id
// (any target when empty) and success flag exists.
func (h *harness) hasAuditEvent(action, targetID string, success bool) bool {
	h.t.Helper()
	events, err := h.st.ListAuditEvents(store.AuditFilter{Limit: 50})
	if err != nil {
		h.t.Fatalf("list audit: %v", err)
	}
	for _, e := range events {
		if e.Action == action && e.Success == success && (targetID == "" || e.TargetID == targetID) {
			return true
		}
	}
	return false
}

// --- provider CRUD ---------------------------------------------------------

func TestMailProvidersAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	// Bodies must pass huma's schema validation, which runs before the
	// handler's admin fence — an off-schema body would 422 instead of 403.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/mail-providers", nil},
		{"POST", "/api/v1/mail-providers", providerBody("x")},
		{"PUT", "/api/v1/mail-providers/mp_x", providerBody("x")},
		{"DELETE", "/api/v1/mail-providers/mp_x", nil},
		{"POST", "/api/v1/mail-providers/mp_x/test", map[string]string{"to": "a@b.c"}},
	} {
		resp := h.do(c.method, c.path, c.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as member = %d; want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestMailProvidersRequireAuth(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	jar, _ := newJar()
	h.jar = jar

	for _, path := range []string{"/api/v1/mail-providers", "/api/v1/mail-providers/options"} {
		resp := h.do("GET", path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s unauthenticated = %d; want 401", path, resp.StatusCode)
		}
	}
}

func TestCreateMailProviderRequiresElevation(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	// No h.elevate — the write must be rejected.

	resp := h.do("POST", "/api/v1/mail-providers", providerBody("fastmail"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create without elevation = %d; want 403", resp.StatusCode)
	}
}

func TestCreateMailProviderHappyPath(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")

	resp := h.do("POST", "/api/v1/mail-providers", providerBody("fastmail"))
	if resp.StatusCode != 200 {
		t.Fatalf("create = %d; want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Passwords are write-only: no response carries the credential.
	if strings.Contains(string(raw), "s3cret-pass") || strings.Contains(string(raw), "password") {
		t.Fatalf("create response leaks the password: %s", raw)
	}

	list := decodeJSON[struct {
		Providers []MailProviderDTO `json:"providers"`
	}](t, h.do("GET", "/api/v1/mail-providers", nil))
	if len(list.Providers) != 1 {
		t.Fatalf("list after create: got %d providers, want 1", len(list.Providers))
	}
	p := list.Providers[0]
	if p.Label != "fastmail" || p.Host != "smtp.example.com" || p.Port != 587 || p.Encryption != "starttls" {
		t.Fatalf("provider round trip = %+v", p)
	}
	// The store kept the credential even though the API never echoes it.
	stored, err := h.st.GetMailProvider(p.ID)
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.Password != "s3cret-pass" {
		t.Fatalf("stored password = %q; want s3cret-pass", stored.Password)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderCreate, p.ID, true) {
		t.Fatal("mail.provider.create success audit event not found")
	}
}

func TestCreateMailProviderDuplicateLabel409(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	h.createProvider("fastmail")

	resp := h.do("POST", "/api/v1/mail-providers", providerBody("fastmail"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate label = %d; want 409", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderCreate, "", false) {
		t.Fatal("mail.provider.create failure audit event not found")
	}
}

func TestCreateMailProviderValidation422(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")

	for name, mutate := range map[string]func(map[string]any){
		"empty label":    func(b map[string]any) { b["label"] = "  " },
		"empty host":     func(b map[string]any) { b["host"] = "" },
		"port zero":      func(b map[string]any) { b["port"] = 0 },
		"port too big":   func(b map[string]any) { b["port"] = 70000 },
		"bad from":       func(b map[string]any) { b["from_address"] = "not-an-email" },
		"bad encryption": func(b map[string]any) { b["encryption"] = "ssl" },
	} {
		b := providerBody("v")
		mutate(b)
		resp := h.do("POST", "/api/v1/mail-providers", b)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d; want 422", name, resp.StatusCode)
		}
	}
}

func TestUpdateMailProviderKeepsPasswordWhenEmpty(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	p := h.createProvider("fastmail")

	body := providerBody("fastmail")
	body["host"] = "smtp2.example.com"
	body["password"] = "" // edit without re-entering the credential
	resp := h.do("PUT", "/api/v1/mail-providers/"+p.ID, body)
	if resp.StatusCode != 200 {
		t.Fatalf("update = %d; want 200", resp.StatusCode)
	}
	updated := decodeJSON[MailProviderDTO](t, resp)
	if updated.Host != "smtp2.example.com" {
		t.Fatalf("updated host = %q; want smtp2.example.com", updated.Host)
	}
	stored, _ := h.st.GetMailProvider(p.ID)
	if stored.Password != "s3cret-pass" {
		t.Fatalf("empty update password overwrote stored one: %q", stored.Password)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderUpdate, p.ID, true) {
		t.Fatal("mail.provider.update success audit event not found")
	}
}

func TestUpdateMailProviderLabelConflict409(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	h.createProvider("fastmail")
	b := h.createProvider("gmail")

	resp := h.do("PUT", "/api/v1/mail-providers/"+b.ID, providerBody("fastmail"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("label conflict = %d; want 409", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderUpdate, b.ID, false) {
		t.Fatal("mail.provider.update failure audit event not found")
	}
}

func TestUpdateMailProviderNotFound(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")

	resp := h.do("PUT", "/api/v1/mail-providers/mp_ghost", providerBody("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update ghost = %d; want 404", resp.StatusCode)
	}
}

func TestDeleteMailProviderHappyPath(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	p := h.createProvider("fastmail")

	resp := h.do("DELETE", "/api/v1/mail-providers/"+p.ID, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d; want 204", resp.StatusCode)
	}
	if _, err := h.st.GetMailProvider(p.ID); err != store.ErrNotFound {
		t.Fatalf("provider still in store after delete: %v", err)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderDelete, p.ID, true) {
		t.Fatal("mail.provider.delete success audit event not found")
	}
}

func TestDeleteMailProviderNotFound(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")

	resp := h.do("DELETE", "/api/v1/mail-providers/mp_ghost", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete ghost = %d; want 404", resp.StatusCode)
	}
}

// --- picker options (non-admin) ---------------------------------------------

func TestListMailProviderOptionsMemberVisible(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	h.createProvider("fastmail")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")

	resp := h.do("GET", "/api/v1/mail-providers/options", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("options as member = %d; want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// id + label only: no host, no credential.
	if strings.Contains(string(raw), "smtp.example.com") || strings.Contains(string(raw), "s3cret-pass") {
		t.Fatalf("options response leaks provider details: %s", raw)
	}
	if !strings.Contains(string(raw), "fastmail") {
		t.Fatalf("options missing provider label: %s", raw)
	}
}

// --- test-send ---------------------------------------------------------------

// smtpSink is a minimal in-process SMTP server, good for one transaction:
// greet, accept EHLO/MAIL/RCPT/DATA, record the envelope.
type smtpSink struct {
	addr             string
	mu               sync.Mutex
	from, rcpt, data string
}

func startSMTPSink(t *testing.T) *smtpSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sink listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &smtpSink{addr: ln.Addr().String()}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		fmt.Fprintf(conn, "220 sink ESMTP\r\n")
		inData := false
		var data strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					s.mu.Lock()
					s.data = data.String()
					s.mu.Unlock()
					fmt.Fprintf(conn, "250 ok\r\n")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				fmt.Fprintf(conn, "250 sink\r\n")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				s.mu.Lock()
				s.from = line
				s.mu.Unlock()
				fmt.Fprintf(conn, "250 ok\r\n")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				s.mu.Lock()
				s.rcpt = line
				s.mu.Unlock()
				fmt.Fprintf(conn, "250 ok\r\n")
			case cmd == "DATA":
				inData = true
				fmt.Fprintf(conn, "354 go\r\n")
			case cmd == "QUIT":
				fmt.Fprintf(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "250 ok\r\n")
			}
		}
	}()
	return s
}

func TestTestMailProviderDeliversThroughSink(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	sink := startSMTPSink(t)
	host, portStr, _ := net.SplitHostPort(sink.addr)
	port, _ := strconv.Atoi(portStr)

	resp := h.do("POST", "/api/v1/mail-providers", map[string]any{
		"label": "sink", "host": host, "port": port,
		"from_address": "molma@example.com", "encryption": "none",
	})
	p := decodeJSON[MailProviderDTO](t, resp)

	resp = h.do("POST", "/api/v1/mail-providers/"+p.ID+"/test", map[string]string{
		"to": "admin@example.com",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test send = %d; want 204", resp.StatusCode)
	}

	sink.mu.Lock()
	from, rcpt, data := sink.from, sink.rcpt, sink.data
	sink.mu.Unlock()
	if !strings.Contains(from, "molma@example.com") {
		t.Errorf("sink MAIL FROM = %q; want molma@example.com", from)
	}
	if !strings.Contains(rcpt, "admin@example.com") {
		t.Errorf("sink RCPT TO = %q; want admin@example.com", rcpt)
	}
	if !strings.Contains(data, "molma test email") {
		t.Errorf("sink DATA missing subject: %q", data)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderTest, p.ID, true) {
		t.Fatal("mail.provider.test success audit event not found")
	}
}

func TestTestMailProviderUnreachable502(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	// A port that was just listening and is now closed: connect refuses fast.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)

	resp := h.do("POST", "/api/v1/mail-providers", map[string]any{
		"label": "dead", "host": host, "port": port,
		"from_address": "molma@example.com", "encryption": "none",
	})
	p := decodeJSON[MailProviderDTO](t, resp)

	resp = h.do("POST", "/api/v1/mail-providers/"+p.ID+"/test", map[string]string{
		"to": "admin@example.com",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("test send to dead host = %d; want 502", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionMailProviderTest, p.ID, false) {
		t.Fatal("mail.provider.test failure audit event not found")
	}
}

func TestTestMailProviderValidation(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	p := h.createProvider("fastmail")

	resp := h.do("POST", "/api/v1/mail-providers/mp_ghost/test", map[string]string{"to": "a@b.c"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("test ghost provider = %d; want 404", resp.StatusCode)
	}

	resp = h.do("POST", "/api/v1/mail-providers/"+p.ID+"/test", map[string]string{"to": "not-an-email"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("test bad to = %d; want 422", resp.StatusCode)
	}
}

// --- app binding (synchronous fences) ----------------------------------------

func TestSetAppMailBindingRequiresAuth(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	jar, _ := newJar()
	h.jar = jar

	resp := h.do("PUT", "/api/v1/apps/i1/mail-binding", map[string]string{"provider_id": "x"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rebind = %d; want 401", resp.StatusCode)
	}
}

func TestSetAppMailBindingUnknownApp404(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")

	resp := h.do("PUT", "/api/v1/apps/ghost/mail-binding", map[string]string{"provider_id": ""})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("rebind unknown app = %d; want 404", resp.StatusCode)
	}
}

func TestSetAppMailBindingMemberCannotControlHousehold(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")
	h.seedInstance("i1", "whoami", "whoami", "u_admin", store.ScopeHousehold)

	resp := h.do("PUT", "/api/v1/apps/i1/mail-binding", map[string]string{"provider_id": "x"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member rebind household = %d; want 403", resp.StatusCode)
	}
}

func TestSetAppMailBindingUnknownProvider422(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.seedInstance("i1", "mailer", "mailer", "u_admin", store.ScopeHousehold)

	resp := h.do("PUT", "/api/v1/apps/i1/mail-binding", map[string]string{"provider_id": "mp_ghost"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rebind to ghost provider = %d; want 422", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionAppMailRebind, "i1", false) {
		t.Fatal("app.mail.rebind failure audit event not found")
	}
}

// A member CAN reach the rebind endpoint for their own personal instance: the
// unknown-provider 422 (not 403/404) proves authorization passed.
func TestSetAppMailBindingMemberOwnPersonal(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alice", "pass1")
	h.addMember("u_bob", "bob", "bobpass")
	h.loginAs("bob", "bobpass")
	h.seedInstance("i1", "mailer", "mailer", "u_bob", store.ScopePersonal)

	resp := h.do("PUT", "/api/v1/apps/i1/mail-binding", map[string]string{"provider_id": "mp_ghost"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("member rebind own personal = %d; want 422", resp.StatusCode)
	}
}

// --- install pre-checks --------------------------------------------------------

const mailManifestYML = `id: mailer
manifest_version: 1
name: Mailer
version: "1.0"
compose_file: compose.yml
main_service: app
main_port: 80
mail:
  optional: true
`

func TestInstallMailProviderOnNonMailApp422(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("POST", "/api/v1/apps", map[string]any{
		"manifest_id": "whoami",
		"config":      map[string]any{"mail_provider_id": "mp_x"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("install non-mail app with provider = %d; want 422", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionAppInstall, "", false) {
		t.Fatal("app.install failure audit event not found")
	}
}

func TestInstallUnknownMailProvider422(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "mailer", mailManifestYML)
	h.setupAdmin("alice", "pass1")

	resp := h.do("POST", "/api/v1/apps", map[string]any{
		"manifest_id": "mailer",
		"config":      map[string]any{"mail_provider_id": "mp_ghost"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("install with ghost provider = %d; want 422", resp.StatusCode)
	}
	if !h.hasAuditEvent(audit.ActionAppInstall, "", false) {
		t.Fatal("app.install failure audit event not found")
	}
}

// The install plan advertises the mail block + registered providers so the
// install dialog can render the picker without an extra request.
func TestInstallPlanCarriesMailProviders(t *testing.T) {
	h := newHarness(t)
	writeManifestFixture(t, h.catalogDir, "mailer", mailManifestYML)
	writeManifestFixture(t, h.catalogDir, "whoami", minimalManifestYML)
	h.setupAdmin("alice", "pass1")
	h.elevate("pass1")
	p := h.createProvider("fastmail")

	resp := h.do("GET", "/api/v1/catalog/mailer/install-plan", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("install plan = %d; want 200", resp.StatusCode)
	}
	plan := decodeJSON[InstallPlanDTO](t, resp)
	if plan.Mail == nil {
		t.Fatal("install plan mail block missing for mail-capable app")
	}
	if !plan.Mail.Optional {
		t.Error("install plan mail.optional = false; want true")
	}
	if len(plan.Mail.Providers) != 1 || plan.Mail.Providers[0].ID != p.ID || plan.Mail.Providers[0].Label != "fastmail" {
		t.Errorf("install plan mail.providers = %+v; want [{%s fastmail}]", plan.Mail.Providers, p.ID)
	}

	// A non-mail app's plan omits the block entirely.
	resp = h.do("GET", "/api/v1/catalog/whoami/install-plan", nil)
	plan = decodeJSON[InstallPlanDTO](t, resp)
	if plan.Mail != nil {
		t.Errorf("non-mail app install plan carries mail block: %+v", plan.Mail)
	}
}
