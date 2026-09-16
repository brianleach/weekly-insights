// Package safeio writes files that hold the user's own session data.
//
// Every page and cache this tool produces quotes prompts and project names, so
// none of it should ever be readable by another account, even briefly. Writing
// in place cannot promise that: os.WriteFile applies its mode only when it
// creates the file, so replacing a world-readable file leaves the new contents
// exposed until a following chmod, and a reader that already holds the
// descriptor keeps it afterwards. Writing through a path also follows a
// symlink, so a destination someone else can create is a destination that can
// point anywhere.
//
// So everything here writes a fresh owner-only temporary file beside the
// destination and renames it over the top. The rename replaces the name
// atomically, including when that name was a symlink, and the contents are
// never visible under looser permissions than the ones asked for.
package safeio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFile replaces path with b at the given mode.
func WriteFile(path string, b []byte, mode os.FileMode) error {
	return write(path, mode, func(w io.Writer) error {
		_, err := w.Write(b)
		return err
	})
}

// CopyFile replaces dst with the contents of src at the given mode.
func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return write(dst, mode, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

func write(path string, mode os.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	// os.CreateTemp creates with 0600 and a name nothing else can predict, so
	// the contents are owner-only from the first byte.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(name)
		}
	}()
	// CreateTemp's own mode is already owner-only; this is what applies a
	// caller's choice, and it happens before anything is written.
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", name, err)
	}
	if err := fill(tmp); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	done = true
	return nil
}
