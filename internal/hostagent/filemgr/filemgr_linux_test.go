//go:build linux

package filemgr

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/malmoos/malmo/internal/hostagent/fileops"
	"github.com/malmoos/malmo/internal/protocol"
)

func TestClassifyReconstructRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error // the sentinel errors.Is must match after the round-trip
	}{
		{"invalid-path", fileops.ErrInvalidPath, fileops.ErrInvalidPath},
		{"is-dir", fileops.ErrIsDir, fileops.ErrIsDir},
		{"not-found", fs.ErrNotExist, fs.ErrNotExist},
		{"exists", fs.ErrExist, fs.ErrExist},
		{"permission", fs.ErrPermission, fs.ErrPermission},
		{"no-space", syscall.ENOSPC, syscall.ENOSPC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, msg := classify(tc.err)
			got := reconstruct(kind, msg)
			if !errors.Is(got, tc.want) {
				t.Fatalf("round-trip lost the sentinel: kind=%q got=%v", kind, got)
			}
		})
	}
}

func TestReconstructOther(t *testing.T) {
	got := reconstruct("other", "boom")
	if got == nil || got.Error() != "boom" {
		t.Fatalf("want error 'boom', got %v", got)
	}
	if reconstruct("other", "").Error() == "" {
		t.Fatal("empty other message should get a default")
	}
}

func runWorkerResult(t *testing.T, spec workerSpec, stdin string) (workerResult, int) {
	t.Helper()
	var out bytes.Buffer
	code := runWorker(spec, strings.NewReader(stdin), &out)
	var res workerResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode worker result %q: %v", out.String(), err)
	}
	return res, code
}

func TestRunWorkerListAndMkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, code := runWorkerResult(t, workerSpec{Op: "list", Path: dir}, "")
	if code != 0 || res.ErrKind != "" {
		t.Fatalf("list: code=%d err=%q", code, res.ErrKind)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "note.txt" {
		t.Fatalf("list entries = %+v", res.Entries)
	}

	res, _ = runWorkerResult(t, workerSpec{Op: "mkdir", Path: filepath.Join(dir, "New")}, "")
	if res.ErrKind != "" {
		t.Fatalf("mkdir err = %q", res.ErrKind)
	}
	if info, err := os.Stat(filepath.Join(dir, "New")); err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestRunWorkerMkdirExists(t *testing.T) {
	dir := t.TempDir()
	res, _ := runWorkerResult(t, workerSpec{Op: "mkdir", Path: dir}, "")
	if res.ErrKind != "exists" {
		t.Fatalf("want exists, got %q", res.ErrKind)
	}
}

func TestRunWorkerDeleteNotFound(t *testing.T) {
	res, _ := runWorkerResult(t, workerSpec{Op: "delete", Path: filepath.Join(t.TempDir(), "gone")}, "")
	if res.ErrKind != "not-found" {
		t.Fatalf("want not-found, got %q", res.ErrKind)
	}
}

func TestRunWorkerMoveAndCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// copy a → b
	res, _ := runWorkerResult(t, workerSpec{Op: "copy", Path: src, Path2: filepath.Join(dir, "b.txt")}, "")
	if res.ErrKind != "" {
		t.Fatalf("copy err = %q", res.ErrKind)
	}
	// move a → c
	res, _ = runWorkerResult(t, workerSpec{Op: "move", Path: src, Path2: filepath.Join(dir, "c.txt")}, "")
	if res.ErrKind != "" {
		t.Fatalf("move err = %q", res.ErrKind)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still present after move")
	}
}

func TestRunWorkerSave(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "up.txt")
	res, code := runWorkerResult(t, workerSpec{Op: "save", Path: dst}, "streamed")
	if code != 0 || res.ErrKind != "" {
		t.Fatalf("save: code=%d err=%q", code, res.ErrKind)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "streamed" {
		t.Fatalf("saved content = %q err=%v", got, err)
	}
}

func TestRunWorkerOpen(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "movie.bin")
	if err := os.WriteFile(f, []byte("the-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runWorker(workerSpec{Op: "open", Path: f}, nil, &out); code != 0 {
		t.Fatalf("open: code=%d", code)
	}
	body, ok := strings.CutPrefix(out.String(), "OK\n")
	if !ok {
		t.Fatalf("missing OK header: %q", out.String())
	}
	if body != "the-bytes" {
		t.Fatalf("body = %q", body)
	}
}

