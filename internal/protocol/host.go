// Package protocol defines the wire types shared between the brain and
// host-agent (BRAIN_HOST_PROTOCOL.md). HTTP/JSON over a UNIX socket.
package protocol

// SocketPath is the production socket location. In dev the brain and the
// fake host-agent agree on a path via MALMO_AGENT_SOCK.
const SocketPath = "/var/run/malmo/agent.sock"

// PublishRequest registers a per-app .local name (POST /v1/discovery/publish).
type PublishRequest struct {
	Slug string `json:"slug"`
}

type PublishResponse struct {
	Name  string `json:"name"`
	State string `json:"state"` // "established"
}

// UnpublishRequest removes a per-app name (POST /v1/discovery/unpublish).
type UnpublishRequest struct {
	Slug string `json:"slug"`
}

// SystemStatus is GET /v1/system/status.
type SystemStatus struct {
	Hostname     string `json:"hostname"`
	UptimeS      int64  `json:"uptime_s"`
	DiskPressure bool   `json:"disk_pressure"`
	AgentVersion string `json:"agent_version"`
}

// PublishedName is one entry in the discovery state.
type PublishedName struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// DiscoveryState is GET /v1/discovery/state.
type DiscoveryState struct {
	Publisher  string          `json:"publisher"`
	HostName   string          `json:"host_name"`
	RenamedTo  *string         `json:"renamed_to"`
	Published  []PublishedName `json:"published"`
	Interfaces []string        `json:"interfaces"`
}

// VerifyPasswordRequest is POST /v1/auth/verify-password. host-agent delegates
// to PAM authenticate() in prod; the fake checks a bcrypt hash. The endpoint
// never distinguishes wrong-password from unknown-user from locked-account.
type VerifyPasswordRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

type VerifyPasswordResponse struct {
	Valid bool `json:"valid"`
}

// SetPasswordRequest is POST /v1/auth/set-password. Upsert: creates the user
// if missing (real impl: useradd + passwd as one atomic op), otherwise just
// updates the password.
type SetPasswordRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// DeleteUserRequest is POST /v1/auth/delete-user. Idempotent: unknown user
// returns 200.
type DeleteUserRequest struct {
	User string `json:"user"`
}

// SetRoleRequest is POST /v1/auth/set-role. Updates the user's Linux group
// membership (malmo-admin) to match the new role. Role must be "admin" or "member".
type SetRoleRequest struct {
	User string `json:"user"`
	Role string `json:"role"`
}

// StorageHealth is GET /v1/health/storage. host-agent reads the latest
// findings written by malmo-storage-verify (BOOT.md # The storage-ready target)
// at /run/malmo/health/storage.json and returns them to the brain, which
// converts findings into typed health issues per HEALTH.md.
//
// An empty Findings slice means storage looks healthy — not "no report yet."
// A missing report file at the host-agent end is reported as empty Findings;
// a malformed report is reported as a single Finding with ID
// "health-report-malformed" so the brain has something to surface rather than
// silently passing.
type StorageHealth struct {
	CheckedAt string    `json:"checked_at"`
	Findings  []Finding `json:"findings"`
}

// Finding is one anomaly the reporter detected. ID is a stable string drawn
// from the typed taxonomy in HEALTH.md # Storage (e.g. "data-drive-missing",
// "canary-mismatch"). The brain looks the ID up in its registry to derive
// category, severity, tier, and the blocks_* flags — the reporter does not
// re-declare those, so the source of truth stays in one place.
type Finding struct {
	ID      string `json:"id"`
	Details string `json:"details,omitempty"`
}

// ResolveHomeResponse is GET /v1/users/{username}/home. Returns the owner's home
// directory path and POSIX UID/GID so the brain (containerized, no /etc/passwd
// access) can emit correct bind-mount sources and user: directives in the
// compose override. 404 with code "unknown-user" if the user does not exist.
type ResolveHomeResponse struct {
	HomePath string `json:"home_path"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

// WellKnownIdentityResponse is GET /v1/identity/well-known. Returns the fixed
// service-account identities the brain needs to emit correct user:/group_add
// directives in compose overrides for household-scope app instances.
//
// MalmoAppUID/GID is the shared service identity (compose user:).
// MalmoSharedGID is the GID of the malmo-shared group (apps electing a shared
// folder source are added to it via group_add).
type WellKnownIdentityResponse struct {
	MalmoAppUID    int `json:"malmo_app_uid"`
	MalmoAppGID    int `json:"malmo_app_gid"`
	MalmoSharedGID int `json:"malmo_shared_gid"`
}

// Error is the JSON error body shape on non-2xx responses.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
