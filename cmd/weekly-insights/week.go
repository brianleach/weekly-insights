package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/brianleach/weekly-insights/internal/auth"
	"github.com/brianleach/weekly-insights/internal/coach"
	"github.com/brianleach/weekly-insights/internal/extract"
	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/progress"
	"github.com/brianleach/weekly-insights/internal/report"
	"github.com/brianleach/weekly-insights/internal/runner"
	"github.com/brianleach/weekly-insights/internal/safeio"
	"github.com/brianleach/weekly-insights/internal/snapshot"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/validate"
	"github.com/brianleach/weekly-insights/internal/window"
)

// weekStep is one line of bundle.json: what a step did and why.
type weekStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, skipped or failed
	Reason string `json:"reason"`
	File   string `json:"file,omitempty"`
}

// weekBundle is bundle.json, the machine-readable half of the bundle.
type weekBundle struct {
	Tool    string     `json:"tool"`
	Version string     `json:"version"`
	Window  weekWindow `json:"window"`
	Steps   []weekStep `json:"steps"`
	Files   []string   `json:"files"`
}

type weekWindow struct {
	Days  int    `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// cmdWeek runs every step of a week in order and writes one owner-only
// bundle directory. A failed step is recorded, not fatal: the steps that do
// not depend on it still run, and the command exits nonzero at the end.
func cmdWeek(args []string) error {
	fs := flag.NewFlagSet("week", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	outDir := fs.String("out", "", "directory for the reports and the bundle (default ~/claude-weekly-insights)")
	skipInsights := fs.Bool("skip-insights", false, "do not run the builtin /insights report")
	skipExtract := fs.Bool("skip-extract", false, "do not extract facets through claude -p")
	withProgress := fs.Bool("progress", false, "add the progress memo (sends the global CLAUDE.md to the model)")
	openIt := fs.Bool("open", false, "open the bundle's index when done")
	claudeBin := fs.String("claude", "claude", "path to the Claude Code binary")
	limit := fs.Int("limit", extract.DefaultLimit, "extract: sessions this run (--all lifts the cap)")
	all := fs.Bool("all", false, "extract: every session that needs facets")
	conc := fs.Int("concurrency", extract.DefaultConcurrency, "extract: parallel model calls")
	timeout := fs.Duration("timeout", extract.DefaultTimeout, "extract: limit for one session's model call")
	budget := fs.Duration("budget", extract.DefaultBudget, "extract: total wall-clock limit")
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
	if *outDir == "" {
		*outDir = filepath.Join(home, "claude-weekly-insights")
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	m := weekBundle{Tool: "weekly-insights", Version: resolvedVersion(), Window: weekWindow{
		Days: o.Days, Start: r.Start.Format("2006-01-02"), End: r.End.Format("2006-01-02")}}
	files := map[string][]byte{}
	notLoggedIn := false
	step := func(name, file string, err error, reason string) {
		s := weekStep{Name: name, Status: "ok", Reason: reason, File: file}
		switch {
		case err != nil:
			first, _, _ := strings.Cut(err.Error(), "\n")
			s.Status, s.File = "failed", ""
			s.Reason = strings.TrimPrefix(reason+": "+first, ": ")
			notLoggedIn = notLoggedIn || errors.Is(err, runner.ErrNotLoggedIn)
			fmt.Fprintf(os.Stderr, "week: %s failed: %s\n", name, s.Reason)
		case file == "" && strings.HasPrefix(reason, "skipped: "):
			s.Status, s.Reason = "skipped", strings.TrimPrefix(reason, "skipped: ")
		}
		m.Steps = append(m.Steps, s)
	}
	// keep reads a finished file into the bundle under its own name.
	keep := func(path, as string) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		files[as] = b
		return as, nil
	}

	if *skipInsights {
		step("insights", "", nil, "skipped: --skip-insights")
	} else {
		var name string
		path, err := runInsights(ctx, p, o, insightsOpts{outDir: *outDir, claudeBin: *claudeBin})
		if err == nil {
			name, err = keep(path, filepath.Base(path))
		}
		step("insights", name, err, "builtin /insights over the window")
	}

	extractFailed := false
	if *skipExtract {
		step("extract", "", nil, "skipped: --skip-extract")
	} else {
		reason, err := weekExtract(ctx, p, o, extract.Options{ClaudeBin: *claudeBin, Limit: *limit,
			All: *all, Concurrency: *conc, PerSessionTimeout: *timeout, Budget: *budget})
		extractFailed = err != nil
		step("extract", "", err, reason)
	}

	if res, err := validate.Dir(p.WeeklyFacets(), true); err != nil {
		step("validate", "", err, "validate --fix")
	} else if len(res.Unresolved) > 0 {
		step("validate", "", fmt.Errorf("%d problems remain after --fix", len(res.Unresolved)), "validate --fix")
	} else {
		step("validate", "", nil, fmt.Sprintf("%d facet files, %d fixed", res.Checked, len(res.Fixed)))
	}

	// Aggregate reads the window again: insights harvests and extract writes
	// facets, and both should count.
	snap, fails, err := weekAggregate(p, o)
	if err != nil {
		step("aggregate", "", err, "aggregate")
		step("weekly", "", nil, "skipped: aggregate failed")
		step("trend", "", nil, "skipped: aggregate failed")
		step("failures", "", nil, "skipped: aggregate failed")
		step("coach", "", nil, "skipped: aggregate failed")
	} else {
		sum := failures.Summarize(fails, snap.Sessions.Substantive)
		reason := fmt.Sprintf("snapshot %s saved, %d sessions", snap.Window.Label, snap.Sessions.Substantive)
		if extractFailed {
			reason += "; extract failed, so only the facets already on disk count"
		}
		step("aggregate", "", nil, reason)
		snaps, err := snapshot.List(p)
		if err == nil {
			files["weekly.html"] = []byte(report.WeeklyHTML(snap, previousSnapshot(snaps, snap)))
			files["trend.html"] = []byte(report.TrendHTML(snaps))
			step("weekly", "weekly.html", nil, "this week against the previous snapshot")
			step("trend", "trend.html", nil, fmt.Sprintf("%d snapshots", len(snaps)))
		} else {
			step("weekly", "", err, "listing snapshots")
			step("trend", "", err, "listing snapshots")
		}
		files["failures.txt"] = []byte(failures.Report(sum,
			fmt.Sprintf("%s .. %s  (%dd)", m.Window.Start, m.Window.End, o.Days)))
		step("failures", "failures.txt", nil, fmt.Sprintf("%d failed tool calls", sum.Total))
		var name string
		res, err := weekCoach(p, o, &snap, fails, *outDir, home, m.Window.End)
		if err == nil {
			name = "coach.html"
			files[name] = []byte(coach.HTML(res, fmt.Sprintf("Coach %s .. %s (%dd)", m.Window.Start, m.Window.End, o.Days)))
		}
		step("coach", name, err, fmt.Sprintf("%d proposals", len(res.Proposals)))
	}

	if !*withProgress {
		step("progress", "", nil, "skipped: off by default; --progress sends the global CLAUDE.md to the model")
	} else if found, _ := progress.Discover(*outDir); len(found) < 2 {
		step("progress", "", nil, fmt.Sprintf("skipped: needs two weekly reports in %s, found %d", *outDir, len(found)))
	} else {
		var name string
		in, err := progressInput(*outDir, 4, cf.root, home)
		if err == nil {
			var path string
			if path, err = writeProgressMemo(in, *outDir, *claudeBin); err == nil {
				name, err = keep(path, "progress.html")
			}
		}
		step("progress", name, err, "progress memo")
	}

	if notLoggedIn {
		fmt.Fprintln(os.Stderr, withSetupHelp(runner.ErrNotLoggedIn))
	}
	index, err := writeWeekBundle(*outDir, fmt.Sprintf("week-%dd-%s", o.Days, m.Window.End), m, files)
	if err != nil {
		return err
	}
	fmt.Println(index)
	if *openIt {
		openPath(index)
	}
	var failed []string
	for _, s := range m.Steps {
		if s.Status == "failed" {
			failed = append(failed, s.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d step(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// weekExtract is the extract command's run with its bounds, returning a
// one-line summary.
func weekExtract(ctx context.Context, p store.Paths, o window.Options, opts extract.Options) (string, error) {
	r, err := window.Select(p, o)
	if err != nil {
		return "", err
	}
	var sessions []model.Session
	for _, s := range r.Substantive {
		if window.NeedsExtraction(s) {
			sessions = append(sessions, s)
		}
	}
	if len(sessions) == 0 {
		return "nothing to extract", nil
	}
	opts.Paths, opts.Sessions, opts.Stderr = p, sessions, os.Stderr
	opts.Token, _ = auth.Resolve()
	res, err := extract.Run(ctx, opts)
	return fmt.Sprintf("extracted %d, rejected %d, failed %d, unfinished %d",
		res.Extracted, len(res.Rejected), res.Failed, res.Unfinished), err
}

// weekAggregate is aggregate without the printing: build, attach failures,
// save. It returns the window's failures for the steps after it.
func weekAggregate(p store.Paths, o window.Options) (snapshot.Snapshot, []failures.Failure, error) {
	r, err := window.Select(p, o)
	if err != nil {
		return snapshot.Snapshot{}, nil, err
	}
	snap := snapshot.Build(r, o.Days, nil)
	fails := failures.Collect(p, r.Substantive)
	snap.Failures = failures.SnapshotSummary(failures.Summarize(fails, len(r.Substantive)))
	_, err = snapshot.Save(p, snap)
	return snap, fails, err
}

// weekCoach runs the coach over the snapshot aggregate just saved and writes
// its proposals file beside the reports.
func weekCoach(p store.Paths, o window.Options, snap *snapshot.Snapshot, fails []failures.Failure,
	outDir, home, end string) (coach.Result, error) {
	r, err := window.Select(p, o)
	if err != nil {
		return coach.Result{}, err
	}
	res, err := coachRun(p, o, r, fails, snap, resolveClaudeDir("", home))
	if err != nil {
		return res, err
	}
	_, err = writeProposals(outDir, o.Days, end, res)
	return res, err
}

// previousSnapshot is the newest snapshot of the same window length before
// cur, or nil.
func previousSnapshot(snaps []snapshot.Snapshot, cur snapshot.Snapshot) *snapshot.Snapshot {
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Window.Days == cur.Window.Days && snaps[i].Window.Label < cur.Window.Label {
			return &snaps[i]
		}
	}
	return nil
}

// writeWeekBundle builds the bundle in a sibling temporary directory and
// renames it into place, so a reader never sees a half-written week. It
// returns the index path.
func writeWeekBundle(out, name string, m weekBundle, files map[string][]byte) (string, error) {
	for f := range files {
		m.Files = append(m.Files, f)
	}
	m.Files = append(m.Files, "bundle.json", "index.html")
	sort.Strings(m.Files)
	j, err := marshalIndent(m)
	if err != nil {
		return "", err
	}
	files["bundle.json"] = append(j, '\n')
	files["index.html"] = []byte(weekIndex(m))

	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", out, err)
	}
	// MkdirTemp creates the directory 0700, so the bundle is owner-only from
	// the start.
	tmp, err := os.MkdirTemp(out, "."+name+".tmp-")
	if err != nil {
		return "", fmt.Errorf("creating bundle: %w", err)
	}
	defer os.RemoveAll(tmp) // a no-op once renamed
	for f, b := range files {
		if err := safeio.WriteFile(filepath.Join(tmp, f), b, 0o600); err != nil {
			return "", err
		}
	}
	final := filepath.Join(out, name)
	// A directory cannot be renamed over a non-empty one, so the old bundle
	// moves aside first and comes back if the new one cannot take its place.
	old := ""
	if _, err := os.Lstat(final); err == nil {
		old = tmp + "-old"
		if err := os.Rename(final, old); err != nil {
			return "", fmt.Errorf("replacing %s: %w", final, err)
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		if old != "" {
			_ = os.Rename(old, final)
		}
		return "", fmt.Errorf("replacing %s: %w", final, err)
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove the previous bundle at %s: %v\n", old, err)
		}
	}
	return filepath.Join(final, "index.html"), nil
}

// weekIndex is the bundle's landing page: each step, its status, and a link
// to what it produced.
func weekIndex(m weekBundle) string {
	var b strings.Builder
	title := fmt.Sprintf("Week %s .. %s (%dd)", m.Window.Start, m.Window.End, m.Window.Days)
	fmt.Fprintf(&b, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}
td{padding:.3rem .8rem .3rem 0;vertical-align:top}.failed{color:#b42318}.skipped{color:#667085}</style>
</head><body><h1>%s</h1><table>
`, html.EscapeString(title), html.EscapeString(title))
	for _, s := range m.Steps {
		name := html.EscapeString(s.Name)
		if s.File != "" {
			name = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(s.File), name)
		}
		fmt.Fprintf(&b, "<tr><td>%s</td><td class=%q>%s</td><td>%s</td></tr>\n",
			name, s.Status, s.Status, html.EscapeString(s.Reason))
	}
	fmt.Fprintf(&b, "</table><p><a href=\"bundle.json\">bundle.json</a> &middot; %s %s</p></body></html>\n",
		html.EscapeString(m.Tool), html.EscapeString(m.Version))
	return b.String()
}
