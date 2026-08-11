package api

import (
	"net/http"
	"testing"
)

// TestClientIPTrustBoundary pins the rule the per-IP throttles depend on
// (#329): X-Forwarded-For is read only when the immediate peer is a trusted
// proxy, and only the last untrusted hop is taken. Every "want" here is an
// address the caller could not have chosen for itself.
func TestClientIPTrustBoundary(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 127.0.0.1")
	if err != nil {
		t.Fatalf("parse trusted proxies: %v", err)
	}

	cases := []struct {
		name       string
		trusted    bool // configure the server with the trusted set above
		remoteAddr string
		xff        []string
		want       string
	}{
		// No trusted proxies configured at all: the header is inert.
		{"no trusted set, no header", false, "192.168.1.1:54321", nil, "192.168.1.1"},
		{"no trusted set, forged header ignored", false, "192.168.1.1:54321", []string{"203.0.113.9"}, "192.168.1.1"},

		// Peer is not in the trusted set: the header is inert even though other
		// addresses are trusted.
		{"untrusted peer, forged header ignored", true, "192.168.1.1:54321", []string{"203.0.113.9"}, "192.168.1.1"},

		// Peer is a trusted proxy: the header is read, right to left.
		{"trusted peer, single hop", true, "10.0.0.2:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"trusted peer, forged hops behind the real one", true, "10.0.0.2:443", []string{"1.2.3.4, 5.6.7.8, 203.0.113.9"}, "203.0.113.9"},
		{"trusted peer, trailing trusted hops skipped", true, "10.0.0.2:443", []string{"203.0.113.9, 10.0.0.7, 10.0.0.8"}, "203.0.113.9"},
		{"trusted peer, chain split across header lines", true, "10.0.0.2:443", []string{"1.2.3.4", "203.0.113.9, 10.0.0.7"}, "203.0.113.9"},
		{"trusted peer, no header", true, "10.0.0.2:443", nil, "10.0.0.2"},
		{"trusted peer, empty header", true, "10.0.0.2:443", []string{""}, "10.0.0.2"},

		// A hop we cannot parse stops the walk: nothing to its left is knowable,
		// so the peer address is used.
		{"trusted peer, garbage rightmost hop", true, "10.0.0.2:443", []string{"203.0.113.9, not-an-ip"}, "10.0.0.2"},

		// Every hop trusted ⇒ nothing untrusted to report; fall back to the peer.
		{"trusted peer, all hops trusted", true, "10.0.0.2:443", []string{"10.0.0.7"}, "10.0.0.2"},

		// IPv6 forms are canonicalised so one client cannot hold several buckets.
		{"loopback peer, v6 hop", true, "127.0.0.1:443", []string{"2001:DB8::1"}, "2001:db8::1"},
		{"loopback peer, v4-mapped v6 hop", true, "127.0.0.1:443", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"v6 peer, not trusted", true, "[2001:db8::5]:443", []string{"203.0.113.9"}, "2001:db8::5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			if tc.trusted {
				s.SetTrustedProxies(trusted)
			}
			r := &http.Request{RemoteAddr: tc.remoteAddr, Header: http.Header{}}
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientIPRemoteAddrWithoutPort covers a RemoteAddr net.SplitHostPort can't
// split (a UNIX socket under httptest): the raw value is the key rather than an
// empty string, which would pool every such request into one bucket.
func TestClientIPRemoteAddrWithoutPort(t *testing.T) {
	s := &Server{}
	r := &http.Request{RemoteAddr: "@", Header: http.Header{}}
	if got := s.clientIP(r); got != "@" {
		t.Errorf("clientIP = %q, want %q", got, "@")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty spec trusts nothing", func(t *testing.T) {
		got, err := ParseTrustedProxies("  ,, ")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("prefixes = %v, want none", got)
		}
	})

	t.Run("bare address is a single host", func(t *testing.T) {
		got, err := ParseTrustedProxies("10.0.0.4")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(got) != 1 || got[0].String() != "10.0.0.4/32" {
			t.Fatalf("prefixes = %v, want [10.0.0.4/32]", got)
		}
	})

	t.Run("host bits in a CIDR are masked off", func(t *testing.T) {
		got, err := ParseTrustedProxies("10.1.2.3/8")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(got) != 1 || got[0].String() != "10.0.0.0/8" {
			t.Fatalf("prefixes = %v, want [10.0.0.0/8]", got)
		}
	})

	for _, spec := range []string{"nonsense", "10.0.0.0/64", "10.0.0.0/"} {
		t.Run("rejects "+spec, func(t *testing.T) {
			if _, err := ParseTrustedProxies(spec); err == nil {
				t.Fatalf("parse(%q) = nil error, want a parse failure", spec)
			}
		})
	}
}

// TestDefaultTrustedProxies checks the shipped default covers the addresses a
// peer on the box's own Docker network (or the native dev brain's caller) can
// have, and nothing routable.
func TestDefaultTrustedProxies(t *testing.T) {
	s := &Server{}
	s.SetTrustedProxies(DefaultTrustedProxies())

	trusted := []string{"127.0.0.1:1", "[::1]:1", "172.18.0.2:1"}
	for _, peer := range trusted {
		r := &http.Request{RemoteAddr: peer, Header: http.Header{}}
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		if got := s.clientIP(r); got != "203.0.113.9" {
			t.Errorf("peer %s should be trusted: clientIP = %q", peer, got)
		}
	}

	// The three former-default ranges are listed explicitly: a peer in a client
	// range must not be trusted as a proxy, which is what the earlier default got
	// wrong.
	untrusted := []string{"203.0.113.4:1", "8.8.8.8:1", "[2001:db8::1]:1", "10.4.5.6:1", "192.168.1.9:1", "[fd00::2]:1"}
	for _, peer := range untrusted {
		r := &http.Request{RemoteAddr: peer, Header: http.Header{}}
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		want := remoteAddrHost(r)
		if got := s.clientIP(r); got != want {
			t.Errorf("peer %s should not be trusted: clientIP = %q, want %q", peer, got, want)
		}
	}
}

// TestDefaultTrustedProxiesKeepsLANClientsApart is the regression test for the
// trap in a "trust the private ranges" default: a trusted hop is *skipped*
// during the chain walk, so trusting 192.168.0.0/16 or 10.0.0.0/8 would erase every LAN
// client's own hop and key the whole household on Caddy's address — one device
// could then spend everyone's login and allowlist budget. Each household device
// must resolve to itself.
func TestDefaultTrustedProxiesKeepsLANClientsApart(t *testing.T) {
	s := &Server{}
	s.SetTrustedProxies(DefaultTrustedProxies())

	for _, client := range []string{"192.168.1.20", "192.168.1.21", "10.4.5.6", "100.64.0.3"} {
		r := &http.Request{RemoteAddr: "172.18.0.3:443", Header: http.Header{}} // the box's Caddy
		r.Header.Set("X-Forwarded-For", client)
		if got := s.clientIP(r); got != client {
			t.Errorf("LAN client %s behind Caddy: clientIP = %q, want its own address", client, got)
		}
	}
}
