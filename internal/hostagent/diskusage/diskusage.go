//go:build linux

// Package diskusage is host-agent's data-drive free/total reporter behind GET
// /v1/system/status (DataDiskFreeBytes/DataDiskTotalBytes). It is a thin
// syscall.Statfs wrapper over the data-drive mount (/srv/malmo) — pure Go, no
// cgo — backing the install-plan free_bytes figure (BRAIN_UI_PROTOCOL.md #
// install-plan) so the install dialog can warn before a download that won't fit.
//
// The figure is advisory: a statfs snapshot reserves nothing, so the brain
// treats it as a hint, never a gate. A statfs error (mount absent, path gone)
// fails open to (0, 0), which the brain reads as "not measured" rather than a
// scary empty disk — the same fail-open posture as the locus-B reporters.
package diskusage

import (
	"log/slog"
	"syscall"
)

// dataDiskMount is the data-drive mount point (STORAGE.md). Free space for an
// app install is measured here, not on the OS drive.
const dataDiskMount = "/srv/malmo"

// Reporter implements hostagent.DiskReporter. path is a field so tests can point
// it at a real temp dir without a fake syscall.
type Reporter struct {
	path string
}

// New returns a Reporter measuring the data-drive mount (/srv/malmo).
func New() *Reporter {
	return &Reporter{path: dataDiskMount}
}

// DataDisk returns the data drive's available and total bytes. free is the space
// an unprivileged writer can actually use (Bavail × Bsize — already net of the
// root reserve); total is the filesystem size (Blocks × Bsize). On a statfs
// error it fails open to (0, 0): the brain reads 0 as "not measured" and shows
// no free figure rather than a misleading empty disk.
func (r *Reporter) DataDisk() (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(r.path, &st); err != nil {
		slog.Error("diskusage: statfs failed", "path", r.path, "err", err)
		return 0, 0
	}
	bsize := int64(st.Bsize)
	return int64(st.Bavail) * bsize, int64(st.Blocks) * bsize
}
