// Package store locates and loads the on-disk caches.
//
// It reads two caches maintained by Claude Code's builtin /insights and owns
// two of its own. Nothing here writes to the builtin's directories: a corrupt
// or differently-shaped file there would degrade the builtin report, and this
// tool is meant to sit alongside it rather than replace it.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brianleach/weekly-insights/internal/model"
)

// FacetSource distinguishes facets we extracted from facets the builtin did.
const (
	SourceCanonical = "canonical" // ours, fixed vocabulary
	SourceBuiltin   = "builtin"   // builtin's, free-form, normalized on read
)

// Paths resolves every directory the tool touches. Root defaults to
// ~/.claude/usage-data and is overridable so tests never read real user data.
type Paths struct {
	// Root is the usage-data directory, normally ~/.claude/usage-data.
	Root string
	// ClaudeHome is the ~/.claude directory. Transcripts live here, outside
	// usage-data, so it is tracked separately rather than derived by walking
	// parent directories.
	ClaudeHome string
}

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("locating home directory: %w", err)
	}
	ch := filepath.Join(home, ".claude")
	return Paths{Root: filepath.Join(ch, "usage-data"), ClaudeHome: ch}, nil
}

func (p Paths) SessionMeta() string   { return filepath.Join(p.Root, "session-meta") }
func (p Paths) BuiltinFacets() string { return filepath.Join(p.Root, "facets") }
func (p Paths) WeeklyFacets() string  { return filepath.Join(p.Root, "weekly-facets") }
func (p Paths) Snapshots() string     { return filepath.Join(p.Root, "weekly") }

// Transcripts is the projects tree holding raw session .jsonl files.
func (p Paths) Transcripts() string { return filepath.Join(p.ClaudeHome, "projects") }

// Config controls which sessions count as the user's own work.
type Config struct {
	// ExcludeProjectGlobs drops sessions whose project path matches. Agent
	// scratchpads and eval-harness runs are not work the user did, and on a
	// real corpus they outnumbered genuine sessions five to one.
	ExcludeProjectGlobs []string `json:"exclude_project_globs"`
}

func DefaultConfig() Config {
	return Config{ExcludeProjectGlobs: []string{
		"/private/tmp/*",
		"/tmp/*",
		"*/scratchpad/*",
		"*/.claude/worktrees/*",
	}}
}

// LoadConfig reads a config file, falling back to defaults when absent.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return DefaultConfig(), nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return c, nil
}

// Excluded reports whether a project path is filtered out by the config.
func (c Config) Excluded(projectPath string) bool {
	for _, g := range c.ExcludeProjectGlobs {
		if matchGlob(g, projectPath) {
			return true
		}
	}
	return false
}

// matchGlob supports the leading/trailing "*" forms used in config, which
// filepath.Match cannot express because it refuses to match across separators.
func matchGlob(pattern, s string) bool {
	if pattern == "" {
		return false
	}
	star := strings.Count(pattern, "*")
	switch {
	case star == 0:
		return pattern == s
	case strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") && star >= 2:
		return strings.Contains(s, strings.Trim(pattern, "*"))
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(s, strings.TrimPrefix(pattern, "*"))
	default:
		ok, err := filepath.Match(pattern, s)
		return err == nil && ok
	}
}

// LoadAllMeta reads every session-meta record. Unparseable files are skipped
// rather than fatal: the cache is written concurrently by the running CLI and
// a half-written file should not break a report.
func (p Paths) LoadAllMeta() ([]model.SessionMeta, error) {
	matches, err := filepath.Glob(filepath.Join(p.SessionMeta(), "*.json"))
	if err != nil {
		return nil, fmt.Errorf("globbing session metadata: %w", err)
	}
	out := make([]model.SessionMeta, 0, len(matches))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var sm model.SessionMeta
		if err := json.Unmarshal(b, &sm); err != nil || sm.SessionID == "" {
			continue
		}
		out = append(out, sm)
	}
	return out, nil
}

// LoadFacets returns the facets for a session, preferring our own canonical
// extraction over the builtin's free-form one. Returns nil when neither exists.
func (p Paths) LoadFacets(sessionID string) (*model.Facets, string) {
	for _, c := range []struct{ dir, src string }{
		{p.WeeklyFacets(), SourceCanonical},
		{p.BuiltinFacets(), SourceBuiltin},
	} {
		b, err := os.ReadFile(filepath.Join(c.dir, sessionID+".json"))
		if err != nil {
			continue
		}
		var f model.Facets
		if err := json.Unmarshal(b, &f); err != nil {
			continue
		}
		return &f, c.src
	}
	return nil, ""
}

// TranscriptPath finds a session's raw transcript, which lives under a
// per-project directory whose name encodes the project path.
func (p Paths) TranscriptPath(sessionID string) string {
	matches, err := filepath.Glob(filepath.Join(p.Transcripts(), "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// WriteJSON writes v to path, creating parent directories as needed.
//
// Permissions are owner-only. Snapshots carry verbatim user corrections and
// friction detail quoted out of transcripts, which is the user's own writing
// about their own work and has no business being world-readable on a shared
// machine. MkdirAll only applies its mode to directories it creates, so a
// directory that already exists keeps whatever mode it had.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	// os.WriteFile applies its mode only when it creates the file, so a
	// snapshot written by an earlier version stays 0644 forever unless it is
	// chmodded here.
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("tightening permissions on %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
