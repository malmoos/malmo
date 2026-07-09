//go:build !linux

// Package filemgr — non-Linux stub. The real file manager needs Linux
// privilege-drop (setresuid via SysProcAttr.Credential), so on other platforms
// this stub keeps the package importable (cmd/host-agent-real compiles
// everywhere) while every operation reports unavailability. cmd/host-agent-real
// is only ever run on Linux; this exists purely so the cross-platform surface
// builds, mirroring pamverifier's _other.go.
package filemgr

import (
	"errors"
	"io"

	"github.com/malmoos/malmo/internal/protocol"
)

// WorkerArg matches the Linux constant so the argv dispatch in cmd/host-agent-real
// compiles on every platform.
const WorkerArg = "__fileworker"

var errUnsupported = errors.New("filemgr is not available on this platform (requires linux)")

// LinuxFileManager is a stub on non-Linux builds.
type LinuxFileManager struct{}

// New reports unavailability on non-Linux builds.
func New() (*LinuxFileManager, error) { return nil, errUnsupported }

// RunWorker is a no-op worker on non-Linux builds.
func RunWorker() int { return 2 }

func (*LinuxFileManager) List(_, _, _ string) ([]protocol.FileEntry, error) {
	return nil, errUnsupported
}
func (*LinuxFileManager) Mkdir(_, _, _ string) error  { return errUnsupported }
func (*LinuxFileManager) Delete(_, _, _ string) error { return errUnsupported }
func (*LinuxFileManager) Move(_ string, _, _ protocol.FileLocation) error {
	return errUnsupported
}
func (*LinuxFileManager) Copy(_ string, _, _ protocol.FileLocation) error {
	return errUnsupported
}
func (*LinuxFileManager) Open(_, _, _ string) (io.ReadCloser, error) { return nil, errUnsupported }
func (*LinuxFileManager) Save(_, _, _ string, _ io.Reader) error     { return errUnsupported }
