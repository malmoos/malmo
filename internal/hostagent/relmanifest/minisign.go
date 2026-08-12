// Package relmanifest reads the signed release manifest an **appliance** box
// uses to learn which control-plane version it should be running
// (RELEASE_MANIFEST.md). A hosted box never fetches this file: its target is
// held per-box by the cloud and asked for directly (UPDATES.md # 8.1). The two
// profiles share the apply-and-rollback transaction (internal/hostagent/
// cpupdate) and differ only in what picks the target.
//
// Nothing here touches the network and nothing here runs on a timer. This
// package answers three questions about bytes someone else fetched: is this
// manifest really ours (minisign), what does it say (parse), and does it apply
// to this box (decide). The poll that fetches it, the dashboard prompt, and the
// three-strikes pin are separate slices, and each is easier to get right with
// this part pure and tested.
//
// The verifier is in this file. It is a minisign verifier only — it never
// signs, so there is no private-key handling anywhere in the box.
package relmanifest

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Signature algorithm ids as they appear in the first two bytes of a minisign
// signature or public key.
//
//	"Ed" — the legacy mode: Ed25519 over the file's raw bytes.
//	"ED" — the current mode: Ed25519 over BLAKE2b-512 of the file.
//
// Both are accepted. A manifest is a few hundred bytes, so prehashing buys
// nothing here, but minisign's own default writes "ED" and a verifier that only
// took one of the two would reject files produced by the standard tool.
var (
	algLegacy    = [2]byte{'E', 'd'}
	algPrehashed = [2]byte{'E', 'D'}
)

// ErrNoKeys is returned when the verifier holds no public keys at all.
//
// This is the state of every box built before a release-signing key exists
// (RELEASE_MANIFEST.md # Signing defers key custody until there is a release to
// sign). Such a box must refuse **every** manifest rather than trust one, so
// the appliance updater stays inert instead of acting on an unverifiable file.
// It is a distinct error because "this box was never given a key" and "this
// signature is wrong" call for different operator actions.
var ErrNoKeys = errors.New("relmanifest: no public keys configured")

// ErrUnknownKey is returned when the signature was made by a key this box does
// not accept. Distinct from ErrBadSignature: it is the expected error during a
// key rotation the box has not caught up with, not evidence of tampering.
var ErrUnknownKey = errors.New("relmanifest: signature key id not accepted")

// ErrBadSignature is returned when the signature does not verify. The manifest
// is ignored and the previous valid one stays in effect
// (RELEASE_MANIFEST.md # Failure modes).
var ErrBadSignature = errors.New("relmanifest: signature does not verify")

