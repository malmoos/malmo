package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/malmoos/malmo/internal/health"
	"github.com/malmoos/malmo/internal/hostclient"
	"github.com/malmoos/malmo/internal/protocol"
)

// content issues a raw request to the streaming /api/v1/files/content endpoint,
// carrying the harness jar's cookies (the endpoint is not JSON, so the harness's
// do() helper doesn't fit).
func (h *harness) content(method, root, relPath string, body io.Reader) *http.Response {
	h.t.Helper()
	q := url.Values{"root": {root}, "path": {relPath}}
	req, err := http.NewRequest(method, h.srv.URL+"/api/v1/files/content?"+q.Encode(), body)
	if err != nil {
		h.t.Fatalf("new content request: %v", err)
	}
	for _, c := range h.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("content %s: %v", method, err)
	}
	return resp
}

type fileErrBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	IssueID string `json:"issue_id"`
}

func TestFilesListAndMkdir(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	if err := os.WriteFile(filepath.Join(h.fileHome, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := h.do("POST", "/api/v1/files/mkdir", map[string]string{"root": "home", "path": "Photos"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("mkdir: want 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do("POST", "/api/v1/files/list", map[string]string{"root": "home", "path": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: want 200, got %d", resp.StatusCode)
	}
	body := decodeJSON[protocol.FilesListResponse](t, resp)
	names := map[string]bool{}
	for _, e := range body.Entries {
		names[e.Name] = true
	}
	if !names["note.txt"] || !names["Photos"] {
		t.Fatalf("missing entries: %+v", body.Entries)
	}
}

func TestFilesListUnauthenticated(t *testing.T) {
	h := newHarness(t) // no login
	resp := h.do("POST", "/api/v1/files/list", map[string]string{"root": "home", "path": ""})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFilesInvalidRoot(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	resp := h.do("POST", "/api/v1/files/list", map[string]string{"root": "app-state", "path": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if code := decodeJSON[fileErrBody](t, resp).Code; code != "invalid-root" {
		t.Fatalf("want invalid-root, got %q", code)
	}
}

func TestFilesPathTraversalRejected(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	resp := h.do("POST", "/api/v1/files/list", map[string]string{"root": "home", "path": "../../etc"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if code := decodeJSON[fileErrBody](t, resp).Code; code != "invalid-path" {
		t.Fatalf("want invalid-path, got %q", code)
	}
}

func TestFilesDeleteNotFound(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	resp := h.do("POST", "/api/v1/files/delete", map[string]string{"root": "home", "path": "gone.txt"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	if code := decodeJSON[fileErrBody](t, resp).Code; code != "not-found" {
		t.Fatalf("want not-found, got %q", code)
	}
}

func TestFilesMkdirExists(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	if err := os.Mkdir(filepath.Join(h.fileHome, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp := h.do("POST", "/api/v1/files/mkdir", map[string]string{"root": "home", "path": "Photos"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	if code := decodeJSON[fileErrBody](t, resp).Code; code != "exists" {
		t.Fatalf("want exists, got %q", code)
	}
}

func TestFilesMoveAcrossRoots(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	if err := os.WriteFile(filepath.Join(h.fileHome, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := h.do("POST", "/api/v1/files/move", map[string]any{
		"from": map[string]string{"root": "home", "path": "a.txt"},
		"to":   map[string]string{"root": "shared", "path": "a.txt"},
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("move: want 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got, err := os.ReadFile(filepath.Join(h.fileShared, "a.txt")); err != nil || string(got) != "payload" {
		t.Fatalf("moved file wrong: %q err=%v", got, err)
	}
}

func TestFilesCopy(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	if err := os.WriteFile(filepath.Join(h.fileHome, "a.txt"), []byte("dup"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := h.do("POST", "/api/v1/files/copy", map[string]any{
		"from": map[string]string{"root": "home", "path": "a.txt"},
		"to":   map[string]string{"root": "home", "path": "b.txt"},
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("copy: want 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got, err := os.ReadFile(filepath.Join(h.fileHome, "b.txt")); err != nil || string(got) != "dup" {
		t.Fatalf("copied file wrong: %q err=%v", got, err)
	}
}

func TestFilesUploadDownloadRoundtrip(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")

	up := h.content(http.MethodPut, "home", "up.txt", bytes.NewReader([]byte("streamed-bytes")))
	if up.StatusCode != http.StatusNoContent {
		t.Fatalf("upload: want 204, got %d", up.StatusCode)
	}
	up.Body.Close()
	if got, err := os.ReadFile(filepath.Join(h.fileHome, "up.txt")); err != nil || string(got) != "streamed-bytes" {
		t.Fatalf("uploaded file wrong: %q err=%v", got, err)
	}

	dl := h.content(http.MethodGet, "home", "up.txt", nil)
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download: want 200, got %d", dl.StatusCode)
	}
	if ct := dl.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := dl.Header.Get("Content-Disposition"); cd != `attachment; filename="up.txt"` {
		t.Fatalf("content-disposition = %q", cd)
	}
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if string(got) != "streamed-bytes" {
		t.Fatalf("downloaded body = %q", got)
	}
}

func TestFilesDownloadNotFound(t *testing.T) {
	h := newHarness(t)
	h.setupAdmin("alex", "pw12345678")
	resp := h.content(http.MethodGet, "home", "nope.bin", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFileErrorHelpers(t *testing.T) {
	fe := &fileError{status: http.StatusConflict, Code: "exists", Message: "boom"}
	if fe.Error() != "boom" || fe.GetStatus() != http.StatusConflict {
		t.Fatalf("fileError: Error=%q status=%d", fe.Error(), fe.GetStatus())
	}

	// A *fileError passes through unchanged.
	if st, code, _ := fileErrResponse(fe); st != http.StatusConflict || code != "exists" {
		t.Fatalf("fileError passthrough: %d %q", st, code)
	}
	// A host error below 500 keeps its status + code.
	if st, code, _ := fileErrResponse(&hostclient.FileOpError{Status: 404, Code: "not-found", Message: "x"}); st != 404 || code != "not-found" {
		t.Fatalf("host <500: %d %q", st, code)
	}
	// A host 5xx (real host fault) becomes a 502.
	if st, _, _ := fileErrResponse(&hostclient.FileOpError{Status: 500, Code: "file-op-failed"}); st != http.StatusBadGateway {
		t.Fatalf("host 500 → %d, want 502", st)
	}
	// Anything else (unreachable, decode error) is a 502.
	if st, _, _ := fileErrResponse(errors.New("unreachable")); st != http.StatusBadGateway {
		t.Fatalf("other → %d, want 502", st)
	}

	if got := downloadName("Photos/2024/img.jpg"); got != "img.jpg" {
		t.Fatalf("downloadName = %q", got)
	}
	if got := downloadName(""); got != "download" {
		t.Fatalf("empty downloadName = %q", got)
	}
	if got := downloadName("a/\"quote\".txt"); got != "quote.txt" {
		t.Fatalf("quote-stripped downloadName = %q", got)
	}
}

func TestValidateFileTarget(t *testing.T) {
	if err := validateFileTarget("home", "Photos/x.jpg"); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := validateFileTarget("home", ""); err != nil {
		t.Fatalf("empty path: %v", err)
	}
	if validateFileTarget("bogus", "") == nil {
		t.Fatal("bad root should error")
	}
	if validateFileTarget("home", "../etc") == nil {
		t.Fatal("traversal should error")
	}
	if validateFileTarget("home", "/etc/passwd") == nil {
		t.Fatal("absolute should error")
	}
}

func TestFilesWriteBlockedByHealth(t *testing.T) {
	h := newHarness(t, func(s *Server) {
		hm := health.NewManager(nil)
		hm.Raise("data-drive-missing", "", "")
		s.health = hm
	})
	h.setupAdmin("alex", "pw12345678")

	// A write op (mkdir) is blocked with the health issue surfaced.
	resp := h.do("POST", "/api/v1/files/mkdir", map[string]string{"root": "home", "path": "New"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("mkdir under degraded box: want 409, got %d", resp.StatusCode)
	}
	body := decodeJSON[fileErrBody](t, resp)
	if body.Code != "blocked-by-health-issue" || body.IssueID != "data-drive-missing" {
		t.Fatalf("want blocked-by-health-issue/data-drive-missing, got %+v", body)
	}

	// An upload is blocked too.
	up := h.content(http.MethodPut, "home", "x.txt", bytes.NewReader([]byte("x")))
	if up.StatusCode != http.StatusConflict {
		t.Fatalf("upload under degraded box: want 409, got %d", up.StatusCode)
	}
	up.Body.Close()

	// A read (list) is NOT gated — the degraded box still browses.
	list := h.do("POST", "/api/v1/files/list", map[string]string{"root": "home", "path": ""})
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list under degraded box: want 200, got %d", list.StatusCode)
	}
	list.Body.Close()
}
