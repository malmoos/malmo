//go:build linux

package diskusage

import "testing"

// TestDataDiskRealStatfs points the reporter at a real existing directory (the
// test's temp dir) and asserts a coherent statfs reading: a total > 0 and a free
// that fits within it. This exercises the real syscall path, not a fake.
func TestDataDiskRealStatfs(t *testing.T) {
	r := &Reporter{path: t.TempDir()}
	free, total := r.DataDisk()
	if total <= 0 {
		t.Fatalf("want total > 0, got %d", total)
	}
	if free < 0 || free > total {
		t.Fatalf("free %d out of range [0, %d]", free, total)
	}
}

// TestDataDiskMissingPathFailsOpen pins the fail-open contract: statfs on a
// nonexistent path returns (0, 0), which the brain reads as "not measured"
// rather than an error or a scary empty disk.
func TestDataDiskMissingPathFailsOpen(t *testing.T) {
	r := &Reporter{path: "/nonexistent/molma/data-drive-xyz"}
	free, total := r.DataDisk()
	if free != 0 || total != 0 {
		t.Fatalf("want (0, 0) on statfs error, got (%d, %d)", free, total)
	}
}