// PublicKey is one accepted minisign public key: the 8-byte key id the
// signature carries, and the Ed25519 key itself.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// ParsePublicKey reads a minisign public key, either the whole two-line key
// file or just its base64 line.
//
// Layout after base64-decoding: 2 bytes algorithm, 8 bytes key id, 32 bytes
// Ed25519 public key.
func ParsePublicKey(s string) (PublicKey, error) {
	line, err := lastNonCommentLine(s)
	if err != nil {
		return PublicKey{}, fmt.Errorf("public key: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return PublicKey{}, fmt.Errorf("public key: not base64: %w", err)
	}
	if len(raw) != 2+8+ed25519.PublicKeySize {
		return PublicKey{}, fmt.Errorf("public key: want %d bytes, got %d", 2+8+ed25519.PublicKeySize, len(raw))
	}
	if alg := [2]byte{raw[0], raw[1]}; alg != algLegacy && alg != algPrehashed {
		return PublicKey{}, fmt.Errorf("public key: unsupported algorithm %q", string(raw[0:2]))
	}
	var pk PublicKey
	copy(pk.ID[:], raw[2:10])
	pk.Key = ed25519.PublicKey(append([]byte(nil), raw[10:]...))
	return pk, nil
}

// Verifier holds the public keys this box accepts.
//
// **A list, not a single key, from day one.** RELEASE_MANIFEST.md # Signing
// makes key rotation depend on it: ship a host-agent that accepts {old, new},
// let the apt rollout reach the fleet, dual-sign for a window, then drop the
// old key. A verifier built around one constant would turn a lost or
// compromised key into a synchronized fleet update — the outage shape the
// design exists to avoid — and adding the list later is itself the flag day.
type Verifier struct {
	keys []PublicKey
}

// NewVerifier builds a verifier over the given keys. Zero keys is allowed and
// meaningful: every Verify then fails with ErrNoKeys. See that error for why
// this is the safe state and not a misconfiguration to paper over.
func NewVerifier(keys ...PublicKey) *Verifier {
	return &Verifier{keys: append([]PublicKey(nil), keys...)}
}

// Keys reports how many public keys this verifier accepts. Callers log it at
// startup so "this box trusts nothing" is visible in the journal rather than
// only in a later refusal.
func (v *Verifier) Keys() int { return len(v.keys) }

// signature is a decoded minisign .minisig file.
type signature struct {
	alg            [2]byte
	keyID          [8]byte
	sig            []byte // Ed25519 signature over the message (or its hash)
	trustedComment string
	globalSig      []byte // Ed25519 signature over sig || trustedComment
}

// Verify checks sigFile against message and returns the trusted comment on
// success.
//
// Both signatures in the file are checked, not just the first. The main one
// covers the manifest; the global one covers the signature **plus the trusted
// comment**, and skipping it would let anyone rewrite that comment on a
// genuinely signed file. The trusted comment is where minisign convention puts
// the file name and timestamp, so a verifier that returns it unchecked hands
// its caller attacker-controlled text that looks authenticated.
func (v *Verifier) Verify(message []byte, sigFile string) (trustedComment string, err error) {
	if len(v.keys) == 0 {
		return "", ErrNoKeys
	}
	s, err := parseSignature(sigFile)
	if err != nil {
		return "", err
	}
	key, ok := v.keyByID(s.keyID)
	if !ok {
		return "", fmt.Errorf("%w (key id %x)", ErrUnknownKey, s.keyID)
	}

	signed := message
	if s.alg == algPrehashed {
		h := blake2b.Sum512(message)
		signed = h[:]
	}
	if !ed25519.Verify(key.Key, signed, s.sig) {
		return "", ErrBadSignature
	}
	// The global signature binds the trusted comment to this exact signature.
	if !ed25519.Verify(key.Key, append(append([]byte(nil), s.sig...), []byte(s.trustedComment)...), s.globalSig) {
		return "", fmt.Errorf("%w (trusted comment)", ErrBadSignature)
	}
	return s.trustedComment, nil
}

func (v *Verifier) keyByID(id [8]byte) (PublicKey, bool) {
	for _, k := range v.keys {
		if k.ID == id {
			return k, true
		}
	}
	return PublicKey{}, false
}

// trustedCommentPrefix is the fixed marker minisign writes before the trusted
// comment. The bytes signed by the global signature are the comment *after*
// this prefix.
const trustedCommentPrefix = "trusted comment: "

// parseSignature decodes a .minisig file:
//
//	untrusted comment: <anything>
//	<base64: 2-byte alg | 8-byte key id | 64-byte signature>
//	trusted comment: <text>
//	<base64: 64-byte global signature>
//
// The untrusted comment is exactly what its name says and is never returned.
func parseSignature(sigFile string) (signature, error) {
	var s signature
	lines := nonEmptyLines(sigFile)
	// Line 1 (untrusted comment) is optional in the wild; the two base64 lines
	// and the trusted comment between them are not.
	if len(lines) > 0 && strings.HasPrefix(lines[0], "untrusted comment:") {
		lines = lines[1:]
	}
	if len(lines) < 3 {
		return s, errors.New("relmanifest: signature file truncated")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[0]))
	if err != nil {
		return s, fmt.Errorf("relmanifest: signature line is not base64: %w", err)
	}
	if len(raw) != 2+8+ed25519.SignatureSize {
		return s, fmt.Errorf("relmanifest: signature line: want %d bytes, got %d", 2+8+ed25519.SignatureSize, len(raw))
	}
	s.alg = [2]byte{raw[0], raw[1]}
	if s.alg != algLegacy && s.alg != algPrehashed {
		return s, fmt.Errorf("relmanifest: unsupported signature algorithm %q", string(raw[0:2]))
	}
	copy(s.keyID[:], raw[2:10])
	s.sig = append([]byte(nil), raw[10:]...)

	if !strings.HasPrefix(lines[1], trustedCommentPrefix) {
		return s, errors.New("relmanifest: signature file has no trusted comment")
	}
	s.trustedComment = strings.TrimRight(strings.TrimPrefix(lines[1], trustedCommentPrefix), "\r")

	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[2]))
	if err != nil {
		return s, fmt.Errorf("relmanifest: global signature line is not base64: %w", err)
	}
	if len(global) != ed25519.SignatureSize {
		return s, fmt.Errorf("relmanifest: global signature: want %d bytes, got %d", ed25519.SignatureSize, len(global))
	}
	s.globalSig = global
	return s, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// lastNonCommentLine returns the single payload line of a minisign public key:
// the file is a comment line plus a base64 line, but callers often paste just
// the base64 part.
func lastNonCommentLine(s string) (string, error) {
	lines := nonEmptyLines(s)
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "untrusted comment:") && !strings.HasPrefix(l, trustedCommentPrefix) {
			return l, nil
		}
	}
	return "", errors.New("no key line")
}
