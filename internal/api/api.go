// Package api serves the Brain ↔ UI protocol (BRAIN_UI_PROTOCOL.md): REST via
// huma (OpenAPI emitted as a byproduct) plus the raw SSE event stream.
// Skeleton scope: catalog browse, app list/detail, install/uninstall jobs,
// global event stream. Auth is a dev bypass for now (AUTH.md lands later).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/malmo/malmo/internal/admission"
	"github.com/malmo/malmo/internal/audit"
	"github.com/malmo/malmo/internal/auth"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/events"
	"github.com/malmo/malmo/internal/health"
	"github.com/malmo/malmo/internal/hostclient"
	"github.com/malmo/malmo/internal/lifecycle"
	"github.com/malmo/malmo/internal/manifest"
	"github.com/malmo/malmo/internal/store"
)

type Server struct {
	store   *store.Store
	catalog *catalog.Catalog
	life    *lifecycle.Manager
	bus     *events.Bus
	auth    *auth.Manager
	host    *hostclient.Client
	auditor *audit.Recorder
	health  *health.Manager
	jobs    *Jobs
}

func NewServer(
	st *store.Store,
	cat *catalog.Catalog,
	life *lifecycle.Manager,
	bus *events.Bus,
	authMgr *auth.Manager,
	host *hostclient.Client,
	auditor *audit.Recorder,
	healthMgr *health.Manager,
) *Server {
	return &Server{
		store: st, catalog: cat, life: life, bus: bus,
		auth: authMgr, host: host, auditor: auditor,
		health: healthMgr, jobs: newJobs(),
	}
}

// Handler builds the mux: huma-registered REST routes + the raw SSE endpoint.
// The chain is CORS → auth → mux. CORS handles OPTIONS preflight (no auth
// needed); auth gates everything else except the small public allowlist.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("malmo brain", "0.0.1"))
	s.register(api)
	s.registerAuth(api)
	s.registerUsers(api)
	s.registerMeRoutes(api)
	s.registerHealth(api)

	// SSE is registered raw (huma streaming adds no value here and the raw
	// handler keeps the wire format curl-debuggable per BRAIN_UI_PROTOCOL.md).
	mux.HandleFunc("GET /api/v1/events", s.events)

	return withCORS(s.authMiddleware(mux))
}

func (s *Server) register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-catalog", Method: "GET", Path: "/api/v1/catalog",
		Summary: "List installable apps from the catalog",
	}, s.listCatalog)

	huma.Register(api, huma.Operation{
		OperationID: "list-apps", Method: "GET", Path: "/api/v1/apps",
		Summary: "List installed app instances",
	}, s.listApps)

	huma.Register(api, huma.Operation{
		OperationID: "get-app", Method: "GET", Path: "/api/v1/apps/{id}",
		Summary: "Get one app instance",
	}, s.getApp)

	huma.Register(api, huma.Operation{
		OperationID: "install-app", Method: "POST", Path: "/api/v1/apps",
		Summary: "Install an app (job)", DefaultStatus: http.StatusAccepted,
	}, s.installApp)

	huma.Register(api, huma.Operation{
		OperationID: "install-custom-app", Method: "POST", Path: "/api/v1/apps/custom",
		Summary: "Install a user-pasted (Door-2) compose (job)", DefaultStatus: http.StatusAccepted,
	}, s.installCustomApp)

	huma.Register(api, huma.Operation{
		OperationID: "uninstall-app", Method: "DELETE", Path: "/api/v1/apps/{id}",
		Summary: "Uninstall an app (job)", DefaultStatus: http.StatusAccepted,
	}, s.uninstallApp)

	huma.Register(api, huma.Operation{
		OperationID: "get-job", Method: "GET", Path: "/api/v1/jobs/{id}",
		Summary: "Get job status",
	}, s.getJob)
}

// --- DTOs ----------------------------------------------------------------

