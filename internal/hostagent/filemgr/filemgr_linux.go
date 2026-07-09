//go:build linux

// Package filemgr is the real, privileged host-agent file manager (FILES.md #
// Execution). It implements the hostagent.FileManager seam by running every
// operation in a child process re-exec'd as the requesting user's UID/GID —
// setresuid/setresgid via exec.Cmd.SysProcAttr.Credential, NOT in-process,
// because Go's setuid syscalls are per-OS-thread and unsafe under the M:N
// scheduler. Running as the user makes POSIX 0750/02770 the kernel-enforced
// backstop (a brain-side bug degrades to "denied," not "leaked"), gives created
// files correct ownership natively, and contains symlink attacks for free.
//
// It is isolated in its own package (imported only by cmd/host-agent-real) so
// the shared internal/hostagent package carries no privileged-exec surface,
// mirroring usermgr/pamverifier. The actual filesystem work is the same
// internal/hostagent/fileops primitives the fake runs — the difference is only
// the UID drop and the fork/frame plumbing here.
package filemgr

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	osuser "os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/malmoos/malmo/internal/hostagent/fileops"
	"github.com/malmoos/malmo/internal/protocol"
)

// WorkerArg is the argv[1] sentinel that puts the host-agent binary into
// file-worker mode. cmd/host-agent-real dispatches it to RunWorker before its
// normal startup.
const WorkerArg = "__fileworker"

// specEnv carries the JSON-encoded workerSpec to the child. Env (not argv) is
// used because /proc/<pid>/environ is readable only by the process owner and
// root — the child runs as the user, so its paths don't leak to other local
// users the way a world-readable /proc/<pid>/cmdline would.
const specEnv = "MALMO_FILEWORKER_SPEC"

// defaultSharedDir is the household shared tree (STORAGE.md # Permissions).
const defaultSharedDir = "/srv/malmo/shared"

// workerSpec is the op the parent hands the child via specEnv. Path is the
// resolved absolute target; Path2 is the destination for move/copy.
type workerSpec struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Path2 string `json:"path2,omitempty"`
}

// workerResult is the child's stdout for metadata/upload ops. ErrKind is empty
// on success; otherwise it names the error class so the parent reconstructs an
// errors.Is-matchable error (the host-agent handler maps it to a wire code).
type workerResult struct {
	Entries []protocol.FileEntry `json:"entries,omitempty"`
	ErrKind string               `json:"err_kind,omitempty"`
	ErrMsg  string               `json:"err_msg,omitempty"`
}

// LinuxFileManager implements hostagent.FileManager as the requesting user.
type LinuxFileManager struct {
	// SharedDir is the shared-tree root (default /srv/malmo/shared).
	SharedDir string
	// self is the host-agent-real executable path, re-exec'd as the worker.
	self string
}

// New returns a LinuxFileManager that re-execs the current binary as its worker.
func New() (*LinuxFileManager, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("filemgr: resolve executable: %w", err)
	}
	return &LinuxFileManager{SharedDir: defaultSharedDir, self: self}, nil
}

func (m *LinuxFileManager) List(user, root, path string) ([]protocol.FileEntry, error) {
	abs, cred, err := m.resolve(user, root, path)
	if err != nil {
		return nil, err
	}
	res, err := m.runJSON(cred, workerSpec{Op: "list", Path: abs}, nil)
	if err != nil {
		return nil, err
	}
	return res.Entries, nil
}

func (m *LinuxFileManager) Mkdir(user, root, path string) error {
	abs, cred, err := m.resolve(user, root, path)
	if err != nil {
		return err
	}
	_, err = m.runJSON(cred, workerSpec{Op: "mkdir", Path: abs}, nil)
	return err
}

func (m *LinuxFileManager) Delete(user, root, path string) error {
	abs, cred, err := m.resolve(user, root, path)
	if err != nil {
		return err
	}
	_, err = m.runJSON(cred, workerSpec{Op: "delete", Path: abs}, nil)
	return err
}

