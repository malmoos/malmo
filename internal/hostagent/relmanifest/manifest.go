package relmanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/mod/semver"
)

// SchemaVersion is the manifest schema this box understands
// (RELEASE_MANIFEST.md # Manifest schema (v1)). A different value means the
// publisher made a breaking change, so the box ignores the file rather than
// guessing. Additive changes do NOT bump it — unknown fields are ignored, which
// is what lets a new optional field ship without a fleet update.
const SchemaVersion = 1

// Channel is the only channel v1 ships. The field exists in the schema so a
// future "beta" is additive (a second file, an opt-in setting) rather than a
// flag day.
const Channel = "stable"

// ErrWrongSchema is returned for a manifest whose manifest_version is not
// SchemaVersion.
var ErrWrongSchema = errors.New("relmanifest: unsupported manifest_version")

// ErrWrongChannel is returned for a manifest belonging to a channel this box
// does not follow.
var ErrWrongChannel = errors.New("relmanifest: manifest is for another channel")

// Pair is a control-plane version pair: the brain and the UI.
//
// They are two fields because the schema has two. In practice they are always
// equal — BUILD.md # Versioning moved to one version for the whole monorepo
// (DECISIONS.md 2026-07-16) — so do not read them as independently versioned.
type Pair struct {
	Brain string `json:"brain"`
	UI    string `json:"ui"`
}

// Manifest is the parsed release manifest. Unknown fields are dropped on
// purpose: the publisher may add optional fields at any time without bumping
// ManifestVersion, and a box that refused them could not be shipped ahead of
// them.
type Manifest struct {
	ManifestVersion  int       `json:"manifest_version"`
	Channel          string    `json:"channel"`
	Brain            string    `json:"brain"`
	UI               string    `json:"ui"`
	MinimumHostAgent string    `json:"minimum_host_agent"`
	ReleasedAt       time.Time `json:"released_at"`
	// RollbackTo is the kill switch: nil in steady state, and a prior pair when
	// a release is retracted (RELEASE_MANIFEST.md # Kill switch).
	RollbackTo *Pair `json:"rollback_to"`
}

// Pair returns the version pair the manifest names for normal operation.
func (m Manifest) Pair() Pair { return Pair{Brain: m.Brain, UI: m.UI} }

// Parse decodes and sanity-checks a manifest. It does NOT verify a signature —
// call Verify first and only parse bytes that verified, so no unsigned input
// ever reaches the fields a decision is made from.
//
// The checks here are the ones that make the file meaningful at all: our schema
// version, our channel, and three semver-shaped versions. Whether the manifest
// *applies to this box* is a separate question with more than two answers, and
// lives in Decide.
func Parse(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("relmanifest: parse: %w", err)
	}
	if m.ManifestVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("%w: %d (this box reads %d)", ErrWrongSchema, m.ManifestVersion, SchemaVersion)
	}
	if m.Channel != Channel {
		return Manifest{}, fmt.Errorf("%w: %q", ErrWrongChannel, m.Channel)
	}
	for name, v := range map[string]string{"brain": m.Brain, "ui": m.UI, "minimum_host_agent": m.MinimumHostAgent} {
		if !validVersion(v) {
			return Manifest{}, fmt.Errorf("relmanifest: %s: %q is not a version", name, v)
		}
	}
	if r := m.RollbackTo; r != nil {
		if !validVersion(r.Brain) || !validVersion(r.UI) {
			return Manifest{}, fmt.Errorf("relmanifest: rollback_to: %q/%q is not a version pair", r.Brain, r.UI)
		}
	}
	return m, nil
}

// Action is what a box should do about a manifest. The three states are not
// two: RELEASE_MANIFEST.md # Failure modes keeps "this is the current release,
// but do not prompt" separate from "this manifest is not for you", because a
// box in the first state is healthy and waiting for its apt-driven host-agent
// update, while a box in the second is looking at a file it should ignore.
type Action int

const (
	// ActionNone — the box already runs what the manifest names. Steady state.
	ActionNone Action = iota
	// ActionUpdate — the manifest names a different pair and the box may offer
	// it. UPDATES.md # 3: the dashboard surfaces a prompt; nothing applies on
	// its own.
	ActionUpdate
	// ActionRollback — the kill switch is set and the box is running the
	// retracted version, so it should offer to go back to the named pair.
	ActionRollback
	// ActionHoldHostAgentTooOld — the manifest is valid and current, but this
	// box's host-agent is older than minimum_host_agent. The manifest is
	// honoured as "the current release" and no prompt is surfaced; the next apt
	// update resolves it and the prompt appears on a later poll.
	ActionHoldHostAgentTooOld
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionUpdate:
		return "update"
	case ActionRollback:
		return "rollback"
	case ActionHoldHostAgentTooOld:
		return "hold-host-agent-too-old"
	default:
		return fmt.Sprintf("action(%d)", int(a))
	}
}

// BoxState is what Decide needs to know about the box: the control-plane pair
// it is running now, and its host-agent version.
type BoxState struct {
	Running          Pair
	HostAgentVersion string
}

// Decision is Decide's answer: what to do, and which pair to do it with.
// Target is empty when Action is ActionNone or ActionHoldHostAgentTooOld.
type Decision struct {
	Action Action
	Target Pair
}

// Decide answers what this box should do about a manifest it has already
// verified and parsed.
//
// Order matters and is spec-driven. The kill switch is checked **before** the
// host-agent gate: a box running a version that has been retracted should be
// offered the way back even if it is behind on host-agent, because the retracted
// version is the thing hurting it right now. The host-agent gate exists to stop
// a box moving *forward* onto a version its host side cannot drive.
func Decide(m Manifest, box BoxState) Decision {
	if r := m.RollbackTo; r != nil {
		// Only a box that actually applied the retracted pair needs to move.
		// A box that never took it simply never sees the offer — the prompt is
		// retracted by the target below being what it already runs.
		if box.Running == m.Pair() {
			return Decision{Action: ActionRollback, Target: *r}
		}
		return Decision{Action: ActionNone}
	}
	if box.Running == m.Pair() {
		return Decision{Action: ActionNone}
	}
	if !hostAgentSatisfies(box.HostAgentVersion, m.MinimumHostAgent) {
		return Decision{Action: ActionHoldHostAgentTooOld}
	}
	return Decision{Action: ActionUpdate, Target: m.Pair()}
}

// hostAgentSatisfies reports whether have >= want.
//
// An unreadable host-agent version fails closed: it is a version we cannot
// compare, and treating it as "new enough" would let the one safety belt in
// this design open itself whenever the string is malformed.
func hostAgentSatisfies(have, want string) bool {
	if !validVersion(have) || !validVersion(want) {
		return false
	}
	return semver.Compare(canonical(have), canonical(want)) >= 0
}

// validVersion accepts the semver the manifest carries. The file writes plain
// "1.4.2" (no leading v), while golang.org/x/mod/semver requires the "v", so
// canonicalize before every comparison rather than at the edges — the two forms
// have already been mixed once in this repo's history.
func validVersion(s string) bool { return s != "" && semver.IsValid(canonical(s)) }

func canonical(s string) string {
	if s == "" || s[0] == 'v' {
		return s
	}
	return "v" + s
}
