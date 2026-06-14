package api

import (
	"context"
	"log/slog"

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