func (m *LinuxFileManager) Move(user string, from, to protocol.FileLocation) error {
	fromAbs, toAbs, cred, err := m.resolvePair(user, from, to)
	if err != nil {
		return err
	}
	_, err = m.runJSON(cred, workerSpec{Op: "move", Path: fromAbs, Path2: toAbs}, nil)
	return err
}

func (m *LinuxFileManager) Copy(user string, from, to protocol.FileLocation) error {
	fromAbs, toAbs, cred, err := m.resolvePair(user, from, to)
	if err != nil {
		return err
	}
	_, err = m.runJSON(cred, workerSpec{Op: "copy", Path: fromAbs, Path2: toAbs}, nil)
	return err
}

func (m *LinuxFileManager) Save(user, root, path string, body io.Reader) error {
	abs, cred, err := m.resolve(user, root, path)
	if err != nil {
		return err
	}
	_, err = m.runJSON(cred, workerSpec{Op: "save", Path: abs}, body)
	return err
}

// Open streams a download from a worker child. The child writes a header line
// ("OK\n" or "ERR\t<kind>\t<msg>\n") before any bytes, so a pre-stream failure
// (not-found, permission, is-a-directory) surfaces as a typed error and the
// returned ReadCloser only ever carries file bytes. Close reaps the child.
func (m *LinuxFileManager) Open(user, root, path string) (io.ReadCloser, error) {
	abs, cred, err := m.resolve(user, root, path)
	if err != nil {
		return nil, err
	}
	cmd := m.workerCmd(cred, workerSpec{Op: "open", Path: abs})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("filemgr open: start worker: %w", err)
	}
	br := bufio.NewReader(stdout)
	header, err := br.ReadString('\n')
	if err != nil {
		_ = stdout.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("filemgr open: no worker header: %w", err)
	}
	if kind, msg, isErr := parseErrHeader(header); isErr {
		_ = cmd.Wait()
		return nil, reconstruct(kind, msg)
	}
	return &workerReader{r: br, stdout: stdout, cmd: cmd}, nil
}

