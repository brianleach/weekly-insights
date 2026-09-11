// Package stage builds a temporary Claude config directory that contains only
// the sessions inside a time window.
//
// The builtin /insights takes no arguments and scans everything under the
// config directory. It does, however, honor CLAUDE_CONFIG_DIR. So the way to
// get the real report for one week is to hand it a config directory where
// "everything" is exactly that week: transcripts and caches for the window's
// sessions, and symlinks to the real directory for everything else (settings,
// plugins, commands) so the child behaves like the user's normal install.
package stage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// Counts reports what was staged so a caller can sanity-check coverage
// before spending a model run on it.
type Counts struct {
	Sessions    int
	Transcripts int
	Meta        int
	Facets      int
}

// Inputs names the real locations to stage from.
type Inputs struct {
	Paths store.Paths
	// AccountFile is the file holding login state, normally ~/.claude.json.
	// It sits beside the config directory, not inside it, and the child reads
	// it from CLAUDE_CONFIG_DIR, so it has to be brought along explicitly.
	AccountFile string
}

// skip lists the config-dir entries the stage replaces rather than shares.
var skip = map[string]bool{"projects": true, "usage-data": true, ".claude.json": true}

// Build populates dst, which must be empty or absent.
func Build(in Inputs, sessions []model.Session, dst string) (Counts, error) {
	var c Counts
	if err := ensureEmptyDir(dst); err != nil {
		return c, err
	}
	if err := shareConfig(in.Paths.ClaudeHome, dst); err != nil {
		return c, err
	}
	// Copied, not linked: the child updates this file on exit, and a symlink
	// would let it race the live session that owns the real one.
	if err := copyFile(in.AccountFile, filepath.Join(dst, ".claude.json"), 0o600); err != nil && !os.IsNotExist(err) {
		return c, fmt.Errorf("staging account file: %w", err)
	}

	sp := store.Paths{Root: filepath.Join(dst, "usage-data"), ClaudeHome: dst}
	for _, d := range []string{sp.Transcripts(), sp.SessionMeta(), sp.BuiltinFacets()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return c, fmt.Errorf("creating %s: %w", d, err)
		}
	}

	for _, s := range sessions {
		c.Sessions++
		id := s.Meta.SessionID
		if err := stageTranscript(in.Paths, sp, id, &c); err != nil {
			return c, err
		}
		src := filepath.Join(in.Paths.SessionMeta(), id+".json")
		if err := copyFile(src, filepath.Join(sp.SessionMeta(), id+".json"), 0o644); err == nil {
			c.Meta++
		} else if !os.IsNotExist(err) {
			return c, fmt.Errorf("staging session meta for %s: %w", id, err)
		}
		// Our canonical facets win over the builtin's free-form ones. Seeding
		// the cache also means the child skips extraction entirely, so the
		// run costs only the narrative passes.
		for _, dir := range []string{in.Paths.WeeklyFacets(), in.Paths.BuiltinFacets()} {
			err := copyFile(filepath.Join(dir, id+".json"), filepath.Join(sp.BuiltinFacets(), id+".json"), 0o644)
			if err == nil {
				c.Facets++
				break
			}
			if !os.IsNotExist(err) {
				return c, fmt.Errorf("staging facets for %s: %w", id, err)
			}
		}
	}
	return c, nil
}

// stageTranscript copies the session's .jsonl under the same project directory
// name the real tree uses, since that name encodes the project path the
// builtin reports on.
//
// Copied, not symlinked: the builtin enumerates transcripts with a directory
// listing that treats symlinks as neither files nor directories, so a linked
// transcript is silently skipped and the report comes back empty.
func stageTranscript(real, sp store.Paths, id string, c *Counts) error {
	tp := real.TranscriptPath(id)
	if tp == "" {
		return nil
	}
	projDir := filepath.Join(sp.Transcripts(), filepath.Base(filepath.Dir(tp)))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", projDir, err)
	}
	if err := copyFile(tp, filepath.Join(projDir, id+".jsonl"), 0o644); err != nil {
		return fmt.Errorf("copying transcript %s: %w", id, err)
	}
	c.Transcripts++
	// Per-session sidecar directories (titles and similar) ride along when present.
	if side := filepath.Join(filepath.Dir(tp), id); isDir(side) {
		if err := copyDir(side, filepath.Join(projDir, id)); err != nil {
			return fmt.Errorf("copying session dir %s: %w", id, err)
		}
	}
	return nil
}

// copyDir copies a small flat-or-nested directory tree of regular files.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target, 0o644)
	})
}

// shareConfig symlinks every top-level entry of the real config dir except the
// ones the stage owns, so settings, plugins, hooks and commands all resolve.
func shareConfig(home, dst string) error {
	entries, err := os.ReadDir(home)
	if err != nil {
		return fmt.Errorf("reading %s: %w", home, err)
	}
	for _, e := range entries {
		if skip[e.Name()] {
			continue
		}
		if err := os.Symlink(filepath.Join(home, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("linking %s: %w", e.Name(), err)
		}
	}
	return nil
}

func ensureEmptyDir(dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("creating stage %s: %w", dst, err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("reading stage %s: %w", dst, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("stage directory %s is not empty", dst)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
