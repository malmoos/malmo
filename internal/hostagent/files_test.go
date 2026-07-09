package hostagent

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/malmoos/malmo/internal/hostagent/fileops"
	"github.com/malmoos/malmo/internal/protocol"
)

func newFilesAgent(t *testing.T) (*http.ServeMux, string, string) {
	t.Helper()
	home := t.TempDir()
	shared := t.TempDir()
	a, mux := newTestAgent(&stubVerifier{})
	a.Files = NewFakeFileManager(home, shared)
	return mux, home, shared
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// contentReq builds a raw GET/PUT to /v1/files/content with query params and an
// optional body (the streaming endpoints don't take a JSON body).
func contentReq(t *testing.T, mux *http.ServeMux, method, user, root, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"user": {user}, "root": {root}, "path": {path}}
	req := httptest.NewRequest(method, "/v1/files/content?"+q.Encode(), body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestFilesListReturnsEntries(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	writeFile(t, filepath.Join(home, "note.txt"), "hi")
	if err := os.Mkdir(filepath.Join(home, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := post(t, mux, "/v1/files/list", protocol.FilesPathRequest{User: "alex", Root: "home", Path: ""})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body)
	}
	resp := decodeBody[protocol.FilesListResponse](t, w)
	names := map[string]bool{}
	for _, e := range resp.Entries {
		names[e.Name] = true
	}
	if !names["note.txt"] || !names["Photos"] {
		t.Fatalf("missing entries: %+v", resp.Entries)
	}
}

func TestFilesListInvalidRoot(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/list", protocol.FilesPathRequest{User: "alex", Root: "app-state"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestFilesListMissingUser(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/list", protocol.FilesPathRequest{Root: "home"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestFilesListNotWired(t *testing.T) {
	_, mux := newTestAgent(&stubVerifier{}) // Files left nil
	w := post(t, mux, "/v1/files/list", protocol.FilesPathRequest{User: "alex", Root: "home"})
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", w.Code)
	}
}

func TestFilesPathTraversalRejected(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/list", protocol.FilesPathRequest{User: "alex", Root: "home", Path: "../../etc"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if code := decodeBody[protocol.Error](t, w).Code; code != "invalid-path" {
		t.Fatalf("want invalid-path, got %q", code)
	}
}

func TestFilesMkdir(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/mkdir", protocol.FilesPathRequest{User: "alex", Root: "home", Path: "New"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body)
	}
	if info, err := os.Stat(filepath.Join(home, "New")); err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestFilesMkdirExists(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	if err := os.Mkdir(filepath.Join(home, "New"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := post(t, mux, "/v1/files/mkdir", protocol.FilesPathRequest{User: "alex", Root: "home", Path: "New"})
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
	if code := decodeBody[protocol.Error](t, w).Code; code != "exists" {
		t.Fatalf("want exists, got %q", code)
	}
}

func TestFilesDeleteNotFound(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/delete", protocol.FilesPathRequest{User: "alex", Root: "home", Path: "gone.txt"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestFilesDelete(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	writeFile(t, filepath.Join(home, "gone.txt"), "x")
	w := post(t, mux, "/v1/files/delete", protocol.FilesPathRequest{User: "alex", Root: "home", Path: "gone.txt"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(home, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestFilesMoveAcrossRoots(t *testing.T) {
	mux, home, shared := newFilesAgent(t)
	writeFile(t, filepath.Join(home, "a.txt"), "payload")
	w := post(t, mux, "/v1/files/move", protocol.FilesTransferRequest{
		User: "alex",
		From: protocol.FileLocation{Root: "home", Path: "a.txt"},
		To:   protocol.FileLocation{Root: "shared", Path: "a.txt"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body)
	}
	if got, err := os.ReadFile(filepath.Join(shared, "a.txt")); err != nil || string(got) != "payload" {
		t.Fatalf("moved file wrong: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}
}

func TestFilesCopyClobber(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	writeFile(t, filepath.Join(home, "a.txt"), "one")
	writeFile(t, filepath.Join(home, "b.txt"), "two")
	w := post(t, mux, "/v1/files/copy", protocol.FilesTransferRequest{
		User: "alex",
		From: protocol.FileLocation{Root: "home", Path: "a.txt"},
		To:   protocol.FileLocation{Root: "home", Path: "b.txt"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
}

func TestFilesTransferInvalidRoot(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := post(t, mux, "/v1/files/move", protocol.FilesTransferRequest{
		User: "alex",
		From: protocol.FileLocation{Root: "home", Path: "a"},
		To:   protocol.FileLocation{Root: "elsewhere", Path: "a"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestFilesDownload(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	writeFile(t, filepath.Join(home, "movie.bin"), "the-bytes")
	w := contentReq(t, mux, http.MethodGet, "alex", "home", "movie.bin", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if w.Body.String() != "the-bytes" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestFilesDownloadNotFound(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := contentReq(t, mux, http.MethodGet, "alex", "home", "nope.bin", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestFilesDownloadRejectsDir(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	if err := os.Mkdir(filepath.Join(home, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := contentReq(t, mux, http.MethodGet, "alex", "home", "Photos", nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", w.Code)
	}
}

func TestFilesUploadThenDownload(t *testing.T) {
	mux, home, _ := newFilesAgent(t)
	w := contentReq(t, mux, http.MethodPut, "alex", "home", "up.txt", bytes.NewReader([]byte("uploaded")))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: want 200, got %d (%s)", w.Code, w.Body)
	}
	if got, err := os.ReadFile(filepath.Join(home, "up.txt")); err != nil || string(got) != "uploaded" {
		t.Fatalf("uploaded file wrong: %q err=%v", got, err)
	}
	dl := contentReq(t, mux, http.MethodGet, "alex", "home", "up.txt", nil)
	if dl.Body.String() != "uploaded" {
		t.Fatalf("roundtrip body = %q", dl.Body.String())
	}
}

func TestFilesUploadInvalidRoot(t *testing.T) {
	mux, _, _ := newFilesAgent(t)
	w := contentReq(t, mux, http.MethodPut, "alex", "bogus", "up.txt", bytes.NewReader([]byte("x")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestWriteFileErrMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{fileops.ErrInvalidPath, http.StatusBadRequest, "invalid-path"},
		{fileops.ErrIsDir, http.StatusUnprocessableEntity, "is-a-directory"},
		{fs.ErrNotExist, http.StatusNotFound, "not-found"},
		{fs.ErrExist, http.StatusConflict, "exists"},
		{fs.ErrPermission, http.StatusForbidden, "permission-denied"},
		{syscall.ENOSPC, http.StatusInsufficientStorage, "no-space"},
		{errors.New("boom"), http.StatusInternalServerError, "file-op-failed"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeFileErr(w, "test", tc.err)
		if w.Code != tc.status {
			t.Errorf("%v: status = %d, want %d", tc.err, w.Code, tc.status)
		}
		if code := decodeBody[protocol.Error](t, w).Code; code != tc.code {
			t.Errorf("%v: code = %q, want %q", tc.err, code, tc.code)
		}
	}
}
