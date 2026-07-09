//go:build linux

package filemgr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/malmoos/malmo/internal/hostagent/fileops"
)

// RunWorker is the child entry point (argv[1] == WorkerArg). It runs as the
// requesting user's UID/GID — the parent dropped privilege via
// SysProcAttr.Credential — so every fileops call is kernel-checked against the
// user's own permissions. It reads the op from specEnv, runs it against os.Stdin
// / os.Stdout, and returns a process exit code. Errors are reported in-band
// (JSON ErrKind for metadata/upload, an ERR header for download), so a failed
// op still exits 0; a non-zero exit means the worker itself broke.
func RunWorker() int {
	var spec workerSpec
	if err := json.Unmarshal([]byte(os.Getenv(specEnv)), &spec); err != nil {
		fmt.Fprintf(os.Stderr, "fileworker: bad spec: %v\n", err)
		return 2
	}
	return runWorker(spec, os.Stdin, os.Stdout)
}

// runWorker is the testable core: it executes spec reading from stdin and
// writing to stdout, with no reliance on the process environment. Running it
// directly (no fork, no UID drop) exercises the op + framing logic; the real
// privilege drop is verified in the outer VM loop.
func runWorker(spec workerSpec, stdin io.Reader, stdout io.Writer) int {
	switch spec.Op {
	case "list":
		entries, err := fileops.List(spec.Path)
		return writeResult(stdout, workerResult{Entries: entries}, err)
	case "mkdir":
		return writeResult(stdout, workerResult{}, fileops.Mkdir(spec.Path))
	case "delete":
		return writeResult(stdout, workerResult{}, fileops.Delete(spec.Path))
	case "move":
		return writeResult(stdout, workerResult{}, fileops.Move(spec.Path, spec.Path2))
	case "copy":
		return writeResult(stdout, workerResult{}, fileops.Copy(spec.Path, spec.Path2))
	case "save":
		return writeResult(stdout, workerResult{}, fileops.Save(spec.Path, stdin))
	case "open":
		return runOpen(spec.Path, stdout)
	default:
		fmt.Fprintf(os.Stderr, "fileworker: unknown op %q\n", spec.Op)
		return 2
	}
}

// writeResult encodes a metadata/upload result to stdout, folding any op error
// into ErrKind/ErrMsg. It always returns exit 0 — the error is in the JSON, not
// the exit code.
func writeResult(stdout io.Writer, res workerResult, err error) int {
	if err != nil {
		res.ErrKind, res.ErrMsg = classify(err)
	}
	_ = json.NewEncoder(stdout).Encode(res)
	return 0
}

// runOpen streams a download: an "OK\n" header then the file bytes, or an
// "ERR\t<kind>\t<msg>\n" header if the file can't be opened. The header lets the
// parent surface a typed pre-stream error while the body stays pure bytes.
func runOpen(path string, stdout io.Writer) int {
	rc, err := fileops.Open(path)
	if err != nil {
		kind, msg := classify(err)
		fmt.Fprintf(stdout, "ERR\t%s\t%s\n", kind, sanitizeHeader(msg))
		return 0
	}
	defer rc.Close()
	if _, err := io.WriteString(stdout, "OK\n"); err != nil {
		return 1
	}
	if _, err := io.Copy(stdout, rc); err != nil {
		// The header (and some bytes) already went out; the parent is streaming
		// to the browser and can only observe the non-zero exit.
		return 1
	}
	return 0
}

// sanitizeHeader strips tabs/newlines so an error message can't break the
// tab-delimited single-line ERR header framing.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}
