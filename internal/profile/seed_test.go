package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSeed writes content to a seed file in a fresh temp dir and returns its path.
func writeSeed(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return p
}

func TestReadSeed(t *testing.T) {
	tests := []struct {
		name string
		// content is the seed file's bytes; nil means no file is written
		// (the absent-seed case).
		content    *string
		wantErr    error  // sentinel to match with errors.Is; nil means "no error"
		wantErrSub string // substring the error must contain (when wantErr is nil but an error is expected)
		wantBox    string
		wantSecret string
	}{
		{
			name:       "valid seed",
			content:    ptr(`{"box_id":"cindy-fox","admin_bootstrap_secret":"s3cr3t"}`),
			wantBox:    "cindy-fox",
			wantSecret: "s3cr3t",
		},
		{
			name:       "surrounding whitespace trimmed",
			content:    ptr(`{"box_id":"  cindy-fox \n","admin_bootstrap_secret":"\t s3cr3t  "}`),
			wantBox:    "cindy-fox",
			wantSecret: "s3cr3t",
		},
		{
			name:    "absent file returns ErrSeedAbsent",
			content: nil,
			wantErr: ErrSeedAbsent,
		},
		{
			name:       "malformed JSON is a hard error",
			content:    ptr(`{not json`),
			wantErrSub: "parse seed",
		},
		{
			name:       "missing box_id is a hard error",
			content:    ptr(`{"admin_bootstrap_secret":"s3cr3t"}`),
			wantErrSub: "missing box_id",
		},
		{
			name:       "blank box_id is a hard error",
			content:    ptr(`{"box_id":"   ","admin_bootstrap_secret":"s3cr3t"}`),
			wantErrSub: "missing box_id",
		},
		{
			name:       "missing admin_bootstrap_secret is a hard error",
			content:    ptr(`{"box_id":"cindy-fox"}`),
			wantErrSub: "missing admin_bootstrap_secret",
		},
		{
			name:       "blank admin_bootstrap_secret is a hard error",
			content:    ptr(`{"box_id":"cindy-fox","admin_bootstrap_secret":"  "}`),
			wantErrSub: "missing admin_bootstrap_secret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "does-not-exist")
			if tt.content != nil {
				path = writeSeed(t, *tt.content)
			}
			got, err := ReadSeed(path)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ReadSeed err = %v, want errors.Is %v", err, tt.wantErr)
				}
			case tt.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("ReadSeed err = %v, want substring %q", err, tt.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("ReadSeed unexpected err: %v", err)
				}
				if got.BoxID != tt.wantBox {
					t.Errorf("BoxID = %q, want %q", got.BoxID, tt.wantBox)
				}
				if got.AdminBootstrapSecret != tt.wantSecret {
					t.Errorf("AdminBootstrapSecret = %q, want %q", got.AdminBootstrapSecret, tt.wantSecret)
				}
			}
		})
	}
}

// TestReadSeed_UnreadableIsHardError covers the present-but-unreadable branch (an
// error other than not-exist). A directory is a deterministic stand-in under any
// uid — unlike a 0o000 file, which root (CI) can still read. It must NOT be
// mistaken for the absent-seed case.
func TestReadSeed_UnreadableIsHardError(t *testing.T) {
	_, err := ReadSeed(t.TempDir())
	if err == nil {
		t.Fatal("ReadSeed(dir) err = nil, want error")
	}
	if errors.Is(err, ErrSeedAbsent) {
		t.Errorf("ReadSeed(dir) returned ErrSeedAbsent; an unreadable seed must be a hard error, not the absent case")
	}
}

// TestReadSeed_EnrollmentPreserved confirms the reserved enrollment block is
// carried through verbatim (C3b consumes it later) without ReadSeed validating it.
func TestReadSeed_EnrollmentPreserved(t *testing.T) {
	raw := `{"box_id":"cindy-fox","admin_bootstrap_secret":"s3cr3t","enrollment":{"token":"abc","zone":"cindy-fox.malmo.network"}}`
	got, err := ReadSeed(writeSeed(t, raw))
	if err != nil {
		t.Fatalf("ReadSeed: %v", err)
	}
	var enr struct {
		Token string `json:"token"`
		Zone  string `json:"zone"`
	}
	if err := json.Unmarshal(got.Enrollment, &enr); err != nil {
		t.Fatalf("enrollment not preserved as valid JSON: %v", err)
	}
	if enr.Token != "abc" || enr.Zone != "cindy-fox.malmo.network" {
		t.Errorf("enrollment = %+v, want token=abc zone=cindy-fox.malmo.network", enr)
	}
}
