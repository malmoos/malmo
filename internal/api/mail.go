package api

// Outgoing-mail provider management (SERVICE_PROVISIONING.md # BYO outgoing
// mail, SETTINGS.md). Provider CRUD is admin-only and elevation-class; the
// per-app binding endpoint follows the stop/start authorization (a member may
// rebind their own personal instance). Passwords are write-only: requests
// carry them, responses never do.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/audit"
	"github.com/malmoos/malmo/internal/auth"
	"github.com/malmoos/malmo/internal/lifecycle"
	"github.com/malmoos/malmo/internal/store"
)

// testMailTimeout bounds the whole synchronous test-send (dial, handshake,
// auth, send) so a black-holed SMTP host returns a clean error, not a hung
// request.
const testMailTimeout = 15 * time.Second

func (s *Server) registerMail(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-mail-providers", Method: "GET", Path: "/api/v1/mail-providers",
		Summary: "List outgoing-mail providers (admin only)",
	}, s.listMailProviders)

	huma.Register(api, huma.Operation{
		OperationID: "create-mail-provider", Method: "POST", Path: "/api/v1/mail-providers",
		Summary: "Register an outgoing-mail provider (admin only)",
	}, s.createMailProvider)

	huma.Register(api, huma.Operation{
		OperationID: "update-mail-provider", Method: "PUT", Path: "/api/v1/mail-providers/{id}",
		Summary: "Update an outgoing-mail provider (admin only)",
	}, s.updateMailProvider)

	huma.Register(api, huma.Operation{
		OperationID: "delete-mail-provider", Method: "DELETE", Path: "/api/v1/mail-providers/{id}",
		Summary: "Delete an outgoing-mail provider (admin only; bound apps fall back to unbound)", DefaultStatus: 204,
	}, s.deleteMailProvider)

	huma.Register(api, huma.Operation{
		OperationID: "test-mail-provider", Method: "POST", Path: "/api/v1/mail-providers/{id}/test",
		Summary: "Send a test email through a provider (admin only)", DefaultStatus: 204,
	}, s.testMailProvider)

	huma.Register(api, huma.Operation{
		OperationID: "list-mail-provider-options", Method: "GET", Path: "/api/v1/mail-providers/options",
		Summary: "List provider picker options — id and label only (any authenticated user)",
	}, s.listMailProviderOptions)

	huma.Register(api, huma.Operation{
		OperationID: "set-app-mail-binding", Method: "PUT", Path: "/api/v1/apps/{id}/mail-binding",
		Summary: "Bind an app to an outgoing-mail provider (empty provider_id unbinds)",
	}, s.setAppMailBinding)
}

// MailProviderDTO is the read shape of a provider. The password is write-only
// (requests carry it; no response ever echoes it).
type MailProviderDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	FromAddress string `json:"from_address"`
	Encryption  string `json:"encryption" enum:"none,starttls,tls"`
	CreatedAt   int64  `json:"created_at"`
}

func mailProviderDTO(p store.MailProvider) MailProviderDTO {
	return MailProviderDTO{
		ID: p.ID, Label: p.Label, Host: p.Host, Port: p.Port,
		Username: p.Username, FromAddress: p.FromAddress,
		Encryption: p.Encryption, CreatedAt: p.CreatedAt.Unix(),
	}
}

