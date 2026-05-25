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
	ID         string `json:"id"`
	ManifestID string `json:"manifest_id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Version    string `json:"version"`
	State      string `json:"state"`
	URL        string `json:"url"`
}

func toDTO(i store.Instance) InstanceDTO {
	url := ""
	if i.Slug != "" {
		url = "http://" + i.Slug + ".malmo.local"
	}
	return InstanceDTO{
		ID: i.ID, ManifestID: i.ManifestID, Name: i.Name, Slug: i.Slug,
		Version: i.Version, State: i.State, URL: url,
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
	insts, err := s.store.List()
	if err != nil {
		return nil, huma.Error500InternalServerError("list failed", err)
	}
	out := &struct {
		Body struct {
			Apps []InstanceDTO `json:"apps"`
		}
	}{}
	out.Body.Apps = []InstanceDTO{}
	for _, i := range insts {
		out.Body.Apps = append(out.Body.Apps, toDTO(i))
	}
	return out, nil
}

func (s *Server) getApp(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{ Body InstanceDTO }, error) {
	i, err := s.store.Get(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such app")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get failed", err)
	}
	return &struct{ Body InstanceDTO }{Body: toDTO(i)}, nil
}

func (s *Server) installApp(ctx context.Context, in *struct {
	Body struct {
		ManifestID string `json:"manifest_id"`
	}
}) (*struct{ Body Job }, error) {
	manifestID := in.Body.ManifestID
	if manifestID == "" {
		return nil, huma.Error422UnprocessableEntity("manifest_id is required")
	}
	jobCtx := ctx // capture for audit inside the job goroutine
	job := s.jobs.run("app-install", func(job *Job) (map[string]any, error) {
		inst, err := s.life.Install(context.Background(), manifestID, job.setStep)
		target := audit.Target{Kind: "app"}
		meta := map[string]any{"manifest_id": manifestID}
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

func (s *Server) installCustomApp(ctx context.Context, in *struct {
	Body struct {
		Name        string `json:"name"`
		Compose     string `json:"compose"`
		MainService string `json:"main_service,omitempty"`
		MainPort    int    `json:"main_port"`
	}
}) (*struct{ Body Job }, error) {
	spec := lifecycle.CustomSpec{
		Name:        in.Body.Name,
		Compose:     in.Body.Compose,
		MainService: in.Body.MainService,
		MainPort:    in.Body.MainPort,
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
		inst, err := s.life.InstallCustom(context.Background(), spec, job.setStep)
		target := audit.Target{Kind: "app"}
		meta := map[string]any{"name": spec.Name}
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
	if _, err := s.store.Get(id); errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound("no such app")
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
