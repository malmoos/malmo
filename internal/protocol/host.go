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
	Hostname      string `json:"hostname"`
	UptimeS       int64  `json:"uptime_s"`
	DiskPressure  bool   `json:"disk_pressure"`
	AgentVersion  string `json:"agent_version"`
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

// Error is the JSON error body shape on non-2xx responses.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
