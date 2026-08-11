package api

// The admin trigger for a control-plane update (UPDATES.md # 3, issue #381).
//
// The brain does not run the update — it cannot, because it is one of the two
// containers being replaced. host-agent runs it as a job (BRAIN_HOST_PROTOCOL.md
// # Pattern B); this endpoint starts that job and hands back its id.
//
// **The job id is host-agent's, not the brain's.** The brain has its own job
// registry (jobs.go) and deliberately does not wrap this in one: a brain-side
// record would die the moment the update recreates the brain, halfway through
// the operation it was tracking. Polling goes to host-agent, which stays up, so
// the status read still works after the brain has been replaced — as long as
// the caller kept the id.
//
// **The target is two explicit image refs.** There is no release-manifest poll
// and no cloud call: the box↔cloud credential is still undesigned (NEXT.md,
// Tier 1). The whole updater is testable and shippable without it.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/audit"
	"github.com/malmoos/malmo/internal/hostclient"
	"github.com/malmoos/malmo/internal/protocol"
)

func (s *Server) registerSystemUpdate(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "start-system-update", Method: "POST", Path: "/api/v1/system/update",
		Summary:       "Move the control plane to the given brain/UI image pair (admin only)",
		DefaultStatus: 202,
	}, s.startSystemUpdate)
	huma.Register(api, huma.Operation{
		OperationID: "get-system-update", Method: "GET", Path: "/api/v1/system/update/{job_id}",
		Summary: "Status of a control-plane update job (admin only)",
	}, s.getSystemUpdate)
}

// SystemUpdateRequestDTO is the POST body: the target pair. An empty ref means
// "leave that component alone", which is how a UI-only or brain-only ship is
// expressed. Both empty is refused.
type SystemUpdateRequestDTO struct {
	BrainImage string `json:"brain_image,omitempty"`
	UIImage    string `json:"ui_image,omitempty"`
}

// SystemUpdateResultDTO is what a finished job did. Present on failure too:
// "we reverted, and here is what broke" is what the admin needs to see
// (UPDATES.md # 3 step 4).
type SystemUpdateResultDTO struct {
	BrainChanged bool   `json:"brain_changed"`
	UIChanged    bool   `json:"ui_changed"`
	Reverted     bool   `json:"reverted"`
	FailureMode  string `json:"failure_mode,omitempty"`
	RevertError  string `json:"revert_error,omitempty"`
}

// SystemUpdateErrorDTO is the failure code and message of a failed job.
type SystemUpdateErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SystemUpdateJobDTO is one host-agent job as the dashboard sees it. Status is
// running, completed, or failed.
type SystemUpdateJobDTO struct {
	JobID      string                 `json:"job_id"`
	Kind       string                 `json:"kind"`
	Status     string                 `json:"status"`
	StartedAt  string                 `json:"started_at"`
	FinishedAt string                 `json:"finished_at,omitempty"`
	Error      *SystemUpdateErrorDTO  `json:"error,omitempty"`
	Result     *SystemUpdateResultDTO `json:"result,omitempty"`
}

// startSystemUpdate starts the update job. Elevation-class per CLAUDE.md, so it
// audits the start **and** every refusal: a member trying to update the box is
// exactly the kind of thing the Activity view exists to show.
//
// success=true here means "the update started", not "the update worked". The
// brain cannot audit the outcome, because the brain is what the update
// replaces; the job record on host-agent is where the outcome lives.
func (s *Server) startSystemUpdate(ctx context.Context, in *struct {
	Body SystemUpdateRequestDTO
}) (*struct{ Body SystemUpdateJobDTO }, error) {
	brainRef := strings.TrimSpace(in.Body.BrainImage)
	uiRef := strings.TrimSpace(in.Body.UIImage)
	tgt := audit.Target{Kind: "system", ID: "control-plane"}
	meta := map[string]any{"brain_image": brainRef, "ui_image": uiRef}
	fail := func() { s.auditor.Record(ctx, audit.ActionSystemUpdate, tgt, meta, false) }

	if err := requireAdmin(ctx); err != nil {
		fail()
		return nil, err
	}
	if brainRef == "" && uiRef == "" {
		fail()
		return nil, huma.Error422UnprocessableEntity("brain_image or ui_image is required")
	}
	// Refs travel into a compose file and onto a `docker pull` argument list.
	// The arguments are exec'd without a shell and the compose rewrite verifies
	// what it wrote, so this is not the only guard — but a ref with a newline in
	// it is a mistake in every case, and refusing it here keeps the mistake out
	// of the box's declaration entirely.
	for _, ref := range []string{brainRef, uiRef} {
		if strings.ContainsFunc(ref, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			fail()
			return nil, huma.Error422UnprocessableEntity("image refs may not contain whitespace or control characters")
		}
	}

	job, err := s.host.StartSystemUpdate(ctx, brainRef, uiRef)
	if err != nil {
		fail()
		if errors.Is(err, hostclient.ErrUpdateInProgress) {
			return nil, huma.Error409Conflict("a control-plane update is already running")
		}
		slog.Error("system-update: host refused the job", "image", brainRef, "err", err)
		return nil, huma.Error502BadGateway("could not start the update")
	}
	s.auditor.Record(ctx, audit.ActionSystemUpdate, tgt, meta, true)
	slog.Info("system-update started", "image", brainRef, "step", "accepted")
	return &struct{ Body SystemUpdateJobDTO }{Body: toSystemUpdateJobDTO(job)}, nil
}

// getSystemUpdate polls the job. A pure read, so it does not audit. Admin-only,
// same as the start: what a box is being moved to is admin business.
func (s *Server) getSystemUpdate(ctx context.Context, in *struct {
	JobID string `path:"job_id"`
}) (*struct{ Body SystemUpdateJobDTO }, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	job, err := s.host.Job(ctx, in.JobID)
	if err != nil {
		if errors.Is(err, hostclient.ErrJobNotFound) {
			return nil, huma.Error404NotFound("no such update job")
		}
		slog.Error("system-update: host job read failed", "err", err)
		return nil, huma.Error502BadGateway("could not read the update job")
	}
	return &struct{ Body SystemUpdateJobDTO }{Body: toSystemUpdateJobDTO(job)}, nil
}

func toSystemUpdateJobDTO(j protocol.Job) SystemUpdateJobDTO {
	out := SystemUpdateJobDTO{
		JobID:      j.ID,
		Kind:       j.Kind,
		Status:     j.Status,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
	if j.Error != nil {
		out.Error = &SystemUpdateErrorDTO{Code: j.Error.Code, Message: j.Error.Message}
	}
	if j.Result != nil {
		out.Result = &SystemUpdateResultDTO{
			BrainChanged: j.Result.BrainChanged,
			UIChanged:    j.Result.UIChanged,
			Reverted:     j.Result.Reverted,
			FailureMode:  j.Result.FailureMode,
			RevertError:  j.Result.RevertError,
		}
	}
	return out
}
