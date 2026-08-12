package relmanifest

import (
	"fmt"
	"os"
	"path/filepath"
)

// Cache file names, kept beside each other in the state dir. The signature is
// stored next to the manifest because the pair is what gets re-verified on the
// next boot — a cached manifest without its signature would have to be trusted
// on the say-so of the file system, which is exactly what signing exists to
// avoid.
const (
	manifestFile  = "manifest.json"
	signatureFile = "manifest.json.minisig"
)

// ManifestPath is the cached manifest's path inside the state dir
// (RELEASE_MANIFEST.md # Failure modes names /var/lib/malmo/manifest.json).
func ManifestPath(dir string) string { return filepath.Join(dir, manifestFile) }

// SignaturePath is the cached signature's path inside the state dir.
func SignaturePath(dir string) string { return filepath.Join(dir, signatureFile) }

// Cached is a manifest read back from disk, together with the raw bytes and the
// signature it was stored with, so the caller can re-verify before trusting it.
type Cached struct {
	Manifest  Manifest
	Raw       []byte
	Signature string
}

// Save writes the manifest bytes and its signature to dir.
//
// The raw bytes are stored, not a re-marshalled struct. A re-marshal would
// drop the unknown fields Parse ignores and reorder the rest, and the stored
// signature covers the publisher's exact bytes — so a round trip through our
// struct would produce a file that can never verify again.
//
// Both files are written atomically. A box that loses power mid-write must come
// back to either the old pair of files or the new pair, never a manifest from
// one release beside a signature from another: that combination fails
// verification and would look like tampering.
func Save(dir string, raw []byte, signature string) error {
	if err := writeFileAtomic(ManifestPath(dir), raw, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(SignaturePath(dir), []byte(signature), 0o644)
}

// Load reads the cached manifest and re-verifies it with v before returning it.
//
// Re-verifying on read is the point of storing the signature. The cache is what
// an offline box acts on (RELEASE_MANIFEST.md # Failure modes: "keeps the
// last-known manifest ... updates pause until connectivity returns"), so
// trusting it unchecked would make the local file system a way around the
// signature — anything that can write /var/lib/malmo could then choose the
// version the box runs.
//
// A missing cache returns ok=false with a nil error: that is the normal state
// of a box that has never reached the CDN, not a failure.
func Load(dir string, v *Verifier) (c Cached, ok bool, err error) {
	raw, err := os.ReadFile(ManifestPath(dir))
	if os.IsNotExist(err) {
		return Cached{}, false, nil
	}
	if err != nil {
		return Cached{}, false, fmt.Errorf("relmanifest: read cached manifest: %w", err)
	}
	sig, err := os.ReadFile(SignaturePath(dir))
	if os.IsNotExist(err) {
		return Cached{}, false, fmt.Errorf("relmanifest: cached manifest has no signature file")
	}
	if err != nil {
		return Cached{}, false, fmt.Errorf("relmanifest: read cached signature: %w", err)
	}
	if _, err := v.Verify(raw, string(sig)); err != nil {
		return Cached{}, false, fmt.Errorf("relmanifest: cached manifest: %w", err)
	}
	m, err := Parse(raw)
	if err != nil {
		return Cached{}, false, err
	}
	return Cached{Manifest: m, Raw: raw, Signature: string(sig)}, true, nil
}

// writeFileAtomic writes data via a same-directory temp file, an fsync, and a
// rename, then fsyncs the directory so the rename itself survives a power cut.
// Same ceremony as the control-plane ledger, and for the same reason: this file
// is read on boot to decide what the box should run.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s to sync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
