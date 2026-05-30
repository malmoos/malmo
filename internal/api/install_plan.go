package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmo/malmo/internal/auth"
	"github.com/malmo/malmo/internal/catalog"
	"github.com/malmo/malmo/internal/manifest"
	"github.com/malmo/malmo/internal/store"
)

// source value constants for folder source menus.
const (
	sourceShared   = "shared"
	sourcePersonal = "personal"
)

// InstallPlanDTO is the response body for GET /api/v1/catalog/{id}/install-plan.
// It carries everything the consent screen needs: declared permissions, the
// role-derived scope options, and per-folder source menus. It is advisory —
// the authoritative validation and override stamping happen in slice 4 when
// POST /api/v1/apps receives the user's elections in its config.
type InstallPlanDTO struct {
	ManifestID   string                 `json:"manifest_id"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	ScopeOptions []string               `json:"scope_options"`
	ScopeDefault string                 `json:"scope_default"`
	Permissions  InstallPlanPermissions `json:"permissions"`
}

// InstallPlanPermissions mirrors manifest.Permissions for the install-plan
// wire shape, with Folders mapped to the richer InstallPlanFolder type.
type InstallPlanPermissions struct {
	Internet bool                `json:"internet"`
	LAN      bool                `json:"lan"`
	GPU      bool                `json:"gpu"`
	Devices  []string            `json:"devices"`
	Folders  []InstallPlanFolder `json:"folders"`
}

// InstallPlanFolder is one declared folder with its per-scope source menus.
type InstallPlanFolder struct {
	Folder           string        `json:"folder"`
	Mode             string        `json:"mode"`
	Scope            string        `json:"scope"`
	SubfolderDefault string        `json:"subfolder_default,omitempty"`
	Sources          FolderSources `json:"sources"`
}

// FolderSources holds one SourceMenu per install scope. Both are always
// populated regardless of the caller's role — the household menu is present
// even for members (it's unreachable since household scope isn't offered, but
// keeping the shape uniform simplifies the UI).
type FolderSources struct {
	Household SourceMenu `json:"household"`
	Personal  SourceMenu `json:"personal"`
}

// SourceMenu is the set of source choices for a folder under one scope.
// A single-option menu renders as fixed/disabled in the UI.
type SourceMenu struct {
	Options []string `json:"options"`
	Default string   `json:"default"`
}

// scopeMenu returns the scope options and default for the caller's role.
// admin → (["household","personal"], "household")
// member → (["personal"], "personal")
// This is the single source of truth for the scope authorization table
// (DASHBOARD.md # install authorization). resolveOwnerScope enforces the same
// table on the write path — keep the two in sync.
func scopeMenu(isAdmin bool) (options []string, def string) {
	if isAdmin {
		return []string{store.ScopeHousehold, store.ScopePersonal}, store.ScopeHousehold
	}
	return []string{store.ScopePersonal}, store.ScopePersonal
}

// buildInstallPlan maps a parsed manifest and the caller's role into an
// InstallPlanDTO. Pure function — no I/O, trivially unit-testable.
func buildInstallPlan(man *manifest.Manifest, isAdmin bool) InstallPlanDTO {
	opts, def := scopeMenu(isAdmin)

	folders := make([]InstallPlanFolder, 0, len(man.Permissions.Folders))
	for _, f := range man.Permissions.Folders {
		// Per-scope source menus. Household always forces shared (the household
		// share lives under /srv/malmo/shared/<Folder>/, which is the single
		// shared path). Personal offers personal (~/Folder/) or shared, defaulting
		// to personal. Built per-folder so a future author-declared source hint
		// can vary the personal default without a wire-shape change.
		householdMenu := SourceMenu{
			Options: []string{sourceShared},
			Default: sourceShared,
		}
		personalMenu := SourceMenu{
			Options: []string{sourcePersonal, sourceShared},
			Default: sourcePersonal,
		}
		folders = append(folders, InstallPlanFolder{
			Folder:           f.Folder,
			Mode:             f.Mode,
			Scope:            f.Scope,
			SubfolderDefault: f.Default,
			Sources: FolderSources{
				Household: householdMenu,
				Personal:  personalMenu,
			},
		})
	}

	devices := man.Permissions.Devices
	if devices == nil {
		devices = []string{}
	}

	return InstallPlanDTO{
		ManifestID:   man.ID,
		Name:         man.Name,
		Version:      man.Version,
		ScopeOptions: opts,
		ScopeDefault: def,
		Permissions: InstallPlanPermissions{
			Internet: man.Permissions.Internet,
			LAN:      man.Permissions.LAN,
			GPU:      man.Permissions.GPU,
			Devices:  devices,
			Folders:  folders,
		},
	}
}

func (s *Server) installPlan(ctx context.Context, in *struct {
	ID string `path:"id"`
}) (*struct{ Body InstallPlanDTO }, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("unauthenticated")
	}
	man, _, err := s.catalog.Load(in.ID)
	if errors.Is(err, catalog.ErrNotFound) {
		// Normal client outcome (stale id, typo) — 404 without logging, like getApp.
		return nil, huma.Error404NotFound("no such catalog app")
	}
	if err != nil {
		// The entry exists but won't parse or is missing its compose file — a
		// catalog integrity problem, not a client error. Surface as 500 and log
		// loudly so the operator sees the broken entry.
		slog.Error("install-plan: catalog entry failed to load", "manifest_id", in.ID, "err", err)
		return nil, huma.Error500InternalServerError("catalog entry is malformed")
	}
	return &struct{ Body InstallPlanDTO }{Body: buildInstallPlan(man, id.IsAdmin())}, nil
}
