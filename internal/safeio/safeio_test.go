package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesALooserFileWithoutWideningIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("contents = %q, want %q", b, "new")
	}
}

func TestWriteFileReplacesASymlinkInsteadOfFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "account.json")
	if err := os.WriteFile(target, []byte("real config"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "insights-7d-2026-09-16.html")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteFile(path, []byte("<html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "real config" {
		t.Errorf("the symlink target became %q; a planted link must not be written through", b)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the destination is still a symlink")
	}
}

func TestCopyFileMissingSourceLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.html")
	if err := CopyFile(filepath.Join(dir, "missing.html"), dst, 0o600); !os.IsNotExist(err) {
		t.Errorf("CopyFile from a missing source = %v, want a not-exist error", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed copy left %d file(s) in the directory", len(entries))
	}
}

func TestCopyFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.html")
	if err := os.WriteFile(src, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.html")
	if err := CopyFile(src, dst, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}
	if b, _ := os.ReadFile(dst); string(b) != "report" {
		t.Errorf("contents = %q, want %q", b, "report")
	}
}
