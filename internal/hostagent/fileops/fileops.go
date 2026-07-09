// Package fileops implements the pure filesystem primitives behind the
// in-dashboard file manager (FILES.md): directory listing, mkdir, move, copy,
// delete, and streamed open/save, plus lexical path containment.
//
// It is deliberately identity-agnostic: every function operates on an
// already-resolved absolute path and carries no notion of "which user" or any
// privilege logic. Two consumers run these exact primitives — the fake
// host-agent (hostagent.FakeFileManager, in-process as the dev operator) and
// the real host-agent's __fileworker child (re-exec'd as the requesting user's
// UID, internal/hostagent/filemgr). Keeping the primitives here means the same
// tested code path runs in both, and the privilege-drop plumbing stays a thin
// fork-and-frame shell around it.
//
// Errors are the plain os/fs errors (fs.ErrNotExist, fs.ErrExist,
// fs.ErrPermission, syscall.ENOSPC) so callers map them to wire codes with
// errors.Is without a bespoke error taxonomy.
package fileops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/malmoos/malmo/internal/protocol"
)

// ErrInvalidPath is returned by Resolve when a relative path escapes its root
// (is absolute, contains a ".." that climbs above the base, or holds a NUL).
// It denotes a malformed request, not a missing file — callers map it to 400.
var ErrInvalidPath = errors.New("invalid path")

// ErrIsDir is returned when a content transfer (download/upload) targets a
// directory. Callers map it to a validation error, not a not-found.
var ErrIsDir = errors.New("is a directory")

// Resolve joins a cleaned relative path onto an absolute base and returns the
// absolute target, rejecting anything that would escape the base. base must be
// an absolute, already-trusted root (the user's home or the shared tree); rel
// is the untrusted path from the request. This is the lexical containment
// layer; the kernel-enforced UID check in the real agent is the real backstop
// (FILES.md # Authorization).
func Resolve(base, rel string) (string, error) {
	base = filepath.Clean(base)
	if rel == "" || rel == "." {
		return base, nil
	}
	if strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", ErrInvalidPath
	}
	abs := filepath.Join(base, rel)
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", ErrInvalidPath
	}
	return abs, nil
}

// List returns the directory entries at abs. Dotfiles are included with
// Hidden=true (the UI filters them by default); directories report SizeBytes 0.
// Entries that vanish or become unreadable between the readdir and the stat are
// skipped rather than failing the whole listing.
func List(abs string) ([]protocol.FileEntry, error) {
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.FileEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := int64(0)
		if !e.IsDir() {
			size = info.Size()
		}
		out = append(out, protocol.FileEntry{
			Name:      e.Name(),
			Dir:       e.IsDir(),
			SizeBytes: size,
			Mtime:     info.ModTime().UTC().Format(time.RFC3339),
			Hidden:    strings.HasPrefix(e.Name(), "."),
		})
	}
	return out, nil
}

// Mkdir creates a single directory. It is Mkdir, not MkdirAll — the parent must
// already exist, so a client cannot materialize a chain of intermediate dirs it
// did not mean to. Returns fs.ErrExist if the name is taken.
func Mkdir(abs string) error {
	return os.Mkdir(abs, 0o755)
}

// Delete permanently removes a file or directory tree (no trash in v1). Missing
// targets return fs.ErrNotExist so the caller can 404 rather than silently
// succeeding. Uses Lstat so a symlink is removed as the link, not its target.
func Delete(abs string) error {
	if _, err := os.Lstat(abs); err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

// Move renames fromAbs to toAbs, refusing to clobber an existing destination.
// Falls back to copy-then-delete across filesystems (home and shared can sit on
// different mounts), so a home → shared move works even when os.Rename can't.
func Move(fromAbs, toAbs string) error {
	if err := checkNotExist(toAbs); err != nil {
		return err
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			if cerr := Copy(fromAbs, toAbs); cerr != nil {
				return cerr
			}
			return os.RemoveAll(fromAbs)
		}
		return err
	}
	return nil
}

// Copy duplicates fromAbs to toAbs (a file, or a directory tree recursively),
// refusing to clobber an existing destination. Symlinks are copied as symlinks.
func Copy(fromAbs, toAbs string) error {
	info, err := os.Lstat(fromAbs)
	if err != nil {
		return err
	}
	if err := checkNotExist(toAbs); err != nil {
		return err
	}
	switch {
	case info.IsDir():
		return copyTree(fromAbs, toAbs)
	case info.Mode()&fs.ModeSymlink != 0:
		return copySymlink(fromAbs, toAbs)
	default:
		return copyFile(fromAbs, toAbs, info.Mode())
	}
}

// Open returns a reader over the file at abs for a streamed download. The caller
// closes it. A directory target is rejected with ErrIsDir.
func Open(abs string) (io.ReadCloser, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s: %w", abs, ErrIsDir)
	}
	return os.Open(abs)
}

// Save writes r to the file at abs for a streamed upload, replacing any existing
// file (O_TRUNC — v1 has no resumable upload, so an interrupted transfer
// restarts). A short write from a full disk surfaces as syscall.ENOSPC.
func Save(abs string, r io.Reader) error {
	out, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// checkNotExist returns fs.ErrExist if abs already exists, nil if it does not,
// or the underlying error otherwise. Used to make move/copy non-clobbering.
func checkNotExist(abs string) error {
	_, err := os.Lstat(abs)
	if err == nil {
		return fmt.Errorf("%s: %w", abs, fs.ErrExist)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(target, dst)
}

func copyTree(src, dst string) error {
	if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			if err := copyTree(s, d); err != nil {
				return err
			}
		case info.Mode()&fs.ModeSymlink != 0:
			if err := copySymlink(s, d); err != nil {
				return err
			}
		default:
			if err := copyFile(s, d, info.Mode()); err != nil {
				return err
			}
		}
	}
	return nil
}