// MailProviderBody is the create/update request shape. On update an empty
// password keeps the stored one, so an admin can edit the host or label
// without re-entering the credential.
type MailProviderBody struct {
	Label       string `json:"label"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	FromAddress string `json:"from_address"`
	Encryption  string `json:"encryption" enum:"none,starttls,tls"`
}

// validateMailProviderBody normalizes and checks the shared create/update
// fields; validation failures are plain 422s and do not audit.
func validateMailProviderBody(b *MailProviderBody) error {
	b.Label = strings.TrimSpace(b.Label)
	b.Host = strings.TrimSpace(b.Host)
	b.FromAddress = strings.TrimSpace(b.FromAddress)
	switch {
	case b.Label == "":
		return huma.Error422UnprocessableEntity("label is required")
	case b.Host == "":
		return huma.Error422UnprocessableEntity("host is required")
	case b.Port < 1 || b.Port > 65535:
		return huma.Error422UnprocessableEntity("port must be 1-65535")
	case b.FromAddress == "" || !strings.Contains(b.FromAddress, "@"):
		return huma.Error422UnprocessableEntity("from_address must be an email address")
	}
	// A newline in any of these reaches a MALMO_MAIL_* .env line (one field per
	// line) and the test-send's SMTP commands / RFC 5322 headers, so a CRLF would
	// let one field smuggle extra env lines, SMTP commands, or mail headers.
	// Reject at the boundary — none of these fields legitimately span lines.
	for _, f := range []string{b.Label, b.Host, b.Username, b.Password, b.FromAddress} {
		if strings.ContainsAny(f, "\r\n") {
			return huma.Error422UnprocessableEntity("fields must not contain line breaks")
		}
	}
	switch b.Encryption {
	case store.MailEncryptionNone, store.MailEncryptionSTARTTLS, store.MailEncryptionTLS:
		return nil
	default:
		return huma.Error422UnprocessableEntity("encryption must be none, starttls, or tls")
	}
}

func (s *Server) listMailProviders(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		Providers []MailProviderDTO `json:"providers"`
	}
}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	providers, err := s.store.ListMailProviders()
	if err != nil {
		return nil, huma.Error500InternalServerError("list mail providers failed", err)
	}
	out := &struct {
		Body struct {
			Providers []MailProviderDTO `json:"providers"`
		}
	}{}
	out.Body.Providers = []MailProviderDTO{}
	for _, p := range providers {
		out.Body.Providers = append(out.Body.Providers, mailProviderDTO(p))
	}
	return out, nil
}

// listMailProviderOptions is the non-admin sibling of listMailProviders: id +
// label only, for the rebind picker on an app detail page (a member may rebind
// their own personal instance, so the names must be readable without admin).
// The install dialog's picker rides the install plan instead.
func (s *Server) listMailProviderOptions(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		Providers []MailProviderOption `json:"providers"`
	}
}, error) {
	if _, ok := auth.FromContext(ctx); !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	providers, err := s.store.ListMailProviders()
	if err != nil {
		return nil, huma.Error500InternalServerError("list mail providers failed", err)
	}
	out := &struct {
		Body struct {
			Providers []MailProviderOption `json:"providers"`
		}
	}{}
	out.Body.Providers = []MailProviderOption{}
	for _, p := range providers {
		out.Body.Providers = append(out.Body.Providers, MailProviderOption{ID: p.ID, Label: p.Label})
	}
	return out, nil
}

func (s *Server) createMailProvider(ctx context.Context, in *struct {
	Body MailProviderBody
}) (*struct{ Body MailProviderDTO }, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := requireElevated(ctx); err != nil {
		return nil, err
	}
	if err := validateMailProviderBody(&in.Body); err != nil {
		return nil, err
	}

	p := store.MailProvider{
		ID: newID(), Label: in.Body.Label, Host: in.Body.Host, Port: in.Body.Port,
		Username: in.Body.Username, Password: in.Body.Password,
		FromAddress: in.Body.FromAddress, Encryption: in.Body.Encryption,
		CreatedAt: time.Now(),
	}
	meta := map[string]any{"label": p.Label, "host": p.Host}
	if err := s.store.CreateMailProvider(p); err != nil {
		s.auditor.Record(ctx, audit.ActionMailProviderCreate, audit.Target{Kind: "mail_provider"}, meta, false)
		if errors.Is(err, store.ErrConflict) {
			return nil, huma.Error409Conflict("a provider with that label already exists")
		}
		return nil, huma.Error500InternalServerError("create mail provider failed", err)
	}
	s.auditor.Record(ctx, audit.ActionMailProviderCreate, audit.Target{Kind: "mail_provider", ID: p.ID}, meta, true)
	return &struct{ Body MailProviderDTO }{Body: mailProviderDTO(p)}, nil
}

func (s *Server) updateMailProvider(ctx context.Context, in *struct {
	ID   string `path:"id"`
	Body MailProviderBody
}) (*struct{ Body MailProviderDTO }, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := requireElevated(ctx); err != nil {
		return nil, err
	}
	if err := validateMailProviderBody(&in.Body); err != nil {
		return nil, err
	}

	existing, err := s.store.GetMailProvider(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such mail provider")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get mail provider failed", err)
	}

	p := existing
	p.Label, p.Host, p.Port = in.Body.Label, in.Body.Host, in.Body.Port
	p.Username, p.FromAddress, p.Encryption = in.Body.Username, in.Body.FromAddress, in.Body.Encryption
	if in.Body.Password != "" {
		p.Password = in.Body.Password
	}
	tgt := audit.Target{Kind: "mail_provider", ID: p.ID}
	meta := map[string]any{"label": p.Label, "host": p.Host}
	if err := s.store.UpdateMailProvider(p); err != nil {
		s.auditor.Record(ctx, audit.ActionMailProviderUpdate, tgt, meta, false)
		if errors.Is(err, store.ErrConflict) {
			return nil, huma.Error409Conflict("a provider with that label already exists")
		}
		return nil, huma.Error500InternalServerError("update mail provider failed", err)
	}

	// A provider edit changes what bound apps should be sending with, but their
	// .env still carries the old values until each is re-stamped. Bound apps
	// pick the change up on their next rebind/recreate; v1 accepts that lag.
	s.auditor.Record(ctx, audit.ActionMailProviderUpdate, tgt, meta, true)
	return &struct{ Body MailProviderDTO }{Body: mailProviderDTO(p)}, nil
}

func (s *Server) deleteMailProvider(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := requireElevated(ctx); err != nil {
		return nil, err
	}

	tgt := audit.Target{Kind: "mail_provider", ID: in.ID}
	if err := s.store.DeleteMailProvider(in.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, huma.Error404NotFound("no such mail provider")
		}
		s.auditor.Record(ctx, audit.ActionMailProviderDelete, tgt, nil, false)
		return nil, huma.Error500InternalServerError("delete mail provider failed", err)
	}
	s.auditor.Record(ctx, audit.ActionMailProviderDelete, tgt, nil, true)
	return nil, nil
}

func (s *Server) testMailProvider(ctx context.Context, in *struct {
	ID   string `path:"id"`
	Body struct {
		To string `json:"to"`
	}
}) (*struct{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	to := strings.TrimSpace(in.Body.To)
	if to == "" || !strings.Contains(to, "@") || strings.ContainsAny(to, "\r\n") {
		return nil, huma.Error422UnprocessableEntity("to must be an email address")
	}

	p, err := s.store.GetMailProvider(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such mail provider")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get mail provider failed", err)
	}

	tgt := audit.Target{Kind: "mail_provider", ID: p.ID}
	meta := map[string]any{"label": p.Label, "host": p.Host}
	sendCtx, cancel := context.WithTimeout(ctx, testMailTimeout)
	defer cancel()
	if err := sendTestMail(sendCtx, p, to); err != nil {
		s.auditor.Record(ctx, audit.ActionMailProviderTest, tgt, meta, false)
		return nil, huma.Error502BadGateway(fmt.Sprintf("test send failed: %v", err))
	}
	s.auditor.Record(ctx, audit.ActionMailProviderTest, tgt, meta, true)
	return nil, nil
}

func (s *Server) setAppMailBinding(ctx context.Context, in *struct {
	ID   string `path:"id"`
	Body struct {
		ProviderID string `json:"provider_id"` // empty unbinds
	}
}) (*struct{ Body Job }, error) {
	id := in.ID
	inst, err := s.authorizeAppMutation(ctx, id)
	if err != nil {
		return nil, err
	}
	tgt := audit.Target{Kind: "app", ID: id}
	providerID := in.Body.ProviderID
	meta := map[string]any{"provider_id": providerID}
	if providerID != "" {
		// Reject a binding on a non-mail app synchronously, mirroring the install
		// path — without this a direct API caller (the UI hides the picker for
		// non-mail apps) gets a 200 + job that only fails later in RebindMail. The
		// manifest comes from the catalog; a withdrawn app falls through to that
		// backstop, same as getApp's picker enrichment.
		if man, _, err := s.catalog.Load(inst.ManifestID); err == nil && man.Mail == nil {
			s.auditor.Record(ctx, audit.ActionAppMailRebind, tgt, meta, false)
			return nil, huma.Error422UnprocessableEntity("this app does not support outgoing email")
		}
		if _, err := s.store.GetMailProvider(providerID); errors.Is(err, store.ErrNotFound) {
			s.auditor.Record(ctx, audit.ActionAppMailRebind, tgt, meta, false)
			return nil, huma.Error422UnprocessableEntity("no such mail provider")
		} else if err != nil {
			return nil, huma.Error500InternalServerError("get mail provider failed", err)
		}
	}

	jobCtx := ctx
	job := s.jobs.run("app-mail-rebind", func(job *Job) (map[string]any, error) {
		job.setStep("rebinding")
		err := s.life.RebindMail(context.Background(), id, providerID)
		s.auditor.Record(jobCtx, audit.ActionAppMailRebind, tgt, meta, err == nil)
		if err != nil {
			if errors.Is(err, lifecycle.ErrNoMailSupport) {
				return nil, fmt.Errorf("this app does not support outgoing email")
			}
			return nil, err
		}
		return map[string]any{"instance_id": id}, nil
	})
	return &struct{ Body Job }{Body: job.snapshot()}, nil
}

// sendTestMail dials the provider and delivers a short test message to `to`,
// exercising exactly what a bound app would use: TCP (or implicit TLS) to
// host:port, optional STARTTLS, optional AUTH PLAIN, then one DATA round trip.
func sendTestMail(ctx context.Context, p store.MailProvider, to string) error {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	var conn net.Conn
	var err error
	dialer := &net.Dialer{}
	if p.Encryption == store.MailEncryptionTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	// The smtp.Client has no context support; the connection deadline bounds
	// every subsequent command instead.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	c, err := smtp.NewClient(conn, p.Host)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	defer c.Close()
	if p.Encryption == store.MailEncryptionSTARTTLS {
		if err := c.StartTLS(&tls.Config{ServerName: p.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if p.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", p.Username, p.Password, p.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(p.FromAddress); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: malmo test email\r\nDate: %s\r\n\r\n"+
		"This is a test email from your malmo box — outgoing-mail provider %q is working.\r\n",
		p.FromAddress, to, time.Now().Format(time.RFC1123Z), p.Label)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("send body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return c.Quit()
}
