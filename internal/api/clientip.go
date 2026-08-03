package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Client-IP derivation (AUTH.md # Rate limiting, BRAIN_UI_PROTOCOL.md # Rate
// limiting & abuse). Every per-IP throttle in the brain keys on the string this
// file returns, so a caller who can choose it can mint a fresh bucket per
// request and the throttle stops being a throttle (#329).
//
// The rule: `X-Forwarded-For` is a hint, never evidence. It is honoured only
// when the *immediate peer* — the one address on the request no client can
// forge — is a proxy we trust, and even then only the last untrusted hop is
// taken. Everything else falls back to the peer address itself.
//
// malmo's topology makes the trusted set small: the brain container publishes no
// port, so its only reachable peers are on the box's own `malmo-ingress`
// network, and its only legitimate client is the box's Caddy. The default set is
// therefore the private ranges (see defaultTrustedProxies); MALMO_TRUSTED_PROXIES
// narrows or widens it, and an empty value pins the brain to RemoteAddr alone.

// defaultTrustedProxyCIDRs is the trusted-proxy set the brain uses when
// MALMO_TRUSTED_PROXIES is unset: loopback plus the private ranges, which is
// every address a peer on the box's own Docker network can have (and, under
// `make dev`, the native brain's 127.0.0.1 caller). It mirrors Caddy's own
// `private_ranges` shortcut so the two sides of the hop describe the same set.
//
// It deliberately does not include the public internet: a request that reaches
// the brain from a routable address has not come through the box's Caddy, and
// nothing it says about its own origin is worth reading.
var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fd00::/8",
}

// DefaultTrustedProxies returns the parsed default set. It panics on a malformed
// constant, which can only be a typo in defaultTrustedProxyCIDRs above.
func DefaultTrustedProxies() []netip.Prefix {
	prefixes, err := ParseTrustedProxies(strings.Join(defaultTrustedProxyCIDRs, ","))
	if err != nil {
		panic("api: malformed defaultTrustedProxyCIDRs: " + err.Error())
	}
	return prefixes
}

// ParseTrustedProxies parses a comma-separated list of IPs and CIDR blocks into
// prefixes. A bare address is taken as a single-host prefix ("10.0.0.4" ⇒
// "10.0.0.4/32"). An empty (or whitespace-only) spec yields no prefixes, which
// means "trust nothing": clientIP then always returns the peer address.
func ParseTrustedProxies(spec string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "/") {
			p, err := netip.ParsePrefix(tok)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", tok, err)
			}
			// Masking discards host bits so a sloppy "10.0.0.1/8" still means 10/8
			// rather than failing to contain anything.
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(tok)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", tok, err)
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// SetTrustedProxies records the proxies whose X-Forwarded-For the brain will
// read. cmd/brain calls it once at startup, like SetEnvironment; the zero value
// (no proxies) is the safe default, so a Server that never gets the call keys
// every per-IP bucket on the peer address.
func (s *Server) SetTrustedProxies(prefixes []netip.Prefix) {
	s.trustedProxies = prefixes
}

// clientIP returns the address the per-IP throttles key on, and that the audit
// log records as the request's origin.
//
// It reads X-Forwarded-For only when the immediate peer is a trusted proxy, and
// then returns the rightmost hop that is not itself trusted — the address the
// last trusted proxy in the chain actually observed. Hops to the left of that
// are written by whoever was upstream of the trust boundary, i.e. potentially
// the attacker, and are never returned. A malformed hop stops the walk: we
// cannot tell what is behind it, so the peer address is used instead.
func (s *Server) clientIP(r *http.Request) string {
	peerStr := remoteAddrHost(r)
	peer, err := netip.ParseAddr(peerStr)
	if err != nil || !s.trustsAddr(peer) {
		return peerStr
	}
	hops := forwardedForHops(r)
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		hop = hop.Unmap().WithZone("")
		if s.trustsAddr(hop) {
			continue
		}
		return hop.String()
	}
	return peerStr
}

// trustsAddr reports whether addr is one of the configured trusted proxies.
func (s *Server) trustsAddr(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	for _, p := range s.trustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// forwardedForHops flattens every X-Forwarded-For header value into an ordered
// left-to-right hop list. Repeated header lines are equivalent to one
// comma-joined line per RFC 7230, and Go keeps them separate, so both shapes
// have to be walked or a chain split across lines would hide hops.
func forwardedForHops(r *http.Request) []string {
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, tok := range strings.Split(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				hops = append(hops, tok)
			}
		}
	}
	return hops
}

// remoteAddrHost strips the port from r.RemoteAddr, falling back to the raw
// value for an address shape net can't split (a UNIX socket in tests).
func remoteAddrHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
