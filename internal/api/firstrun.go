package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
)

// registerFirstRun registers the box-config endpoints the setup wizard writes as
// it advances past the admin step (FIRST_RUN.md Phase 2): the telemetry-consent
// choice and the first-run-complete marker. Both are admin-only and reachable
// later from Settings (Privacy / the wizard never reappears). The wizard reads
// the profile + completion state from the public /auth/state probe, not here.
func (s *Server) registerFirstRun(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "set-telemetry-consent", Method: "POST", Path: "/api/v1/telemetry/consent",
		Summary: "Set the box-wide telemetry opt-in (admin only)", DefaultStatus: 204,
	}, s.setTelemetryConsent)

	huma.Register(api, huma.Operation{
		OperationID: "complete-first-run", Method: "POST", Path: "/api/v1/first-run/complete",
		Summary: "Mark the setup wizard finished so it never reappears (admin only)", DefaultStatus: 204,
	}, s.completeFirstRun)
}

// setTelemetryConsent records the box-wide telemetry opt-in chosen at first-run
// (FIRST_RUN.md # Step 4, TELEMETRY.md). One toggle covers both the usage and
// crash streams; off by default. Box config, not an elevation-class mutation —
// no audit, no re-elevation. The transmission pipeline is out of scope here;
// this persists the consent the future telemetry client will gate on.
func (s *Server) setTelemetryConsent(ctx context.Context, in *struct {
	Body struct {
		Enabled bool `json:"enabled"`
	}
}) (*struct{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.store.SetTelemetryConsent(in.Body.Enabled); err != nil {
		return nil, huma.Error500InternalServerError("persist telemetry consent", err)
	}
	slog.Info("telemetry consent set", "enabled", in.Body.Enabled)
	return nil, nil
}

// completeFirstRun latches the first-run-complete marker (FIRST_RUN.md # Phase 3)
// at the wizard's Done step, so the wizard never reappears. This is the
// bootstrap-complete marker the cloud e2e lane (C5) asserts. Distinct from
// "an admin exists" — an admin can be created mid-wizard. Idempotent.
func (s *Server) completeFirstRun(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.store.SetFirstRunComplete(); err != nil {
		return nil, huma.Error500InternalServerError("persist first-run complete", err)
	}
	slog.Info("first-run marked complete")
	return nil, nil
}
