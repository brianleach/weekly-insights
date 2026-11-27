package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/brianleach/weekly-insights/internal/coach"
	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/safeio"
	"github.com/brianleach/weekly-insights/internal/snapshot"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/window"
)

// cmdCoach prints ranked proposals for the global CLAUDE.md and allow list,
// and writes them to the reports directory. It only reads the Claude config
// directory.
func cmdCoach(args []string) error {
	fs := flag.NewFlagSet("coach", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	asJSON := fs.Bool("json", false, "print the proposals as JSON")
	claudeDir := fs.String("claude-dir", "", "Claude config directory to read CLAUDE.md and settings.json from (default $CLAUDE_CONFIG_DIR or ~/.claude)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, o, err := cf.resolve()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}
	fails := failures.Collect(p, r.Substantive)
	res, err := coachRun(p, o, r, fails, nil, resolveClaudeDir(*claudeDir, home))
	if err != nil {
		return err
	}
	path, err := writeProposals(filepath.Join(home, "claude-weekly-insights"), o.Days, r.End.Format("2006-01-02"), res)
	if err != nil {
		return err
	}
	if *asJSON {
		err = writeJSONTo(os.Stdout, res)
	} else {
		fmt.Print(coach.Text(res, fmt.Sprintf("coach  %s .. %s  (%dd)",
			r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"), o.Days)))
	}
	fmt.Fprintf(os.Stderr, "proposals saved: %s\n", path)
	return err
}

// resolveClaudeDir is the flag, else CLAUDE_CONFIG_DIR, else ~/.claude.
func resolveClaudeDir(flagValue, home string) string {
	if flagValue != "" {
		return flagValue
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home, ".claude")
}

// coachRun gathers the rule inputs. cur is the window's snapshot when the
// caller already has it; otherwise the saved one is used, or one is built in
// memory and not saved.
func coachRun(p store.Paths, o window.Options, r window.Result, fails []failures.Failure,
	cur *snapshot.Snapshot, claudeDir string) (coach.Result, error) {
	snaps, err := snapshot.List(p)
	if err != nil {
		return coach.Result{}, err
	}
	label := r.End.Format("2006-01-02")
	if cur == nil {
		for i := range snaps {
			if snaps[i].Window.Label == label && snaps[i].Window.Days == o.Days {
				cur = &snaps[i]
			}
		}
	}
	if cur == nil {
		s := snapshot.Build(r, o.Days, nil)
		s.Failures = failures.SnapshotSummary(failures.Summarize(fails, len(r.Substantive)))
		cur = &s
	}
	in := coach.Input{Current: *cur, Previous: snaps, Failures: fails}
	md, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return coach.Result{}, err
	}
	in.ClaudeMD = string(md)
	b, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return coach.Result{}, err
	}
	var notes []string
	if len(b) > 0 {
		var settings struct {
			Permissions struct {
				Allow []string `json:"allow"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(b, &settings); err != nil {
			notes = append(notes, "settings.json is not valid JSON, so the allow list was read as empty: "+err.Error())
		}
		in.Allow = settings.Permissions.Allow
	}
	res := coach.Propose(in)
	res.Notes = append(res.Notes, notes...)
	return res, nil
}

// writeProposals writes <dir>/coach/proposals-<days>d-<end>.json owner-only.
func writeProposals(dir string, days int, end string, res coach.Result) (string, error) {
	out := filepath.Join(dir, "coach")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", out, err)
	}
	j, err := marshalIndent(res)
	if err != nil {
		return "", err
	}
	path := filepath.Join(out, fmt.Sprintf("proposals-%dd-%s.json", days, end))
	return path, safeio.WriteFile(path, append(j, '\n'), 0o600)
}
