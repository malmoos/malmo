package profile

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// writeMarker writes content to a marker file in a fresh temp dir and returns
// its path.
func writeMarker(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	return p
}

// captureWarn redirects slog to a buffer (warn level) for the test, so the
// warn-on-bad-marker contract can be asserted.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestRead(t *testing.T) {
	tests := []struct {
		name string
		// content is the marker file's bytes; nil means no file is written
		// (the absent-marker case).
		content  *string
		want     Profile
		wantWarn bool
	}{
		{name: "absent marker defaults to appliance, no warn", content: nil, want: Appliance, wantWarn: false},
		{name: "hosted", content: ptr("hosted"), want: Hosted},
		{name: "appliance", content: ptr("appliance"), want: Appliance},
		{name: "trailing newline tolerated", content: ptr("hosted\n"), want: Hosted},
		{name: "surrounding whitespace tolerated", content: ptr("  appliance \n"), want: Appliance},
		{name: "empty defaults to appliance and warns", content: ptr(""), want: Appliance, wantWarn: true},
		{name: "unknown value defaults to appliance and warns", content: ptr("cloud"), want: Appliance, wantWarn: true},
		{name: "mis-cased value is unknown and warns", content: ptr("HOSTED"), want: Appliance, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureWarn(t)
			path := filepath.Join(t.TempDir(), "does-not-exist")
			if tt.content != nil {
				path = writeMarker(t, *tt.content)
			}
			got := Read(path)
			if got != tt.want {
				t.Errorf("Read = %q, want %q", got, tt.want)
			}
			warned := strings.Contains(logs.String(), "level=WARN")
			if warned != tt.wantWarn {
				t.Errorf("warn logged = %v, want %v (logs: %q)", warned, tt.wantWarn, logs.String())
			}
		})
	}
}

// TestRead_UnreadableMarkerWarnsAndDefaults covers the present-but-unreadable
// branch (an error other than not-exist). A directory is a deterministic stand-in
// under any uid — unlike a 0o000 file, which root (CI) can still read.
func TestRead_UnreadableMarkerWarnsAndDefaults(t *testing.T) {
	logs := captureWarn(t)
	got := Read(t.TempDir())
	if got != Appliance {
		t.Errorf("Read(dir) = %q, want %q", got, Appliance)
	}
	if !strings.Contains(logs.String(), "unreadable") {
		t.Errorf("want unreadable warn, got logs: %q", logs.String())
	}
}
