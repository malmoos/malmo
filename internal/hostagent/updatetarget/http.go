package updatetarget

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultURL is where a hosted box asks what it should be running. The control
// plane serves it publicly and unauthenticated, because "what is the newest
// malmo" is not a secret — which is also why this can ship before the box↔cloud
// credential exists (UPDATES.md # 8.1 parks that in NEXT.md).
//
// It is a **default, not a constant**: the URL is configuration so a box can be
// pointed at an alternative source to prove a release before the fleet gets it.
// Keeping it configurable also keeps the seam honest — the box knows there is an
// update-target source, not who operates it.
const DefaultURL = "https://malmo.network/api/updates/target"

// fetchTimeout bounds one read. The answer is a few hundred bytes off a cached
// endpoint; anything slower than this is a source that is effectively down, and
// an unreachable source is a no-op.
const fetchTimeout = 30 * time.Second

// maxBodyBytes caps what will be read from the source. The answer is one small
// JSON object. Anything in megabytes is a misconfigured source or a hostile one,
// and neither deserves the box's memory.
const maxBodyBytes = 64 << 10

// Doer is the HTTP surface this source needs. Consumer-side (CLAUDE.md # Go code
// discipline), and small enough that a test drives it with httptest.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// HTTPSource reads the target from an update-target URL. This is the **hosted**
// implementation of Source.
type HTTPSource struct {
	// URL is the update-target endpoint; empty means DefaultURL.
	URL string
	// HTTP is the client; nil means a plain client with fetchTimeout.
	HTTP Doer
}

// wireTarget is the answer as it arrives. It is a CONTRACT WITH THE CONTROL
// PLANE (malmoos/cloud, internal/portal/updates.go): encoding/json silently
// drops what it does not model, so a rename on either side is a two-repo change,
// not a refactor.
//
// **Unknown fields are ignored on purpose.** The answer may carry more than the
// box models — that is how the sender ships a new optional field without a fleet
// update — but the fields below are modelled rather than passed through
// opaquely, because they are what the box validates and acts on.
type wireTarget struct {
	Version     string    `json:"version"`
	BrainImage  string    `json:"brain_image"`
	BrainDigest string    `json:"brain_digest"`
	UIImage     string    `json:"ui_image"`
	UIDigest    string    `json:"ui_digest"`
	PublishedAt time.Time `json:"published_at"`
}

// Target reads the update-target URL.
//
// A **404 is ErrNoTarget**, not an error: the source is up and saying "nothing
// is published on this channel". The control plane answers 404 rather than an
// empty 200 for exactly this reason — "no target" and "a target with empty
// fields" must never be the same response, or a box could read an unpinned
// answer as an instruction.
//
// Everything else that goes wrong — DNS, a refused connection, a 500, a body
// that is not JSON — is an error, and the loop treats it as a no-op that keeps
// the box on its current version.
func (s HTTPSource) Target(ctx context.Context) (Target, error) {
	url := s.URL
	if url == "" {
		url = DefaultURL
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Target{}, fmt.Errorf("updatetarget: build request for %s: %w", url, err)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Target{}, fmt.Errorf("updatetarget: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Target{}, ErrNoTarget
	}
	if resp.StatusCode != http.StatusOK {
		return Target{}, fmt.Errorf("updatetarget: fetch %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Target{}, fmt.Errorf("updatetarget: read %s: %w", url, err)
	}
	if len(b) > maxBodyBytes {
		return Target{}, fmt.Errorf("updatetarget: %s answered more than %d bytes", url, maxBodyBytes)
	}
	var w wireTarget
	if err := json.Unmarshal(b, &w); err != nil {
		return Target{}, fmt.Errorf("updatetarget: parse the answer from %s: %w", url, err)
	}
	return Target{
		Version:     w.Version,
		BrainImage:  w.BrainImage,
		BrainDigest: w.BrainDigest,
		UIImage:     w.UIImage,
		UIDigest:    w.UIDigest,
		PublishedAt: w.PublishedAt,
	}, nil
}
