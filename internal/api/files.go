package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/malmoos/malmo/internal/auth"
	"github.com/malmoos/malmo/internal/hostclient"
	"github.com/malmoos/malmo/internal/protocol"
)

// The in-dashboard file manager (FILES.md, BRAIN_UI_PROTOCOL.md # Files). The
// brain is policy + a transparent byte-pipe: it resolves the session to a user,
// accepts only the two logical roots (home | shared), rejects path traversal,
// and forwards to host-agent's /v1/files/*, which does the real work as the
// user's UID. File ops are NOT audited and do NOT trigger the elevation
// re-prompt — a user acting on their own content is ordinary use (FILES.md #
// Audit & elevation), deliberately unlike every mutation in users.go.

func (s *Server) registerFiles(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "files-list", Method: "POST", Path: "/api/v1/files/list",
		Summary: "List a directory in the file manager",
	}, s.filesList)
	huma.Register(api, huma.Operation{
		OperationID: "files-mkdir", Method: "POST", Path: "/api/v1/files/mkdir",
		Summary: "Create a folder", DefaultStatus: 204,
	}, s.filesMkdir)
	huma.Register(api, huma.Operation{
		OperationID: "files-move", Method: "POST", Path: "/api/v1/files/move",
		Summary: "Move or rename a file or folder", DefaultStatus: 204,
	}, s.filesMove)
	huma.Register(api, huma.Operation{
		OperationID: "files-copy", Method: "POST", Path: "/api/v1/files/copy",
		Summary: "Copy a file or folder", DefaultStatus: 204,
	}, s.filesCopy)
	huma.Register(api, huma.Operation{
		OperationID: "files-delete", Method: "POST", Path: "/api/v1/files/delete",
		Summary: "Delete a file or folder", DefaultStatus: 204,
	}, s.filesDelete)
}

func (s *Server) filesList(ctx context.Context, in *struct {
	Body struct {
		Root string `json:"root"`
		Path string `json:"path"`
	}
}) (*struct{ Body protocol.FilesListResponse }, error) {
	user, err := s.fileUser(ctx, in.Body.Root, in.Body.Path)
	if err != nil {
		return nil, err
	}
	out, err := s.host.FilesList(ctx, user, in.Body.Root, in.Body.Path)
	if err != nil {
		return nil, mapFileErr(err)
	}
	if out.Entries == nil {
		out.Entries = []protocol.FileEntry{}
	}
	return &struct{ Body protocol.FilesListResponse }{Body: out}, nil
}

func (s *Server) filesMkdir(ctx context.Context, in *struct {
	Body struct {
		Root string `json:"root"`
		Path string `json:"path"`
	}
}) (*struct{}, error) {
	user, err := s.fileWriteUser(ctx, in.Body.Root, in.Body.Path)
	if err != nil {
		return nil, err
	}
	if err := s.host.FilesMkdir(ctx, user, in.Body.Root, in.Body.Path); err != nil {
		return nil, mapFileErr(err)
	}
	return &struct{}{}, nil
}

func (s *Server) filesDelete(ctx context.Context, in *struct {
	Body struct {
		Root string `json:"root"`
		Path string `json:"path"`
	}
}) (*struct{}, error) {
	user, err := s.fileWriteUser(ctx, in.Body.Root, in.Body.Path)
	if err != nil {
		return nil, err
	}
	if err := s.host.FilesDelete(ctx, user, in.Body.Root, in.Body.Path); err != nil {
		return nil, mapFileErr(err)
	}
	return &struct{}{}, nil
}

func (s *Server) filesMove(ctx context.Context, in *struct {
	Body struct {
		From protocol.FileLocation `json:"from"`
		To   protocol.FileLocation `json:"to"`
	}
}) (*struct{}, error) {
	user, err := s.fileTransferUser(ctx, in.Body.From, in.Body.To)
	if err != nil {
		return nil, err
	}
	if err := s.host.FilesMove(ctx, user, in.Body.From, in.Body.To); err != nil {
		return nil, mapFileErr(err)
	}
	return &struct{}{}, nil
}

func (s *Server) filesCopy(ctx context.Context, in *struct {
	Body struct {
		From protocol.FileLocation `json:"from"`
		To   protocol.FileLocation `json:"to"`
	}
}) (*struct{}, error) {
	user, err := s.fileTransferUser(ctx, in.Body.From, in.Body.To)
	if err != nil {
		return nil, err
	}
	if err := s.host.FilesCopy(ctx, user, in.Body.From, in.Body.To); err != nil {
		return nil, mapFileErr(err)
	}
	return &struct{}{}, nil
}

// filesDownload streams a file to the browser (GET /api/v1/files/content). It is
// registered raw (not huma): a multi-gigabyte transfer is a streamed
// octet-stream body, the deliberate ">5s = job" exception (FILES.md #
// Transfers). The brain pipes bytes from host-agent without buffering.
func (s *Server) filesDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauthenticated(w)
		return
	}
	root, relPath := r.URL.Query().Get("root"), r.URL.Query().Get("path")
	if err := validateFileTarget(root, relPath); err != nil {
		writeFileContentError(w, err)
		return
	}
	rc, err := s.host.FilesOpen(r.Context(), id.User.Username, root, relPath)
	if err != nil {
		writeFileContentError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+downloadName(relPath)+"\"")
	if _, err := io.Copy(w, rc); err != nil {
		// Status is already 200 (bytes have flowed); a mid-stream break is a
		// client disconnect or a host read fault we can only log.
		slog.Warn("file download interrupted", "user", id.User.Username, "root", root, "err", err)
	}
}

