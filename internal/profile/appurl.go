package profile

// NetworkApex is the malmo-owned public apex every hosted box lives under
// (MALMO_NETWORK.md; ENVIRONMENT.md # Networking & discovery). A hosted box with
// box-id "<base>-<suffix>" serves the dashboard at "<box-id>.malmo.network" and
// every app at "<slug>.<box-id>.malmo.network", all under the box's
// "*.<box-id>.malmo.network" wildcard cert.
const NetworkApex = "malmo.network"

// HostedAppHost returns the public host an app is served at on a hosted box:
// "<slug>.<box-id>.malmo.network". This is the Caddy route's Host match and the
// host portion of the user-facing URL. It is the appliance secure-URL shape
// (MALMO_NETWORK.md) made the sole scheme — there is no ".local" fallback on
// hosted (no LAN to multicast on). Callers gate on profile == Hosted and a
// non-empty box-id; on appliance the existing ".local"/mDNS path is unchanged.
func HostedAppHost(boxID, slug string) string {
	return slug + "." + boxID + "." + NetworkApex
}

// HostedAppURL returns the user-facing app URL on a hosted box — HTTPS at
// HostedAppHost, since the box always holds a real Let's Encrypt wildcard cert
// (always-on, no toggle: ENVIRONMENT.md # Networking & discovery).
func HostedAppURL(boxID, slug string) string {
	return "https://" + HostedAppHost(boxID, slug)
}

// HostedDashboardHost returns the host the dashboard is served at on a hosted
// box: "<box-id>.malmo.network" — the apex of the box's wildcard, the bare
// box-id with no app-slug label.
func HostedDashboardHost(boxID string) string {
	return boxID + "." + NetworkApex
}

// CertSubjects returns the names the box's Let's Encrypt certs must cover: the
// apex "<box-id>.malmo.network" (the dashboard host) and the wildcard
// "*.<box-id>.malmo.network" (every per-app host). The apex is deliberately
// listed separately — a "*.<box-id>" wildcard covers "<slug>.<box-id>" but not
// the bare "<box-id>" parent, so the dashboard would be uncovered without it.
// These are two certs, obtained by different paths: caddy.EnsureWildcardTLS
// drives the wildcard through acme-dns DNS-01 (the only challenge a wildcard can
// use), while the apex — a real, publicly-reachable host on :443 — is obtained by
// Caddy's default issuer over tls-alpn-01/http-01 as soon as the dashboard route
// names it, so only the wildcard ever touches acme-dns (ENVIRONMENT.md
// # Networking & discovery).
func CertSubjects(boxID string) []string {
	return []string{
		HostedDashboardHost(boxID),
		"*." + boxID + "." + NetworkApex,
	}
}