type InstanceDTO struct {
	ID            string `json:"id"`
	ManifestID    string `json:"manifest_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Version       string `json:"version"`
	State         string `json:"state"`
	URL           string `json:"url"`
	OwnerUserID   string `json:"owner_user_id"`
	OwnerUsername string `json:"owner_username"`
	Scope         string `json:"scope"`
}

func toDTO(i store.Instance, ownerUsername string) InstanceDTO {
	url := ""
	if i.Slug != "" {
		url = "http://" + i.Slug + ".malmo.local"
	}
	return InstanceDTO{
		ID: i.ID, ManifestID: i.ManifestID, Name: i.Name, Slug: i.Slug,
		Version: i.Version, State: i.State, URL: url,
		OwnerUserID: i.OwnerUserID, OwnerUsername: ownerUsername, Scope: i.Scope,
	}
}

// --- handlers ------------------------------------------------------------

func (s *Server) listCatalog(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		Apps []catalog.Entry `json:"apps"`
	}
}, error) {
	apps, err := s.catalog.List()
	if err != nil {
		return nil, huma.Error500InternalServerError("catalog read failed", err)
	}
	out := &struct {
		Body struct {
			Apps []catalog.Entry `json:"apps"`
		}
	}{}
	out.Body.Apps = apps
	return out, nil
}

func (s *Server) listApps(ctx context.Context, _ *struct{}) (*struct {
	Body struct {
		Apps []InstanceDTO `json:"apps"`
	}
}, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	insts, err := s.store.ListVisibleTo(id.User.ID, id.IsAdmin())
	if err != nil {
		return nil, huma.Error500InternalServerError("list failed", err)
	}
	names, err := s.ownerUsernames()
	if err != nil {
		return nil, huma.Error500InternalServerError("owner lookup failed", err)
	}
	out := &struct {
		Body struct {
			Apps []InstanceDTO `json:"apps"`
		}
	}{}
	out.Body.Apps = []InstanceDTO{}
	for _, i := range insts {
		out.Body.Apps = append(out.Body.Apps, toDTO(i, names[i.OwnerUserID]))
	}
	return out, nil
}

func (s *Server) getApp(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{ Body InstanceDTO }, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	i, err := s.store.Get(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such app")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get failed", err)
	}
	// Leak guard: a member asking for someone else's personal instance gets 404
	// (not 403), so existence of another user's app isn't disclosed.
	if !canSee(id, i) {
		return nil, huma.Error404NotFound("no such app")
	}
	owner, err := s.store.GetUser(i.OwnerUserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error500InternalServerError("owner lookup failed", err)
	}
	return &struct{ Body InstanceDTO }{Body: toDTO(i, owner.Username)}, nil
}

// canSee mirrors store.ListVisibleTo's visibility predicate for a single
// instance: admins see all; members see household instances and their own
// personal instances. KEEP IN SYNC with store.ListVisibleTo's SQL WHERE clause
// and with checkDuplicate/uninstallApp's callers — when per-app member grants
// land (NEXT.md), both this and the SQL must change together.
func canSee(id auth.Identity, i store.Instance) bool {
	return id.IsAdmin() || i.Scope == store.ScopeHousehold || i.OwnerUserID == id.User.ID
}

// ownerUsernames maps user id -> username for rendering instance ownership in
// the list response. One query; N is small at v1 user counts.
func (s *Server) ownerUsernames() (map[string]string, error) {
	users, err := s.store.ListUsers()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(users))
	for _, u := range users {
		m[u.ID] = u.Username
	}
	return m, nil
}

func (s *Server) installApp(ctx context.Context, in *struct {
	Body struct {
		ManifestID string `json:"manifest_id"`
		Scope      string `json:"scope,omitempty"`   // "household" | "personal"; default household for admins, forced personal for members
		Confirm    bool   `json:"confirm,omitempty"` // proceed past the duplicate-install warning
	}
}) (*struct{ Body Job }, error) {
	manifestID := in.Body.ManifestID
	if manifestID == "" {
		return nil, huma.Error422UnprocessableEntity("manifest_id is required")
	}
	owner, scope, err := s.resolveOwnerScope(ctx, in.Body.Scope, audit.ActionAppInstall, map[string]any{"manifest_id": manifestID})
	if err != nil {
		return nil, err
	}
	if err := s.checkDuplicate(ctx, manifestID, in.Body.Confirm, audit.ActionAppInstall); err != nil {
		return nil, err
	}
	jobCtx := ctx // capture for audit inside the job goroutine
	job := s.jobs.run("app-install", func(job *Job) (map[string]any, error) {
		inst, err := s.life.Install(context.Background(), manifestID, owner, scope, job.setStep)
		target := audit.Target{Kind: "app"}
		// confirm records a deliberate override of the duplicate-install warning,
		// so the Activity view can see "installed a second copy on purpose".
		meta := map[string]any{"manifest_id": manifestID, "scope": scope, "owner_user_id": owner.UserID, "confirm": in.Body.Confirm}
		if err == nil {
			target.ID = inst.ID
			meta["slug"] = inst.Slug
		}
		s.auditor.Record(jobCtx, audit.ActionAppInstall, target, meta, err == nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance_id": inst.ID, "slug": inst.Slug}, nil
	})
	return &struct{ Body Job }{Body: job.snapshot()}, nil
}

// resolveOwnerScope applies the install authorization table (DASHBOARD.md #
// install authorization): members always install a personal instance owned by
// themselves; admins choose household (the default) or personal. It audits a
// failed attempt when a member explicitly requests a household install.
// meta is the action-specific audit metadata (e.g. {"manifest_id": …} for a
// catalog install, {"name": …} for a custom one); resolveOwnerScope adds the
// requested scope before recording a rejected attempt.
func (s *Server) resolveOwnerScope(ctx context.Context, requested, action string, meta map[string]any) (lifecycle.Owner, string, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return lifecycle.Owner{}, "", huma.Error401Unauthorized("unauthenticated")
	}
	owner := lifecycle.Owner{UserID: id.User.ID, Username: id.User.Username}
	scope := requested
	if id.IsAdmin() {
		if scope == "" {
			scope = store.ScopeHousehold
		}
	} else {
		if scope == store.ScopeHousehold {
			meta["scope"] = scope
			s.auditor.Record(ctx, action, audit.Target{Kind: "app"}, meta, false)
			return lifecycle.Owner{}, "", huma.Error403Forbidden("members can only install personal instances")
		}
		scope = store.ScopePersonal
	}
	if scope != store.ScopeHousehold && scope != store.ScopePersonal {
		return lifecycle.Owner{}, "", huma.Error422UnprocessableEntity("scope must be household or personal")
	}
	return owner, scope, nil
}

// checkDuplicate implements warn-don't-block (DASHBOARD.md # warn, don't block).
// This is an API-layer UX guarantee, NOT a lifecycle invariant: lifecycle.Install
// does not enforce it, so any non-API caller (a CLI, reconciler, import script)
// installs without the warning by design. Duplicate installs are always allowed;
// this only gates the interactive confirm step.
// When an instance of this manifest already exists that the caller could see
// (a household instance or their own personal one) and they haven't confirmed,
// return 409 "duplicate-install" carrying a summary of the existing copies so
// the UI can offer "open it" or "install my own copy". A confirmed retry skips
// the check.
func (s *Server) checkDuplicate(ctx context.Context, manifestID string, confirm bool, action string) error {
	if confirm {
		return nil
	}
	id, _ := auth.FromContext(ctx)
	existing, err := s.store.InstancesByManifest(manifestID)
	if err != nil {
		return huma.Error500InternalServerError("duplicate check failed", err)
	}
	var summaries []error
	for _, i := range existing {
		if !canSee(id, i) {
			continue
		}
		if i.Scope == store.ScopeHousehold {
			summaries = append(summaries, fmt.Errorf("%s is already installed as a household app", i.Name))
		} else {
			summaries = append(summaries, fmt.Errorf("%s is already installed as your personal app", i.Name))
		}
	}
	if len(summaries) == 0 {
		return nil
	}
	s.auditor.Record(ctx, action, audit.Target{Kind: "app"},
		map[string]any{"manifest_id": manifestID, "duplicate": true}, false)
	return huma.Error409Conflict("duplicate-install", summaries...)
}

func (s *Server) installCustomApp(ctx context.Context, in *struct {
	Body struct {
		Name        string `json:"name"`
		Compose     string `json:"compose"`
		MainService string `json:"main_service,omitempty"`
		MainPort    int    `json:"main_port"`
		Scope       string `json:"scope,omitempty"` // "household" | "personal"; default household for admins, forced personal for members
	}
}) (*struct{ Body Job }, error) {
	spec := lifecycle.CustomSpec{
		Name:        in.Body.Name,
		Compose:     in.Body.Compose,
		MainService: in.Body.MainService,
		MainPort:    in.Body.MainPort,
	}
	owner, scope, err := s.resolveOwnerScope(ctx, in.Body.Scope, audit.ActionAppCustomCreate, map[string]any{"name": spec.Name})
	if err != nil {
		return nil, err
	}

	// Sync pre-checks so the user gets immediate, specific feedback instead of
	// a failed job: synthesize (catches missing name/port, ambiguous service)
	// and admit the compose (catches ports:/privileged/etc).
	if _, _, err := manifest.Synthesize(spec.Name, []byte(spec.Compose), spec.MainService, spec.MainPort); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	if err := admission.Check(ctx, []byte(spec.Compose)); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	jobCtx := ctx
	job := s.jobs.run("app-install", func(job *Job) (map[string]any, error) {
		inst, err := s.life.InstallCustom(context.Background(), spec, owner, scope, job.setStep)
		target := audit.Target{Kind: "app"}
		meta := map[string]any{"name": spec.Name, "scope": scope, "owner_user_id": owner.UserID}
		if err == nil {
			target.ID = inst.ID
			meta["slug"] = inst.Slug
		}
		s.auditor.Record(jobCtx, audit.ActionAppCustomCreate, target, meta, err == nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance_id": inst.ID, "slug": inst.Slug}, nil
	})
	return &struct{ Body Job }{Body: job.snapshot()}, nil
}

func (s *Server) uninstallApp(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{ Body Job }, error) {
	id := in.ID
	actor, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	inst, err := s.store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such app")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get failed", err)
	}
	// Leak guard, then authorization: a member can uninstall only their own
	// personal instances; household instances are admin-only (DASHBOARD.md #
	// install authorization — uninstall mirrors it).
	if !canSee(actor, inst) {
		return nil, huma.Error404NotFound("no such app")
	}
	if !actor.IsAdmin() && inst.Scope == store.ScopeHousehold {
		s.auditor.Record(ctx, audit.ActionAppUninstall, audit.Target{Kind: "app", ID: id}, nil, false)
		return nil, huma.Error403Forbidden("only an admin can uninstall a household app")
	}
	jobCtx := ctx
	job := s.jobs.run("app-uninstall", func(job *Job) (map[string]any, error) {
		job.setStep("tearing_down")
		err := s.life.Uninstall(context.Background(), id)
		s.auditor.Record(jobCtx, audit.ActionAppUninstall, audit.Target{Kind: "app", ID: id}, nil, err == nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"instance_id": id}, nil
	})
	return &struct{ Body Job }{Body: job.snapshot()}, nil
}

func (s *Server) getJob(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{ Body Job }, error) {
	j, ok := s.jobs.get(in.ID)
	if !ok {
		return nil, huma.Error404NotFound("no such job")
	}
	return &struct{ Body Job }{Body: j.snapshot()}, nil
}

// events is the global SSE stream (BRAIN_UI_PROTOCOL.md Pattern C, stream 2).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.bus.Subscribe()
	defer unsub()

	// Initial comment so proxies flush headers immediately.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Kind, data)
			flusher.Flush()
		}
	}
}

// withCORS lets the Vite dev server (different origin) call the brain during
// development. Tightened to same-origin behind Caddy in production.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
