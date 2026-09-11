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
	"runtime"
	"sort"
	"strings"

	"github.com/brianleach/weekly-insights/internal/store"
)

// KeychainService is the macOS keychain item the runner reads a token from
// when CLAUDE_CODE_OAUTH_TOKEN is not already set.
const KeychainService = "weekly-insights"

// ErrNotLoggedIn is returned when the child could not authenticate.
var ErrNotLoggedIn = errors.New("the staged Claude Code run was not logged in")

// Options configures one run.
type Options struct {
	Real      store.Paths // the user's real config, for harvesting caches back
	Stage     string      // populated by package stage
	ClaudeBin string      // defaults to "claude"
	Token     string      // optional; see ResolveToken
	OutPath   string      // where to copy the finished report
	Stderr    io.Writer   // child stderr passthrough; nil discards
}

// Result describes what the run left behind.
type Result struct {
	Report          string // path of the copied report
	HarvestedMeta   int    // session-meta files new to the real cache
	HarvestedFacets int    // facet files new to the real cache
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
	if err := os.MkdirAll(filepath.Dir(o.OutPath), 0o755); err != nil {
		return r, fmt.Errorf("creating output directory: %w", err)
	}
	if err := copyFile(src, o.OutPath, 0o644); err != nil {
		return r, fmt.Errorf("copying report: %w", err)
	}
	r.Report = o.OutPath

	// The child computes session-meta for every transcript it scans and
	// facets for any it lacked. Both are worth keeping: they are exactly what
	// the real cache would hold after a normal /insights run, and harvesting
	// them removes the need to run the builtin cumulatively just to refresh it.
	// Existing files are never overwritten.
	r.HarvestedMeta = harvest(sp.SessionMeta(), o.Real.SessionMeta())
	r.HarvestedFacets = harvest(sp.BuiltinFacets(), o.Real.BuiltinFacets())
	return r, nil
}

// childEnv strips the markers that make Claude Code refuse to nest, points the
// child at the stage, and keeps secure storage on the real config dir so the
// keychain entry resolves where the platform supports that.
func childEnv(o Options) []string {
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
	env = append(env,
		"CLAUDE_CONFIG_DIR="+o.Stage,
		"CLAUDE_SECURESTORAGE_CONFIG_DIR="+o.Real.ClaudeHome,
	)
	if o.Token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+o.Token)
	}
	return env
}

// ResolveToken finds a long-lived token for the child: the environment first,
// then the macOS keychain item named KeychainService. The token is returned to
// the caller only to be placed in the child's environment; it is never logged.
func ResolveToken() string {
	if t := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); t != "" {
		return t
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", KeychainService, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func newestReport(usageData string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(usageData, "report-*.html"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("the child run produced no report under %s", usageData)
	}
	sort.Strings(matches) // names embed a timestamp, so lexical order is chronological
	return matches[len(matches)-1], nil
}

func harvest(from, to string) int {
	matches, _ := filepath.Glob(filepath.Join(from, "*.json"))
	n := 0
	for _, m := range matches {
		dst := filepath.Join(to, filepath.Base(m))
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(to, 0o755); err != nil {
			continue
		}
		if copyFile(m, dst, 0o644) == nil {
			n++
		}
	}
	return n
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
