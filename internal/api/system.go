package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/auth"
	"github.com/malmoos/malmo/internal/version"
)

// registerSystem registers the box-level system routes. Only the one-time
// storage poll and the build-version read live here today; the live resource
// stream (GET /api/v1/system/live) is a raw SSE handler registered in Handler,
// not huma.
func (s *Server) registerSystem(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-system-storage", Method: "GET", Path: "/api/v1/system/storage",
		Summary: "Per-disk storage usage (used/total bytes) for the system-resources panel",
	}, s.systemStorage)
	huma.Register(api, huma.Operation{
		OperationID: "get-system-version", Method: "GET", Path: "/api/v1/system/version",
		Summary: "The running malmo-brain build's version and git commit",
	}, s.systemVersion)
}

// SystemVersionDTO is the GET /api/v1/system/version body: the brain build's
// repo SemVer (VERSION file, BUILD.md # Versioning — one version for the whole
// monorepo, DECISIONS.md 2026-07-16) plus the short git commit it was built
// from, both stamped at build time (internal/version). Surfacing this in the
// dashboard is a later slice; this endpoint is the box-level read it will use.
type SystemVersionDTO struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// systemVersion reads the brain's own stamped build identity — no host-agent
// round trip, it's compiled into this binary. Available to every signed-in
// user with no role gate, same posture as systemStorage: build identity isn't
// per-user or sensitive data.
func (s *Server) systemVersion(ctx context.Context, _ *struct{}) (*struct{ Body SystemVersionDTO }, error) {
	if _, ok := auth.FromContext(ctx); !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	return &struct{ Body SystemVersionDTO }{Body: SystemVersionDTO{
		Version: version.Version,
		Commit:  version.Commit,
	}}, nil
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
