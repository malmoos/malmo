//go:build !hosted

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/malmoos/malmo/internal/hostagent/relmanifest"
)

// startReleasePoll starts the appliance release-manifest poll and returns a
// stop function.
//
// **Appliance only.** A hosted box never fetches this file: its target version
// is held per-box by the cloud and asked for directly (UPDATES.md # 8.1), so
// the hosted build has a no-op with the same name in
// releasepoll_hosted.go. The two profiles share the apply-and-rollback
// transaction and differ only in what picks the target.
//
// It runs in the background and is started last, after the socket is serving
// and the brain has been launched. A slow or hanging CDN must never delay the
// box coming up: the manifest only says which version *could* be installed, and
// nothing about it is needed to boot.
//
// A build with no baked signing key does not poll at all — relmanifest.Run says
// so once, loudly, and returns. That is every build today (RELEASE_MANIFEST.md
// # Signing defers key custody until there is a release to sign).
func startReleasePoll(stateDir string) func() {
	p := &relmanifest.Poller{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Verifier: relmanifest.VerifierFromBakedKeys(),
		// The base URL is not a trust boundary — the signature is — so an
		// override is safe and lets a test box point at a local file server.
		// Empty means the real releases host.
		BaseURL:  os.Getenv("MALMO_RELEASE_BASE_URL"),
		StateDir: stateDir,
	}
	// Read what this box already knows before the first fetch, so an appliance
	// that boots without a network still reports the release it last heard
	// about instead of nothing.
	p.LoadCache()

	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	slog.Info("release manifest poll started", "keys", p.Verifier.Keys(), "state_dir", stateDir)
	return cancel
}