// workerReader adapts a worker child's stdout into an io.ReadCloser: reads pull
// file bytes; Close stops the child and reaps it.
type workerReader struct {
	r      *bufio.Reader
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (w *workerReader) Read(p []byte) (int, error) { return w.r.Read(p) }

func (w *workerReader) Close() error {
	_ = w.stdout.Close()
	return w.cmd.Wait()
}

// resolve looks up the user's identity, resolves the logical root to an absolute
// base, and joins the (containment-checked) relative path. It returns the
// absolute target and the drop-to-user credential for the worker.
func (m *LinuxFileManager) resolve(user, root, relPath string) (string, *syscall.Credential, error) {
	cred, home, err := m.credential(user)
	if err != nil {
		return "", nil, err
	}
	base, err := m.base(root, home)
	if err != nil {
		return "", nil, err
	}
	abs, err := fileops.Resolve(base, relPath)
	if err != nil {
		return "", nil, err
	}
	return abs, cred, nil
}

func (m *LinuxFileManager) resolvePair(user string, from, to protocol.FileLocation) (string, string, *syscall.Credential, error) {
	fromAbs, cred, err := m.resolve(user, from.Root, from.Path)
	if err != nil {
		return "", "", nil, err
	}
	toAbs, _, err := m.resolve(user, to.Root, to.Path)
	if err != nil {
		return "", "", nil, err
	}
	return fromAbs, toAbs, cred, nil
}

func (m *LinuxFileManager) base(root, home string) (string, error) {
	switch root {
	case "home":
		return home, nil
	case "shared":
		if m.SharedDir == "" {
			return defaultSharedDir, nil
		}
		return m.SharedDir, nil
	default:
		return "", fileops.ErrInvalidPath
	}
}

// credential resolves the user's uid/gid, home, and supplementary groups. The
// supplementary set matters: the shared tree is 02770 malmo-shared, so the
// worker must carry the user's group memberships or a shared write would be
// denied.
func (m *LinuxFileManager) credential(user string) (*syscall.Credential, string, error) {
	u, err := osuser.Lookup(user)
	if err != nil {
		var unknown osuser.UnknownUserError
		if errors.As(err, &unknown) {
			return nil, "", fmt.Errorf("filemgr: %w", fs.ErrNotExist)
		}
		return nil, "", fmt.Errorf("filemgr: lookup %q: %w", user, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("filemgr: parse uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("filemgr: parse gid %q: %w", u.Gid, err)
	}
	groupIDs, err := u.GroupIds()
	if err != nil {
		return nil, "", fmt.Errorf("filemgr: group ids for %q: %w", user, err)
	}
	groups := make([]uint32, 0, len(groupIDs))
	for _, g := range groupIDs {
		n, err := strconv.ParseUint(g, 10, 32)
		if err != nil {
			return nil, "", fmt.Errorf("filemgr: parse group %q: %w", g, err)
		}
		groups = append(groups, uint32(n))
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, u.HomeDir, nil
}

// workerCmd builds the re-exec command for a worker child with the drop-to-user
// credential and a minimal env carrying only the spec (no parent env leaks to
// the dropped-privilege child).
func (m *LinuxFileManager) workerCmd(cred *syscall.Credential, spec workerSpec) *exec.Cmd {
	specJSON, _ := json.Marshal(spec)
	cmd := exec.Command(m.self, WorkerArg)
	cmd.Env = []string{specEnv + "=" + string(specJSON)}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	return cmd
}

// runJSON runs a metadata/upload worker and decodes its JSON result, turning a
// worker-reported ErrKind back into an errors.Is-matchable error. stdin is the
// upload body for "save" and nil otherwise.
func (m *LinuxFileManager) runJSON(cred *syscall.Credential, spec workerSpec, stdin io.Reader) (workerResult, error) {
	cmd := m.workerCmd(cred, spec)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return workerResult{}, fmt.Errorf("filemgr %s: worker failed: %w", spec.Op, err)
	}
	var res workerResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		return workerResult{}, fmt.Errorf("filemgr %s: bad worker result: %w", spec.Op, err)
	}
	if res.ErrKind != "" {
		return workerResult{}, reconstruct(res.ErrKind, res.ErrMsg)
	}
	return res, nil
}

// classify maps a fileops/os error to a stable kind string for the wire between
// the worker child and the parent. reconstruct is its inverse.
func classify(err error) (kind, msg string) {
	msg = err.Error()
	switch {
	case errors.Is(err, fileops.ErrInvalidPath):
		return "invalid-path", msg
	case errors.Is(err, fileops.ErrIsDir):
		return "is-dir", msg
	case errors.Is(err, fs.ErrNotExist):
		return "not-found", msg
	case errors.Is(err, fs.ErrExist):
		return "exists", msg
	case errors.Is(err, fs.ErrPermission):
		return "permission", msg
	case errors.Is(err, syscall.ENOSPC):
		return "no-space", msg
	default:
		return "other", msg
	}
}

// reconstruct rebuilds an errors.Is-matchable error from a worker's kind/msg, so
// the host-agent handler's error mapping (writeFileErr) works identically for
// the real agent and the fake.
func reconstruct(kind, msg string) error {
	switch kind {
	case "invalid-path":
		return fmt.Errorf("%s: %w", msg, fileops.ErrInvalidPath)
	case "is-dir":
		return fmt.Errorf("%s: %w", msg, fileops.ErrIsDir)
	case "not-found":
		return fmt.Errorf("%s: %w", msg, fs.ErrNotExist)
	case "exists":
		return fmt.Errorf("%s: %w", msg, fs.ErrExist)
	case "permission":
		return fmt.Errorf("%s: %w", msg, fs.ErrPermission)
	case "no-space":
		return fmt.Errorf("%s: %w", msg, syscall.ENOSPC)
	default:
		if msg == "" {
			msg = "file operation failed"
		}
		return errors.New(msg)
	}
}

// parseErrHeader reads the worker's download header. "OK\n" → (_, _, false);
// "ERR\t<kind>\t<msg>\n" → (kind, msg, true).
func parseErrHeader(header string) (kind, msg string, isErr bool) {
	header = strings.TrimRight(header, "\n")
	rest, ok := strings.CutPrefix(header, "ERR\t")
	if !ok {
		return "", "", false
	}
	kind, msg, _ = strings.Cut(rest, "\t")
	return kind, msg, true
}
