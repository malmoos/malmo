package fileops

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name    string
		rel     string
		want    string
		wantErr bool
	}{
		{"empty is base", "", base, false},
		{"dot is base", ".", base, false},
		{"simple child", "Photos", filepath.Join(base, "Photos"), false},
		{"nested child", "Photos/2024/img.jpg", filepath.Join(base, "Photos/2024/img.jpg"), false},
		{"interior dotdot stays inside", "a/../b", filepath.Join(base, "b"), false},
		{"escape via dotdot", "../secret", "", true},
		{"escape via nested dotdot", "a/../../secret", "", true},
		{"absolute rejected", "/etc/passwd", "", true},
		{"nul rejected", "a\x00b", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(base, tc.rel)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidPath) {
					t.Fatalf("want ErrInvalidPath, got %v (path %q)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestList(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "note.txt"), "hello")
	writeFile(t, filepath.Join(base, ".hidden"), "x")
	if err := os.Mkdir(filepath.Join(base, "Photos"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := List(base)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]struct {
		dir    bool
		size   int64
		hidden bool
	}{}
	for _, e := range entries {
		byName[e.Name] = struct {
			dir    bool
			size   int64
			hidden bool
		}{e.Dir, e.SizeBytes, e.Hidden}
		if e.Mtime == "" {
			t.Errorf("entry %q has empty mtime", e.Name)
		}
	}
	if got := byName["note.txt"]; got.dir || got.size != 5 || got.hidden {
		t.Errorf("note.txt: got %+v", got)
	}
	if got := byName[".hidden"]; !got.hidden {
		t.Errorf(".hidden: expected hidden=true, got %+v", got)
	}
	if got := byName["Photos"]; !got.dir || got.size != 0 {
		t.Errorf("Photos: expected dir with size 0, got %+v", got)
	}
}

func TestListNotFound(t *testing.T) {
	_, err := List(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestMkdir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "New")
	if err := Mkdir(dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if err := Mkdir(dir); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("re-mkdir: want ErrExist, got %v", err)
	}
	if err := Mkdir(filepath.Join(base, "missing", "child")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mkdir under missing parent: want ErrNotExist, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	base := t.TempDir()
	f := filepath.Join(base, "gone.txt")
	writeFile(t, f, "x")
	if err := Delete(f); err != nil {
		t.Fatalf("Delete file: %v", err)
	}
	if _, err := os.Lstat(f); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}

	tree := filepath.Join(base, "tree")
	mustMkdir(t, tree)
	writeFile(t, filepath.Join(tree, "a.txt"), "a")
	if err := Delete(tree); err != nil {
		t.Fatalf("Delete tree: %v", err)
	}

	if err := Delete(filepath.Join(base, "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("delete missing: want ErrNotExist, got %v", err)
	}
}

func TestMove(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	dst := filepath.Join(base, "b.txt")
	writeFile(t, src, "data")
	if err := Move(src, dst); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got := readFile(t, dst); got != "data" {
		t.Fatalf("moved content = %q", got)
	}
	if _, err := os.Lstat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still present: %v", err)
	}
}

func TestMoveRefusesClobber(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	dst := filepath.Join(base, "b.txt")
	writeFile(t, src, "one")
	writeFile(t, dst, "two")
	if err := Move(src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	if got := readFile(t, dst); got != "two" {
		t.Fatalf("destination clobbered: %q", got)
	}
}

func TestMoveMissingSource(t *testing.T) {
	base := t.TempDir()
	err := Move(filepath.Join(base, "nope"), filepath.Join(base, "dst"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	dst := filepath.Join(base, "b.txt")
	writeFile(t, src, "payload")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := readFile(t, dst); got != "payload" {
		t.Fatalf("copied content = %q", got)
	}
	if got := readFile(t, src); got != "payload" {
		t.Fatalf("source altered: %q", got)
	}
}

func TestCopyTree(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustMkdir(t, src)
	mustMkdir(t, filepath.Join(src, "sub"))
	writeFile(t, filepath.Join(src, "top.txt"), "top")
	writeFile(t, filepath.Join(src, "sub", "deep.txt"), "deep")

	dst := filepath.Join(base, "dst")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy tree: %v", err)
	}
	if got := readFile(t, filepath.Join(dst, "top.txt")); got != "top" {
		t.Fatalf("top.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dst, "sub", "deep.txt")); got != "deep" {
		t.Fatalf("sub/deep.txt = %q", got)
	}
}

func TestCopySymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target.txt")
	writeFile(t, target, "t")
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	dst := filepath.Join(base, "link-copy")
	if err := Copy(link, dst); err != nil {
		t.Fatalf("Copy symlink: %v", err)
	}
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if got != target {
		t.Fatalf("symlink target = %q, want %q", got, target)
	}
}

func TestCopyRefusesClobber(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	dst := filepath.Join(base, "b.txt")
	writeFile(t, src, "one")
	writeFile(t, dst, "two")
	if err := Copy(src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
}

func TestCopyFileDestParentMissing(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	writeFile(t, src, "x")
	// Parent "missing/" does not exist, so creating the destination file fails.
	err := Copy(src, filepath.Join(base, "missing", "b.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestCopyTreeDestParentMissing(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustMkdir(t, src)
	writeFile(t, filepath.Join(src, "f.txt"), "x")
	err := Copy(src, filepath.Join(base, "missing", "dst"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestCopyDestParentIsFile(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "a.txt")
	writeFile(t, src, "x")
	notDir := filepath.Join(base, "afile")
	writeFile(t, notDir, "y")
	// Lstat of "<file>/child" yields ENOTDIR — a non-ErrNotExist error that the
	// clobber check must surface rather than treat as "destination is free".
	err := Copy(src, filepath.Join(notDir, "child"))
	if err == nil || errors.Is(err, fs.ErrExist) {
		t.Fatalf("want a non-clobber error, got %v", err)
	}
}

func TestCopyUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	base := t.TempDir()
	src := filepath.Join(base, "secret.txt")
	writeFile(t, src, "x")
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o644) })
	if err := Copy(src, filepath.Join(base, "copy.txt")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want ErrPermission, got %v", err)
	}
}

func TestCopyTreeWithUnreadableChild(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	mustMkdir(t, src)
	child := filepath.Join(src, "secret.txt")
	writeFile(t, child, "x")
	if err := os.Chmod(child, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(child, 0o644) })
	if err := Copy(src, filepath.Join(base, "dst")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want ErrPermission, got %v", err)
	}
}

func TestCopyTreeNestedUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	sub := filepath.Join(src, "sub")
	mustMkdir(t, src)
	mustMkdir(t, sub)
	secret := filepath.Join(sub, "secret.txt")
	writeFile(t, secret, "x")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o644) })
	// The error must propagate up through the recursive copyTree of "sub".
	if err := Copy(src, filepath.Join(base, "dst")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want ErrPermission, got %v", err)
	}
}

func TestOpen(t *testing.T) {
	base := t.TempDir()
	f := filepath.Join(base, "movie.bin")
	writeFile(t, f, "bytes")
	rc, err := Open(f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "bytes" {
		t.Fatalf("content = %q", got)
	}
}

func TestOpenRejectsDir(t *testing.T) {
	base := t.TempDir()
	if _, err := Open(base); !errors.Is(err, ErrIsDir) {
		t.Fatalf("want ErrIsDir, got %v", err)
	}
}

func TestOpenNotFound(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestSave(t *testing.T) {
	base := t.TempDir()
	f := filepath.Join(base, "up.txt")
	if err := Save(f, strings.NewReader("first")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readFile(t, f); got != "first" {
		t.Fatalf("content = %q", got)
	}
	// O_TRUNC: a second Save fully replaces the file, no leftover tail.
	if err := Save(f, strings.NewReader("hi")); err != nil {
		t.Fatalf("Save overwrite: %v", err)
	}
	if got := readFile(t, f); got != "hi" {
		t.Fatalf("overwritten content = %q", got)
	}
}

func TestSaveIntoMissingDir(t *testing.T) {
	err := Save(filepath.Join(t.TempDir(), "missing", "x.txt"), strings.NewReader("x"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestSaveReaderError(t *testing.T) {
	sentinel := errors.New("boom")
	err := Save(filepath.Join(t.TempDir(), "up.txt"), errReader{sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestCopyMissingSource(t *testing.T) {
	base := t.TempDir()
	err := Copy(filepath.Join(base, "nope"), filepath.Join(base, "dst"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

// --- helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// errReader fails on the first Read, simulating a transfer that breaks partway.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
