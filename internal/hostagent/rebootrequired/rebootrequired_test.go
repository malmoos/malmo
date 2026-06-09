package rebootrequired

import (
	"os"
	"path/filepath"
	"testing"
)

// newReporter points a Reporter at temp paths so the tests exercise the real
// os.Stat / os.ReadFile code path (not an injected stub) against a real file —
// the "touch the flag" exercise the issue asks for, runnable without a real host.
func newReporter(dir string) *Reporter {
	return &Reporter{
		flagPath: filepath.Join(dir, "reboot-required"),
		pkgsPath: filepath.Join(dir, "reboot-required.pkgs"),
	}
}

// TestReadAbsentFlagIsHealthy: no flag file ⇒ no finding (no reboot pending).
func TestReadAbsentFlagIsHealthy(t *testing.T) {
	r := newReporter(t.TempDir())
	if f := r.Read(); f != nil {
		t.Fatalf("absent flag must report no findings, got %v", f)
	}
}

// TestReadPresentFlagRaisesWithPackages: a present flag plus a .pkgs list raises
// one reboot-required finding whose Details carries the package names.
func TestReadPresentFlagRaisesWithPackages(t *testing.T) {
	dir := t.TempDir()
	r := newReporter(dir)
	if err := os.WriteFile(r.flagPath, []byte("*** System restart required ***\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.pkgsPath, []byte("linux-image-6.8.0\nlibc6\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := r.Read()
	if len(got) != 1 {
		t.Fatalf("present flag must raise exactly one finding, got %v", got)
	}
	if got[0].ID != "reboot-required" {
		t.Errorf("finding ID: want reboot-required, got %q", got[0].ID)
	}
	if got[0].InstanceKey != "" {
		t.Errorf("reboot-required is box-wide (no instance key), got %q", got[0].InstanceKey)
	}
	if got[0].Details != "linux-image-6.8.0, libc6" {
		t.Errorf("Details must list the .pkgs packages, got %q", got[0].Details)
	}
}

// TestReadPresentFlagNoPkgsStillRaises: the flag alone is enough — a missing
// .pkgs file yields an empty detail, never suppresses the finding.
func TestReadPresentFlagNoPkgsStillRaises(t *testing.T) {
	dir := t.TempDir()
	r := newReporter(dir)
	if err := os.WriteFile(r.flagPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := r.Read()
	if len(got) != 1 || got[0].ID != "reboot-required" {
		t.Fatalf("flag without .pkgs must still raise reboot-required, got %v", got)
	}
	if got[0].Details != "" {
		t.Errorf("Details must be empty when .pkgs is absent, got %q", got[0].Details)
	}
}

// TestParsePackagesTrimsDedupesPreservesOrder pins the .pkgs parsing: blank lines
// dropped, whitespace trimmed, duplicates removed (update-notifier appends across
// upgrades), first-seen order kept.
func TestParsePackagesTrimsDedupesPreservesOrder(t *testing.T) {
	in := "linux-image-6.8.0\n\n  libc6 \nlinux-image-6.8.0\ndbus\n"
	got := parsePackages(in)
	want := []string{"linux-image-6.8.0", "libc6", "dbus"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}
