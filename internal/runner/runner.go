// Package runner executes the builtin /insights against a staged config
// directory and collects what it produced.
//
// Nothing here reimplements insights. The child process is the user's own
// Claude Code, running its own command, so the report is the one they already
// know, restricted to the staged window.
package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brianleach/weekly-insights/internal/store"
)

// ErrNotLoggedIn is returned when the child could not authenticate.
var ErrNotLoggedIn = errors.New("the staged Claude Code run was not logged in")

// Options configures one run.
type Options struct {
	Real      store.Paths // the user's real config, for harvesting caches back
	Stage     string      // populated by package stage
	ClaudeBin string      // defaults to "claude"
	Token     string      // optional; resolved by package auth
	OutPath   string      // where to copy the finished report
	Stderr    io.Writer   // child stderr passthrough; nil discards
}

// Result describes what the run left behind.
type Result struct {
	Report          string // path of the copied report
	HarvestedMeta   int    // session-meta files new to the real cache
	HarvestedFacets int    // facet files new to the real cache
	HarvestErrors   int    // harvest copies that failed, for the caller to report
}

// Run executes `claude -p /insights` in the stage and copies the report out.
func Run(o Options) (Result, error) {
	var r Result
	bin := o.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.Command(bin, "-p", "/insights")
	cmd.Dir = os.TempDir()
	cmd.Env = childEnv(o)
	var out bytes.Buffer
	cmd.Stdout = &out
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	err := cmd.Run()
	if strings.Contains(out.String(), "Not logged in") {
		return r, ErrNotLoggedIn
	}
	if err != nil {
		return r, fmt.Errorf("running %s -p /insights: %w\n%s", bin, err, strings.TrimSpace(out.String()))
	}

	sp := store.Paths{Root: filepath.Join(o.Stage, "usage-data"), ClaudeHome: o.Stage}
	src, err := newestReport(sp.Root)
	if err != nil {
		return r, err
	}
	// The report carries prompts and project names, so the directory is owner
	// only and the file is owner read/write.
	if err := os.MkdirAll(filepath.Dir(o.OutPath), 0o700); err != nil {
		return r, fmt.Errorf("creating output directory: %w", err)
	}
	if err := copyFile(src, o.OutPath, 0o600); err != nil {
		return r, fmt.Errorf("copying report: %w", err)
	}
	r.Report = o.OutPath

	// The child computes session-meta for every transcript it scans and
	// facets for any it lacked. Both are worth keeping: they are exactly what
	// the real cache would hold after a normal /insights run, and harvesting
	// them removes the need to run the builtin cumulatively just to refresh it.
	// Existing files are never overwritten.
	var metaFailed, facetFailed int
	r.HarvestedMeta, metaFailed = harvest(sp.SessionMeta(), o.Real.SessionMeta())
	r.HarvestedFacets, facetFailed = harvest(sp.BuiltinFacets(), o.Real.BuiltinFacets())
	r.HarvestErrors = metaFailed + facetFailed
	return r, nil
}

// BaseEnv is the parent environment minus everything that marks this process
// as a running Claude Code session. A child started with those markers refuses
// to run, so any nested `claude -p` must start from this.
//
// CLAUDE_CODE_OAUTH_TOKEN is stripped along with them, so a token that happens
// to sit in the parent environment never leaks into a child by accident. A
// caller that wants the child authenticated injects the resolved token itself.
func BaseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_PID", k == "CLAUDE_CONFIG_DIR",
			k == "CLAUDE_SECURESTORAGE_CONFIG_DIR", k == "CLAUDE_CODE_OAUTH_TOKEN",
			strings.HasPrefix(k, "CLAUDE_CODE_"):
			continue
		}
		env = append(env, kv)
	}
	return env
}

// childEnv points the child at the stage and keeps secure storage on the real
// config dir so the keychain entry resolves where the platform supports that.
func childEnv(o Options) []string {
	env := BaseEnv()
	env = append(env,
		"CLAUDE_CONFIG_DIR="+o.Stage,
		"CLAUDE_SECURESTORAGE_CONFIG_DIR="+o.Real.ClaudeHome,
	)
	if o.Token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+o.Token)
	}
	return env
}

func newestReport(usageData string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(usageData, "report-*.html"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("the child run produced no report under %s", usageData)
	}
	sort.Strings(matches) // names embed a timestamp, so lexical order is chronological
	return matches[len(matches)-1], nil
}

// harvest copies new cache files into the real cache and never overwrites one.
// Exclusive creation is what makes that true: a Stat-then-truncate pair would
// still clobber a file written between the two calls. It returns how many were
// copied and how many failed for a reason other than "already there", and logs
// nothing itself so the caller decides how to report.
func harvest(from, to string) (copied, failed int) {
	matches, _ := filepath.Glob(filepath.Join(from, "*.json"))
	for _, m := range matches {
		dst := filepath.Join(to, filepath.Base(m))
		if err := os.MkdirAll(to, 0o700); err != nil {
			failed++
			continue
		}
		switch err := copyNew(m, dst, 0o600); {
		case err == nil:
			copied++
		case errors.Is(err, os.ErrExist):
			// Already in the real cache; leaving it alone is the point.
		default:
			failed++
		}
	}
	return copied, failed
}

// copyNew copies src to dst only if dst does not exist, returning an error
// satisfying errors.Is(err, os.ErrExist) when it does.
func copyNew(src, dst string, mode os.FileMode) error {
	return copyInto(src, dst, mode, os.O_CREATE|os.O_EXCL|os.O_WRONLY, false)
}

// copyFile copies src over dst, creating or truncating it, and forces mode on
// a destination that already existed with looser permissions.
func copyFile(src, dst string, mode os.FileMode) error {
	return copyInto(src, dst, mode, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, true)
}

func copyInto(src, dst string, mode os.FileMode, flag int, chmod bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, flag, mode)
	if err != nil {
		return err
	}
	// O_CREATE applies mode only when it creates the file, so an existing
	// destination keeps whatever permissions it had until this chmod.
	if chmod {
		if err := out.Chmod(mode); err != nil {
			out.Close()
			return err
		}
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
