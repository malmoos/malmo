package hostclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/malmoos/malmo/internal/hostagent"
	"github.com/malmoos/malmo/internal/protocol"
)

// startFileAgent mounts a real hostagent.Agent backed by a FakeFileManager over
// temp dirs on a UNIX socket, so these tests exercise the actual /v1/files/*
// wire seam (client ↔ socket ↔ handlers ↔ fileops), including the FileOpError
// status/code round-trip.
func startFileAgent(t *testing.T) (*Client, string, string) {
	t.Helper()
	home := t.TempDir()
	shared := t.TempDir()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a := hostagent.New(nil, hostagent.NewFakePublisher(""))
	a.Files = hostagent.NewFakeFileManager(home, shared)
	mux := http.NewServeMux()
	a.Mount(mux)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return New(sock), home, shared
}

func TestFilesClientListMkdir(t *testing.T) {
	c, home, _ := startFileAgent(t)
	ctx := context.Background()
	if err := c.FilesMkdir(ctx, "alex", "home", "Photos"); err != nil {
		t.Fatalf("FilesMkdir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(home, "Photos")); err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	out, err := c.FilesList(ctx, "alex", "home", "")
	if err != nil {
		t.Fatalf("FilesList: %v", err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Name != "Photos" {
		t.Fatalf("entries = %+v", out.Entries)
	}
}

func TestFilesClientErrorMapping(t *testing.T) {
	c, _, _ := startFileAgent(t)
	err := c.FilesDelete(context.Background(), "alex", "home", "gone.txt")
	var fe *FileOpError
	if !errors.As(err, &fe) {
		t.Fatalf("want *FileOpError, got %T: %v", err, err)
	}
	if fe.Status != http.StatusNotFound || fe.Code != "not-found" {
		t.Fatalf("got status=%d code=%q", fe.Status, fe.Code)
	}
	if fe.Error() == "" {
		t.Fatal("empty error string")
	}
}

func TestFilesClientMoveCopy(t *testing.T) {
	c, home, shared := startFileAgent(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	// copy home/a.txt → home/b.txt
	if err := c.FilesCopy(ctx, "alex",
		protocol.FileLocation{Root: "home", Path: "a.txt"},
		protocol.FileLocation{Root: "home", Path: "b.txt"}); err != nil {
		t.Fatalf("FilesCopy: %v", err)
	}
	// move home/a.txt → shared/a.txt
	if err := c.FilesMove(ctx, "alex",
		protocol.FileLocation{Root: "home", Path: "a.txt"},
		protocol.FileLocation{Root: "shared", Path: "a.txt"}); err != nil {
		t.Fatalf("FilesMove: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(shared, "a.txt")); err != nil || string(got) != "payload" {
		t.Fatalf("moved file wrong: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(home, "b.txt")); err != nil || string(got) != "payload" {
		t.Fatalf("copied file wrong: %q err=%v", got, err)
	}
}

func TestFilesClientUploadDownload(t *testing.T) {
	c, home, _ := startFileAgent(t)
	ctx := context.Background()
	if err := c.FilesSave(ctx, "alex", "home", "up.txt", bytes.NewReader([]byte("streamed"))); err != nil {
		t.Fatalf("FilesSave: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(home, "up.txt")); err != nil || string(got) != "streamed" {
		t.Fatalf("saved file wrong: %q err=%v", got, err)
	}
	rc, err := c.FilesOpen(ctx, "alex", "home", "up.txt")
	if err != nil {
		t.Fatalf("FilesOpen: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "streamed" {
		t.Fatalf("downloaded = %q", got)
	}
}

func TestFilesClientOpenNotFound(t *testing.T) {
	c, _, _ := startFileAgent(t)
	_, err := c.FilesOpen(context.Background(), "alex", "home", "nope.bin")
	var fe *FileOpError
	if !errors.As(err, &fe) || fe.Status != http.StatusNotFound {
		t.Fatalf("want 404 FileOpError, got %v", err)
	}
}
