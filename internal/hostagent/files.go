package hostagent

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"syscall"

	"github.com/malmoos/malmo/internal/hostagent/fileops"
	"github.com/malmoos/malmo/internal/protocol"
)

// FileManager is the consumer-side seam for the in-dashboard file manager's
// filesystem work (FILES.md, /v1/files/*). Every method acts on behalf of user
// under a logical root — "home" (the user's /home/<user>/) or "shared"
// (/srv/malmo/shared/); the implementation resolves the root to an absolute
// base, re-validates path containment, and runs the op. The fake
// (FakeFileManager) runs it in-process as the dev operator; the real one
// (filemgr.LinuxFileManager) runs it in a child re-exec'd as the user's UID/GID
// so POSIX 0750/02770 is the kernel-enforced backstop and created files get
// correct ownership natively.
//
// Errors carry the standard os/fs sentinels (fs.ErrNotExist / fs.ErrExist /
// fs.ErrPermission, syscall.ENOSPC) plus fileops.ErrInvalidPath / ErrIsDir, so
// writeFileErr maps them to wire codes with errors.Is and no bespoke taxonomy.
type FileManager interface {
	List(user, root, path string) ([]protocol.FileEntry, error)
	Mkdir(user, root, path string) error
	Delete(user, root, path string) error
	Move(user string, from, to protocol.FileLocation) error
	Copy(user string, from, to protocol.FileLocation) error
	// Open returns a reader over the file for a streamed download; the caller
	// closes it. Any error (not-found, permission, is-a-directory) surfaces here,
	// before bytes flow, so the handler can still set a proper status.
	Open(user, root, path string) (io.ReadCloser, error)
	// Save writes body to the file for a streamed upload, replacing any existing
	// file. It reads body to completion (or until an error) without buffering.
	Save(user, root, path string, body io.Reader) error
}

func validRoot(root string) bool { return root == "home" || root == "shared" }

func (a *Agent) filesList(w http.ResponseWriter, r *http.Request) {
	var req protocol.FilesPathRequest
	if !decode(w, r, &req) {
		return
	}
	if !a.filesGuard(w, req.User, req.Root) {
		return
	}
	entries, err := a.Files.List(req.User, req.Root, req.Path)
	if err != nil {
		writeFileErr(w, "files.list", err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.FilesListResponse{Entries: entries})
}

func (a *Agent) filesMkdir(w http.ResponseWriter, r *http.Request) {
	var req protocol.FilesPathRequest
	if !decode(w, r, &req) {
		return
	}
	if !a.filesGuard(w, req.User, req.Root) {
		return
	}
	if err := a.Files.Mkdir(req.User, req.Root, req.Path); err != nil {
		writeFileErr(w, "files.mkdir", err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (a *Agent) filesDelete(w http.ResponseWriter, r *http.Request) {
	var req protocol.FilesPathRequest
	if !decode(w, r, &req) {
		return
	}
	if !a.filesGuard(w, req.User, req.Root) {
		return
	}
	if err := a.Files.Delete(req.User, req.Root, req.Path); err != nil {
		writeFileErr(w, "files.delete", err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (a *Agent) filesMove(w http.ResponseWriter, r *http.Request) {
	a.filesTransfer(w, r, "files.move", func(user string, from, to protocol.FileLocation) error {
		return a.Files.Move(user, from, to)
	})
}

func (a *Agent) filesCopy(w http.ResponseWriter, r *http.Request) {
	a.filesTransfer(w, r, "files.copy", func(user string, from, to protocol.FileLocation) error {
		return a.Files.Copy(user, from, to)
	})
}

func (a *Agent) filesTransfer(w http.ResponseWriter, r *http.Request, op string, do func(user string, from, to protocol.FileLocation) error) {
	var req protocol.FilesTransferRequest
	if !decode(w, r, &req) {
		return
	}
	if a.Files == nil {
		writeErr(w, http.StatusNotImplemented, "not-implemented", "file manager not available")
		return
	}
	if req.User == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "user is required")
		return
	}
	if !validRoot(req.From.Root) || !validRoot(req.To.Root) {
		writeErr(w, http.StatusBadRequest, "bad-request", "root must be home or shared")
		return
	}
	if err := do(req.User, req.From, req.To); err != nil {
		writeFileErr(w, op, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (a *Agent) filesDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	user, root, path := q.Get("user"), q.Get("root"), q.Get("path")
	if !a.filesGuard(w, user, root) {
		return
	}
	rc, err := a.Files.Open(user, root, path)
	if err != nil {
		writeFileErr(w, "files.download", err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, rc); err != nil {
		// Status is already 200 (bytes have flowed); a mid-stream error is a
		// client disconnect or a read fault we can only log.
		slog.Warn("file download interrupted", "user", user, "root", root, "err", err)
	}
}

func (a *Agent) filesUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	user, root, path := q.Get("user"), q.Get("root"), q.Get("path")
	if !a.filesGuard(w, user, root) {
		return
	}
	if err := a.Files.Save(user, root, path, r.Body); err != nil {
		writeFileErr(w, "files.upload", err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// filesGuard validates the seam is wired and the (user, root) are well-formed,
// writing the appropriate error and returning false when not. Shared by every
// single-location /v1/files/* handler.
func (a *Agent) filesGuard(w http.ResponseWriter, user, root string) bool {
	if a.Files == nil {
		writeErr(w, http.StatusNotImplemented, "not-implemented", "file manager not available")
		return false
	}
	if user == "" {
		writeErr(w, http.StatusBadRequest, "bad-request", "user is required")
		return false
	}
	if !validRoot(root) {
		writeErr(w, http.StatusBadRequest, "bad-request", "root must be home or shared")
		return false
	}
	return true
}

// writeFileErr maps an error from a FileManager op to the wire code + HTTP
// status the brain expects (BRAIN_HOST_PROTOCOL.md # Files endpoints). Anything
// unrecognized is a 500 — a real host fault the brain surfaces as a 502.
func writeFileErr(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, fileops.ErrInvalidPath):
		writeErr(w, http.StatusBadRequest, "invalid-path", "path escapes its root")
	case errors.Is(err, fileops.ErrIsDir):
		writeErr(w, http.StatusUnprocessableEntity, "is-a-directory", "path is a directory")
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not-found", "no such file or directory")
	case errors.Is(err, fs.ErrExist):
		writeErr(w, http.StatusConflict, "exists", "destination already exists")
	case errors.Is(err, fs.ErrPermission):
		writeErr(w, http.StatusForbidden, "permission-denied", "permission denied")
	case errors.Is(err, syscall.ENOSPC):
		writeErr(w, http.StatusInsufficientStorage, "no-space", "no space left on device")
	default:
		slog.Error("file op failed", "step", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "file-op-failed", "file operation failed")
	}
}
