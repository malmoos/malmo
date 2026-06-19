package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/auth"
)

// registerSystem registers the box-level system routes. Only the one-time
// storage poll lives here today; the live resource stream (GET
// /api/v1/system/live) is a raw SSE handler registered in Handler, not huma.
func (s *Server) registerSystem(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-system-storage", Method: "GET", Path: "/api/v1/system/storage",
		Summary: "Per-disk storage usage (used/total bytes) for the system-resources panel",
	}, s.systemStorage)

	huma.Register(api, huma.Operation{
		OperationID: "set-system-timezone", Method: "POST", Path: "/api/v1/system/timezone",
		Summary: "Set the host system timezone (admin only)", DefaultStatus: 204,
	}, s.setSystemTimezone)
}

// setSystemTimezone sets the host system timezone via the host-agent's
// timedatectl seam (TIME.md # System TZ). Used by the first-run wizard's time-zone
// step and, later, Settings → System → Time. Admin-only box config — not an
// elevation-class principal/app mutation, so no audit and no 5-minute re-prompt;
// the wizard's fresh admin session must reach it without re-elevation. tz is an
// IANA zone name; the host-agent validates its shape before shelling out, so a
// host rejection surfaces here as a 502.
func (s *Server) setSystemTimezone(ctx context.Context, in *struct {
	Body struct {
		Timezone string `json:"timezone"`
	}
}) (*struct{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	tz := strings.TrimSpace(in.Body.Timezone)
	if tz == "" {
		return nil, huma.Error422UnprocessableEntity("timezone is required")
	}
	if err := s.host.SetTimezone(ctx, tz); err != nil {
		slog.Error("set-timezone: host call failed", "err", err)
		return nil, huma.Error502BadGateway("host-agent set-timezone failed")
	}
	slog.Info("system timezone set", "timezone", tz)
	return nil, nil
}

// DiskSpaceDTO is one volume's fullness for the Storage bars: a human Label
// ("System", "Data") plus its free and total bytes (LOCAL_ANALYTICS.md #
// Real-time system resources). Used is derived UI-side as Total − Free.
type DiskSpaceDTO struct {
	Label      string `json:"label"`
	FreeBytes  int64  `json:"free_bytes"`
	TotalBytes int64  `json:"total_bytes"`
}

// SystemStorageDTO is the GET /api/v1/system/storage body: one entry per
// mounted volume of interest the host-agent reports (OS drive always, data
// drive when present). Disks is always present, possibly empty (no reporter
// wired host-side); the panel then shows no Storage section.
type SystemStorageDTO struct {
	Disks []DiskSpaceDTO `json:"disks"`
}

// systemStorage proxies the host-agent's per-volume disk fullness to the UI as a
// one-time poll (the install-plan dialog reads the same SystemStatus the same
// way). Available to every signed-in user with no role gate — host-level storage
// state isn't per-user data, same posture as the live resource stream
// (LOCAL_ANALYTICS.md # Privacy model). A host read failure is a 502: the panel
// shows "storage unavailable" rather than a misleading empty disk.
func (s *Server) systemStorage(ctx context.Context, _ *struct{}) (*struct{ Body SystemStorageDTO }, error) {
	if _, ok := auth.FromContext(ctx); !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	status, err := s.host.SystemStatus(ctx)
	if err != nil {
		slog.Error("system-storage: host status read failed", "err", err)
		return nil, huma.Error502BadGateway("could not read system storage")
	}
	out := SystemStorageDTO{Disks: []DiskSpaceDTO{}}
	for _, d := range status.Disks {
		out.Disks = append(out.Disks, DiskSpaceDTO{
			Label:      d.Label,
			FreeBytes:  d.FreeBytes,
			TotalBytes: d.TotalBytes,
		})
	}
	return &struct{ Body SystemStorageDTO }{Body: out}, nil
}