func TestRunWorkerOpenNotFound(t *testing.T) {
	var out bytes.Buffer
	runWorker(workerSpec{Op: "open", Path: filepath.Join(t.TempDir(), "nope")}, nil, &out)
	kind, _, isErr := parseErrHeader(out.String())
	if !isErr || kind != "not-found" {
		t.Fatalf("want ERR not-found header, got %q", out.String())
	}
}

func TestRunWorkerUnknownOp(t *testing.T) {
	var out bytes.Buffer
	if code := runWorker(workerSpec{Op: "frobnicate"}, nil, &out); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestParseErrHeader(t *testing.T) {
	if _, _, isErr := parseErrHeader("OK\n"); isErr {
		t.Fatal("OK should not parse as error")
	}
	kind, msg, isErr := parseErrHeader("ERR\tno-space\tno space left\n")
	if !isErr || kind != "no-space" || msg != "no space left" {
		t.Fatalf("got kind=%q msg=%q isErr=%v", kind, msg, isErr)
	}
}

func TestResolveRejectsBadInput(t *testing.T) {
	me, err := osuser.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	m := &LinuxFileManager{SharedDir: t.TempDir()}

	if _, _, err := m.resolve(me.Username, "app-state", "x"); !errors.Is(err, fileops.ErrInvalidPath) {
		t.Fatalf("bad root: want ErrInvalidPath, got %v", err)
	}
	if _, _, err := m.resolve(me.Username, "home", "../../etc"); !errors.Is(err, fileops.ErrInvalidPath) {
		t.Fatalf("traversal: want ErrInvalidPath, got %v", err)
	}
}

func TestCredentialUnknownUser(t *testing.T) {
	m := &LinuxFileManager{}
	if _, _, err := m.credential("definitely-no-such-user-9f3c"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestCredentialResolvesCurrentUser(t *testing.T) {
	me, err := osuser.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	m := &LinuxFileManager{}
	cred, home, err := m.credential(me.Username)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	if home != me.HomeDir {
		t.Fatalf("home = %q, want %q", home, me.HomeDir)
	}
	if cred.Uid != uint32(mustAtoi(t, me.Uid)) {
		t.Fatalf("uid = %d, want %s", cred.Uid, me.Uid)
	}
}

func TestNew(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.self == "" {
		t.Fatal("self executable path empty")
	}
	if m.SharedDir != defaultSharedDir {
		t.Fatalf("SharedDir = %q, want %q", m.SharedDir, defaultSharedDir)
	}
}

func TestWorkerCmd(t *testing.T) {
	m := &LinuxFileManager{self: "/opt/malmo/host-agent"}
	cred := &syscall.Credential{Uid: 3001, Gid: 3001, Groups: []uint32{100, 3001}}
	cmd := m.workerCmd(cred, workerSpec{Op: "list", Path: "/home/alex/Photos"})

	if cmd.Path != "/opt/malmo/host-agent" {
		t.Fatalf("cmd.Path = %q", cmd.Path)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != WorkerArg {
		t.Fatalf("cmd.Args = %v", cmd.Args)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential != cred {
		t.Fatal("credential not set on SysProcAttr")
	}
	// The env carries exactly the spec, and it decodes back to what we passed.
	var specLine string
	for _, e := range cmd.Env {
		if v, ok := strings.CutPrefix(e, specEnv+"="); ok {
			specLine = v
		}
	}
	if specLine == "" {
		t.Fatalf("spec env not found in %v", cmd.Env)
	}
	var spec workerSpec
	if err := json.Unmarshal([]byte(specLine), &spec); err != nil {
		t.Fatalf("spec env not valid JSON: %v", err)
	}
	if spec.Op != "list" || spec.Path != "/home/alex/Photos" {
		t.Fatalf("decoded spec = %+v", spec)
	}
}

func TestResolvePair(t *testing.T) {
	me, err := osuser.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	shared := t.TempDir()
	m := &LinuxFileManager{SharedDir: shared}
	fromAbs, toAbs, cred, err := m.resolvePair(
		me.Username,
		protocol.FileLocation{Root: "home", Path: "a.txt"},
		protocol.FileLocation{Root: "shared", Path: "b.txt"},
	)
	if err != nil {
		t.Fatalf("resolvePair: %v", err)
	}
	if !strings.HasPrefix(fromAbs, me.HomeDir) {
		t.Fatalf("fromAbs = %q, want under %q", fromAbs, me.HomeDir)
	}
	if toAbs != filepath.Join(shared, "b.txt") {
		t.Fatalf("toAbs = %q", toAbs)
	}
	if cred == nil {
		t.Fatal("nil credential")
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-numeric id %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
