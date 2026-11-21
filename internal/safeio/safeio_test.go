package safeio

import (
	"os"
	"path/filepath"
	"strings"
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

// Every way write can fail must return an error naming the step and must not
// leave its temporary file beside the destination, since that file holds the
// same data the destination would have.
func TestWriteFailuresAreReportedAndCleanedUp(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the source (for CopyFile, or "" for WriteFile), the
		// destination, and the directory to inspect for leftovers.
		setup   func(t *testing.T) (src, dst, dir string)
		wantMsg string
		root    bool // the failure depends on file modes root ignores
	}{
		{
			name: "destination directory does not exist",
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				return "", filepath.Join(dir, "missing", "out.html"), dir
			},
			wantMsg: "creating temporary file in ",
		},
		{
			name: "destination directory is not writable",
			setup: func(t *testing.T) (string, string, string) {
				dir := filepath.Join(t.TempDir(), "locked")
				if err := os.Mkdir(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o700) })
				return "", filepath.Join(dir, "out.html"), dir
			},
			wantMsg: "creating temporary file in ",
			root:    true,
		},
		{
			name: "source cannot be read",
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				src := filepath.Join(dir, "srcdir")
				if err := os.Mkdir(src, 0o700); err != nil {
					t.Fatal(err)
				}
				return src, filepath.Join(dir, "out.html"), dir
			},
			wantMsg: "writing ",
		},
		{
			name: "destination is a directory",
			setup: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				dst := filepath.Join(dir, "out.html")
				if err := os.Mkdir(dst, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dst, "keep"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "", dst, dir
			},
			wantMsg: "replacing ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.root && os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions, so this failure cannot be produced")
			}
			src, dst, dir := tc.setup(t)
			var err error
			if src == "" {
				err = WriteFile(dst, []byte("data"), 0o600)
			} else {
				err = CopyFile(src, dst, 0o600)
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.HasPrefix(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want prefix %q", err, tc.wantMsg)
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp*"))
			if len(leftovers) != 0 {
				t.Errorf("failed write left %v behind", leftovers)
			}
		})
	}
}

func TestWriteFileAppliesTheRequestedMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640, 0o400} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "out.json")
			if err := WriteFile(path, []byte("{}"), mode); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != mode {
				t.Errorf("mode = %04o, want %04o", got, mode)
			}
		})
	}
}
