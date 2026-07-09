package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/malmoos/malmo/internal/protocol"
)

// FileOpError carries the host-agent's error code, message, and HTTP status from
// a /v1/files/* call so the brain can map it to the right dashboard response
// (not-found → 404, exists → 409, no-space → 507, permission-denied → 403,
// invalid-path → 400). The file methods bypass do — which flattens every error
// to an opaque string — to keep this discrimination, mirroring ResolveHome.
// Check with errors.As.
type FileOpError struct {
	Code    string
	Message string
	Status  int
}

func (e *FileOpError) Error() string {
	return fmt.Sprintf("host-agent /v1/files: %s (%s, status %d)", e.Message, e.Code, e.Status)
}

// fileOpError builds a *FileOpError from a non-2xx host-agent response, reading
// the standard {code, message} body. A body that fails to decode still yields a
// usable error keyed on the HTTP status.
func fileOpError(resp *http.Response) *FileOpError {
	var e protocol.Error
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Code == "" {
		e.Code = "host-agent-error"
		e.Message = resp.Status
	}
	return &FileOpError{Code: e.Code, Message: e.Message, Status: resp.StatusCode}
}

// FilesList returns the directory listing at (root, path) for user.
func (c *Client) FilesList(ctx context.Context, user, root, path string) (protocol.FilesListResponse, error) {
	var out protocol.FilesListResponse
	err := c.filesDo(ctx, "/v1/files/list", protocol.FilesPathRequest{User: user, Root: root, Path: path}, &out)
	return out, err
}

// FilesMkdir creates a directory at (root, path) for user.
func (c *Client) FilesMkdir(ctx context.Context, user, root, path string) error {
	return c.filesDo(ctx, "/v1/files/mkdir", protocol.FilesPathRequest{User: user, Root: root, Path: path}, nil)
}

// FilesDelete permanently removes the file or tree at (root, path) for user.
func (c *Client) FilesDelete(ctx context.Context, user, root, path string) error {
	return c.filesDo(ctx, "/v1/files/delete", protocol.FilesPathRequest{User: user, Root: root, Path: path}, nil)
}

// FilesMove renames/moves from → to (which may cross roots) for user.
func (c *Client) FilesMove(ctx context.Context, user string, from, to protocol.FileLocation) error {
	return c.filesDo(ctx, "/v1/files/move", protocol.FilesTransferRequest{User: user, From: from, To: to}, nil)
}

// FilesCopy copies from → to (which may cross roots) for user.
func (c *Client) FilesCopy(ctx context.Context, user string, from, to protocol.FileLocation) error {
	return c.filesDo(ctx, "/v1/files/copy", protocol.FilesTransferRequest{User: user, From: from, To: to}, nil)
}

// filesDo posts a metadata file op, decoding out on success and returning a
// *FileOpError (status-preserving) on any non-2xx.
func (c *Client) filesDo(ctx context.Context, path string, body, out any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://agent"+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("host-agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fileOpError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// FilesOpen streams a file download from host-agent. The returned ReadCloser is
// the raw octet-stream response body; the caller closes it. It uses the
// timeout-less stream client (a transfer can take minutes) and reports any
// pre-stream failure — not-found, permission, is-a-directory — as a
// *FileOpError before a single byte flows.
func (c *Client) FilesOpen(ctx context.Context, user, root, path string) (io.ReadCloser, error) {
	q := url.Values{"user": {user}, "root": {root}, "path": {path}}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://agent/v1/files/content?"+q.Encode(), http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("host-agent unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fileOpError(resp)
	}
	return resp.Body, nil
}

// FilesSave streams an upload to host-agent, piping body straight through
// without buffering the whole file. It uses the timeout-less stream client and
// returns a *FileOpError on any non-2xx (e.g. no-space, permission-denied).
func (c *Client) FilesSave(ctx context.Context, user, root, path string, body io.Reader) error {
	q := url.Values{"user": {user}, "root": {root}, "path": {path}}
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://agent/v1/files/content?"+q.Encode(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("host-agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fileOpError(resp)
	}
	return nil
}