// filesUpload streams an upload body to host-agent (PUT /api/v1/files/content).
// Raw, streamed, and health-gated like the write metadata ops.
func (s *Server) filesUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauthenticated(w)
		return
	}
	root, relPath := r.URL.Query().Get("root"), r.URL.Query().Get("path")
	if err := validateFileTarget(root, relPath); err != nil {
		writeFileContentError(w, err)
		return
	}
	if err := s.blockedByHealth(); err != nil {
		writeFileContentError(w, err)
		return
	}
	if err := s.host.FilesSave(r.Context(), id.User.Username, root, relPath, r.Body); err != nil {
		writeFileContentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fileUser resolves the session to a username and validates the (root, path) for
// a read op. Every file op runs as the session owner — there is no cross-user
// browse for any role (FILES.md # Authorization).
func (s *Server) fileUser(ctx context.Context, root, relPath string) (string, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return "", huma.Error401Unauthorized("unauthenticated")
	}
	if err := validateFileTarget(root, relPath); err != nil {
		return "", err
	}
	return id.User.Username, nil
}

// fileWriteUser is fileUser plus the write-blocking health gate (data drive
// missing → the box runs degraded and /home writes are blocked).
func (s *Server) fileWriteUser(ctx context.Context, root, relPath string) (string, error) {
	user, err := s.fileUser(ctx, root, relPath)
	if err != nil {
		return "", err
	}
	if err := s.blockedByHealth(); err != nil {
		return "", err
	}
	return user, nil
}

// fileTransferUser validates both endpoints of a move/copy and applies the write
// gate. A transfer may cross roots (home → shared).
func (s *Server) fileTransferUser(ctx context.Context, from, to protocol.FileLocation) (string, error) {
	id, ok := auth.FromContext(ctx)
	if !ok {
		return "", huma.Error401Unauthorized("unauthenticated")
	}
	if err := validateFileTarget(from.Root, from.Path); err != nil {
		return "", err
	}
	if err := validateFileTarget(to.Root, to.Path); err != nil {
		return "", err
	}
	if err := s.blockedByHealth(); err != nil {
		return "", err
	}
	return id.User.Username, nil
}

// blockedByHealth returns a 409 blocked-by-health-issue when the data drive is
// enrolled but absent (data-drive-missing → blocks_writes): /home and
// /srv/malmo writes are blocked and the box runs degraded (FILES.md # Failure
// modes, HEALTH.md # blocks_writes). Reads are never gated. Nil health manager
// (some test servers) means no gate.
func (s *Server) blockedByHealth() error {
	if s.health == nil {
		return nil
	}
	if iss, ok := s.health.Get("data-drive-missing", ""); ok && iss.BlocksWrites {
		return &fileError{status: http.StatusConflict, Code: "blocked-by-health-issue", Message: iss.Summary, IssueID: iss.ID}
	}
	return nil
}

// validateFileTarget accepts only the two logical roots and rejects path
// traversal (absolute paths, any ".." segment) before forwarding. host-agent
// re-validates as the UID (the kernel-enforced backstop); this is the
// user-visible policy layer (FILES.md # Authorization).
func validateFileTarget(root, relPath string) error {
	if root != "home" && root != "shared" {
		return &fileError{status: http.StatusBadRequest, Code: "invalid-root", Message: "root must be home or shared"}
	}
	if relPath == "" || relPath == "." {
		return nil
	}
	if strings.ContainsRune(relPath, 0) || path.IsAbs(relPath) {
		return invalidPathErr()
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".." {
			return invalidPathErr()
		}
	}
	return nil
}

func invalidPathErr() error {
	return &fileError{status: http.StatusBadRequest, Code: "invalid-path", Message: "path escapes its root"}
}

// downloadName is the browser-facing filename for a download: the last path
// segment, or "download" for an empty/odd path.
func downloadName(relPath string) string {
	base := path.Base(relPath)
	if base == "." || base == "/" || base == "" {
		return "download"
	}
	// Strip quotes so the Content-Disposition header can't be broken out of.
	return strings.NewReplacer("\"", "", "\\", "", "\n", "", "\r", "").Replace(base)
}

// fileError is a status-carrying wire error for the file surface. huma marshals
// a returned error's own exported fields when it implements StatusError, so the
// dashboard gets the FILES.md {code, message, issue_id?} shape rather than
// huma's detail-carries-code default (BRAIN_UI_PROTOCOL.md # Files). The same
// type serves the raw content handlers via writeFileContentError.
type fileError struct {
	status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	IssueID string `json:"issue_id,omitempty"`
}

func (e *fileError) Error() string  { return e.Message }
func (e *fileError) GetStatus() int { return e.status }

// fileErrResponse reduces any file-op error to (status, code, message). A host
// error keeps its status/code (400/403/404/409/422/507); a host 5xx (real host
// fault) or an unreachable/decoding error becomes a 502.
func fileErrResponse(err error) (int, string, string) {
	var fe *fileError
	if errors.As(err, &fe) {
		return fe.status, fe.Code, fe.Message
	}
	var hostErr *hostclient.FileOpError
	if errors.As(err, &hostErr) {
		if hostErr.Status >= 500 {
			return http.StatusBadGateway, "host-agent-error", "host-agent file op failed"
		}
		return hostErr.Status, hostErr.Code, hostErr.Message
	}
	return http.StatusBadGateway, "host-agent-error", "host-agent unreachable"
}

// mapFileErr wraps a downstream file-op error as a StatusError huma can emit.
func mapFileErr(err error) error {
	status, code, msg := fileErrResponse(err)
	return &fileError{status: status, Code: code, Message: msg}
}

// writeFileContentError writes the {code, message} envelope for the raw content
// handlers (which sit outside huma's error path).
func writeFileContentError(w http.ResponseWriter, err error) {
	status, code, msg := fileErrResponse(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.Error{Code: code, Message: msg})
}
