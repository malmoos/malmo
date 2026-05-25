package healthsource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead_MissingFileReturnsEmpty(t *testing.T) {
	src := New(filepath.Join(t.TempDir(), "absent.json"))

	sh, err := src.Read()
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if sh.Findings == nil {
		t.Fatal("findings must be non-nil slice")
	}
	if len(sh.Findings) != 0 {
		t.Errorf("findings: want empty for missing file, got %v", sh.Findings)
	}
	if sh.CheckedAt == "" {
		t.Error("checked_at must be set even when file is missing")
	}
}

func TestRead_ValidJSONPassesThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	body := `{"checked_at":"2026-05-25T08:00:00Z","findings":[{"id":"data-drive-missing","details":"abc-123 absent"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	sh, err := New(path).Read()
	if err != nil {
		t.Fatalf("valid JSON must not error: %v", err)
	}
	if sh.CheckedAt != "2026-05-25T08:00:00Z" {
		t.Errorf("checked_at: want passthrough, got %q", sh.CheckedAt)
	}
	if len(sh.Findings) != 1 || sh.Findings[0].ID != "data-drive-missing" {
		t.Fatalf("findings: want passthrough, got %v", sh.Findings)
	}
}

func TestRead_MalformedJSONReturnsSyntheticFinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	sh, err := New(path).Read()
	if err == nil {
		t.Fatal("malformed JSON should surface error to caller (for the log)")
	}
	if len(sh.Findings) != 1 || sh.Findings[0].ID != "health-report-malformed" {
		t.Fatalf("findings: want single health-report-malformed, got %v", sh.Findings)
	}
}

func TestRead_NilFindingsNormalizedToEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := os.WriteFile(path, []byte(`{"checked_at":"2026-05-25T08:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sh, err := New(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if sh.Findings == nil {
		t.Fatal("missing findings field must normalize to empty slice, not nil")
	}
}
