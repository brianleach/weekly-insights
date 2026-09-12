// Command weekly-insights runs Claude Code's builtin /insights over a time
// window, and tracks week-over-week trends alongside it.
//
// The builtin is cumulative over all history and takes no arguments, so a week
// of changed behavior is averaged against months of the old pattern. The
// insights subcommand stages a config directory holding only the window's
// sessions and runs the user's own Claude Code against it; the report is the
// builtin's, restricted to the window. The remaining subcommands are local
// only: they pin a label vocabulary so counts stay comparable and diff each
// week's snapshot against the previous one.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/auth"
	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/progress"
	"github.com/brianleach/weekly-insights/internal/prompt"
	"github.com/brianleach/weekly-insights/internal/report"
	"github.com/brianleach/weekly-insights/internal/runner"
	"github.com/brianleach/weekly-insights/internal/snapshot"
	"github.com/brianleach/weekly-insights/internal/stage"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/transcript"
	"github.com/brianleach/weekly-insights/internal/validate"
	"github.com/brianleach/weekly-insights/internal/window"
)

// version is overridden at build time via -ldflags by the Makefile. A plain
// `go install module@vX.Y.Z` never sets it, so resolvedVersion falls back to
// the module version Go embeds in the binary, which is what users of the
// documented install path will have.
var version = "dev"

func resolvedVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

const usage = `weekly-insights - time-windowed usage insights for Claude Code

Usage:
  weekly-insights [flags]              run the real Claude Code /insights over a time window
  weekly-insights <command> [flags]

Commands:
  progress    judge progression across the last N weekly reports (separate file)
  auth        store the token insights needs (--check to verify, --clear to remove)
  select      show what is in the window and how many sessions need facets
  failures    classify every failed tool call in the window (--json for the data)
  prepare     render the window's transcripts to text for facet extraction
  aggregate   build and save a snapshot of the window
  report      render the newest snapshot, diffed against the previous one
              (--html for a browsable page, --out DIR for one file per week)
  validate    enforce the canonical vocabulary on extracted facets
  prompt      print the facet-extraction prompt
  version     print the version

"weekly-insights run" and "weekly-insights insights" are the same as the default.
Run "weekly-insights <command> -h" for a command's flags; "weekly-insights run -h"
for the default's.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	// The weekly report is the default action, so `weekly-insights --days 7`
	// works without repeating the word. Any leading flag means "run the report";
	// `insights` and `run` remain as explicit spellings.
	if strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "-h", "--help", "-v", "--version":
		default:
			if err := cmdInsights(os.Args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}
	switch os.Args[1] {
	case "insights", "run":
		err = cmdInsights(os.Args[2:])
	case "auth":
		err = cmdAuth(os.Args[2:])
	case "progress":
		err = cmdProgress(os.Args[2:])
	case "select":
		err = cmdSelect(os.Args[2:])
	case "failures":
		err = cmdFailures(os.Args[2:])
	case "prepare":
		err = cmdPrepare(os.Args[2:])
	case "aggregate":
		err = cmdAggregate(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "prompt":
		fmt.Print(prompt.Extraction())
	case "version", "--version", "-v":
		fmt.Println(resolvedVersion())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// commonFlags are shared by every window-scoped command so that a window means
// the same thing no matter which command computed it.
type commonFlags struct {
	days           int
	end            string
	config         string
	root           string
	includeScratch bool
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.IntVar(&c.days, "days", window.DefaultDays, "window length in days")
	fs.StringVar(&c.end, "end", "", "window end as YYYY-MM-DD (default: now)")
	fs.StringVar(&c.config, "config", "", "path to a JSON config file")
	fs.StringVar(&c.root, "root", "", "override the usage-data directory (for testing)")
	fs.BoolVar(&c.includeScratch, "include-scratch", false,
		"do not apply the config's project exclusions")
}

func (c *commonFlags) resolve() (store.Paths, window.Options, error) {
	p, err := store.DefaultPaths()
	if err != nil {
		return p, window.Options{}, err
	}
	if c.root != "" {
		p.Root = c.root
	}
	cfg, err := store.LoadConfig(c.config)
	if err != nil {
		return p, window.Options{}, err
	}
	o := window.Options{Days: c.days, Config: cfg, IncludeScratch: c.includeScratch}
	if c.end != "" {
		// A bare date means "through the end of that day", not midnight, or a
		// window ending on a day you worked would silently drop that day.
		t, err := time.Parse("2006-01-02", c.end)
		if err != nil {
			return p, o, fmt.Errorf("parsing --end %q as YYYY-MM-DD: %w", c.end, err)
		}
		o.End = t.Add(24*time.Hour - time.Second).UTC()
	}
	return p, o, nil
}

// cmdInsights is the command the tool exists for: the builtin report, scoped
// to a window. Everything else in this binary is a supplement to it.
func cmdInsights(args []string) error {
	fs := flag.NewFlagSet("insights", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	outDir := fs.String("out", "", "directory for the report (default ~/claude-weekly-insights)")
	openIt := fs.Bool("open", false, "open the report when done")
	keep := fs.Bool("keep-stage", false, "leave the staged config directory in place for inspection")
	claudeBin := fs.String("claude", "claude", "path to the Claude Code binary")
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
	if len(r.Substantive) == 0 {
		return fmt.Errorf("no substantive sessions in the last %d days", o.Days)
	}

	st, err := os.MkdirTemp("", "weekly-insights-stage-")
	if err != nil {
		return fmt.Errorf("creating stage: %w", err)
	}
	if !*keep {
		// The stage holds a copy of the account file, so it is not left behind.
		defer os.RemoveAll(st)
	}
	counts, err := stage.Build(stage.Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, r.Substantive, st)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "staged %d sessions (%d transcripts, %d meta, %d facets) for %s .. %s\n",
		counts.Sessions, counts.Transcripts, counts.Meta, counts.Facets,
		r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"))
	if r.Derived > 0 {
		fmt.Fprintf(os.Stderr, "%d session(s) had no cached metadata; derived from transcripts\n", r.Derived)
	}
	if *keep {
		fmt.Fprintf(os.Stderr, "stage kept at %s\n", st)
	}

	name := fmt.Sprintf("insights-%dd-%s.html", o.Days, r.End.Format("2006-01-02"))
	fmt.Fprintln(os.Stderr, "running claude -p /insights against the stage; this takes a few minutes ...")
	token, _ := auth.Resolve()
	res, err := runner.Run(runner.Options{
		Real: p, Stage: st, ClaudeBin: *claudeBin, Token: token,
		OutPath: filepath.Join(*outDir, name), Stderr: os.Stderr,
	})
	if err == runner.ErrNotLoggedIn {
		return fmt.Errorf(`%w

A staged config directory cannot see the login stored for the real one, so the
child needs a long-lived token minted on your subscription. One-time setup:

  1. in another terminal:  claude setup-token
  2. then here:            weekly-insights auth      (paste the token it printed)

%s in the environment also works for a single run.`, err, auth.EnvVar)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "harvested %d session-meta and %d facet files into the real cache\n",
		res.HarvestedMeta, res.HarvestedFacets)
	if res.HarvestErrors > 0 {
		// The report is still good; only the cache refresh was partial.
		fmt.Fprintf(os.Stderr, "warning: %d cache files could not be copied into the real cache\n",
			res.HarvestErrors)
	}
	fmt.Println(res.Report)
	if *openIt {
		openPath(res.Report)
	}
	return nil
}

// cmdProgress reads the raw weekly reports and asks for a progression memo.
// It never modifies those reports; the memo is written beside them.
func cmdProgress(args []string) error {
	fs := flag.NewFlagSet("progress", flag.ExitOnError)
	weeks := fs.Int("weeks", 4, "how many of the most recent weekly reports to compare")
	reportsDir := fs.String("reports", "", "directory holding insights-<N>d-<date>.html (default ~/claude-weekly-insights)")
	root := fs.String("root", "", "override the usage-data directory (for the numeric trend)")
	claudeBin := fs.String("claude", "claude", "path to the Claude Code binary")
	dryRun := fs.Bool("dry-run", false, "print the exact prompt and data instead of calling the model")
	openIt := fs.Bool("open", false, "open the memo when done")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Below two there is no progression to judge, and the slice below would
	// take a suffix of a negative length.
	if *weeks < 2 {
		return fmt.Errorf("--weeks must be at least 2 to compare weekly reports; got %d", *weeks)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if *reportsDir == "" {
		*reportsDir = filepath.Join(home, "claude-weekly-insights")
	}

	paths, err := progress.Discover(*reportsDir)
	if err != nil {
		return err
	}
	if len(paths) < 2 {
		return fmt.Errorf("need at least two weekly reports in %s to judge progression; found %d (run \"weekly-insights --days 7\" for this week and add --end YYYY-MM-DD for past weeks)", *reportsDir, len(paths))
	}
	if len(paths) > *weeks {
		paths = paths[len(paths)-*weeks:]
	}
	in := progress.Input{}
	for _, p := range paths {
		w, err := progress.Extract(p)
		if err != nil {
			return err
		}
		in.Weeks = append(in.Weeks, w)
	}

	// The user's global CLAUDE.md is what they actually adopted; the reports
	// only show what was suggested.
	if b, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md")); err == nil {
		in.ClaudeMD = string(b)
	}
	// The numeric trend is optional context from the snapshot supplement.
	sp, err := store.DefaultPaths()
	if err == nil {
		if *root != "" {
			sp.Root = *root
		}
		if snaps, err := snapshot.List(sp); err == nil && len(snaps) > 1 {
			in.Trend = report.Trend(snaps)
		}
	}

	if *dryRun {
		fmt.Println(progress.Instructions())
		fmt.Println()
		fmt.Print(in.Text())
		return nil
	}

	fmt.Fprintf(os.Stderr, "comparing %d weekly reports (%s .. %s) with the default model ...\n",
		len(in.Weeks), in.Weeks[0].Label, in.Weeks[len(in.Weeks)-1].Label)
	token, _ := auth.Resolve()
	memo, raw, err := progress.Run(in, progress.Options{ClaudeBin: *claudeBin, Token: token, Stderr: os.Stderr})
	switch {
	case errors.Is(err, progress.ErrParse):
		// The process succeeded, so its reply is still the model's answer; the
		// page shows it verbatim rather than losing it.
		fmt.Fprintf(os.Stderr, "warning: %v; writing the raw reply instead\n", err)
	case err != nil:
		// Anything else, including a failed child, is a failed command.
		return err
	}
	last := in.Weeks[len(in.Weeks)-1]
	out := filepath.Join(*reportsDir, fmt.Sprintf("progress-%dd-%s.html", last.Days, last.Label))
	// The memo quotes the weekly reports, so it gets the same owner-only
	// treatment they do.
	if err := writeSensitive(out, []byte(progress.Render(memo, raw, in))); err != nil {
		return fmt.Errorf("writing memo: %w", err)
	}
	fmt.Println(out)
	if *openIt {
		openPath(out)
	}
	return nil
}

// cmdAuth stores the token once so the README setup is two lines instead of a
// platform-specific keychain incantation.
func cmdAuth(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	check := fs.Bool("check", false, "report whether a token is available and where, without printing it")
	clear := fs.Bool("clear", false, "remove any stored token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *clear:
		if err := auth.Clear(); err != nil {
			return err
		}
		fmt.Println("stored token removed")
		return nil
	case *check:
		if _, src := auth.Resolve(); src != auth.SourceNone {
			fmt.Printf("token available (%s)\n", src)
			return nil
		}
		return fmt.Errorf("no token found; run \"weekly-insights auth\"")
	}

	// Echo is only suppressible on a terminal, so the prompt needs to know
	// whether stdin is one or a pipe.
	isTerminal := false
	if fi, err := os.Stdin.Stat(); err == nil {
		isTerminal = fi.Mode()&os.ModeCharDevice != 0
	}
	line, err := auth.PromptToken(os.Stdin, os.Stderr, isTerminal)
	if err != nil && line == "" {
		return fmt.Errorf("reading token: %w", err)
	}
	src, err := auth.Store(line)
	if err != nil {
		return err
	}
	where := string(src)
	if src == auth.SourceFile {
		if p, e := auth.Path(); e == nil {
			where = p
		}
	}
	fmt.Fprintf(os.Stderr, "token stored (%s)\n", where)
	return nil
}

// writeSensitive writes a page this tool generates from the user's own
// sessions. Those pages quote prompts and project names, so the directory is
// created owner-only and the file is owner read/write, including when it
// replaces one left behind with looser permissions.
func writeSensitive(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	// WriteFile applies its mode only when it creates the file.
	return os.Chmod(path, 0o600)
}

// openPath opens a file with the platform's default handler. Failures are
// ignored: the path was already printed, and a missing opener is not a reason
// to fail a run that produced its report.
func openPath(p string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", p)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	_ = cmd.Start()
}

func cmdSelect(args []string) error {
	fs := flag.NewFlagSet("select", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	worklist := fs.Bool("worklist", false, "print one JSON line per session needing extraction")
	limit := fs.Int("limit", 0, "cap the worklist (0 means no cap)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, o, err := cf.resolve()
	if err != nil {
		return err
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}

	var need []string
	for _, s := range r.Substantive {
		if window.NeedsExtraction(s) {
			need = append(need, s.Meta.SessionID)
		}
	}

	if *worklist {
		// Longest sessions first: they carry the most signal, so a capped run
		// spends its budget where it matters.
		rows := make([]int, 0, len(r.Substantive))
		for i, s := range r.Substantive {
			if window.NeedsExtraction(s) && p.TranscriptPath(s.Meta.SessionID) != "" {
				rows = append(rows, i)
			}
		}
		sort.SliceStable(rows, func(a, b int) bool {
			return r.Substantive[rows[a]].Meta.UserMessageCount >
				r.Substantive[rows[b]].Meta.UserMessageCount
		})
		if *limit > 0 && len(rows) > *limit {
			rows = rows[:*limit]
		}
		for _, i := range rows {
			m := r.Substantive[i].Meta
			fmt.Printf(`{"session_id":%q,"transcript":%q,"project":%q,"start":%q,"user_messages":%d}`+"\n",
				m.SessionID, p.TranscriptPath(m.SessionID), m.ProjectPath, m.StartTime, m.UserMessageCount)
		}
		return nil
	}

	withFacets := 0
	bySource := map[string]int{}
	for _, s := range r.Substantive {
		if s.Facets != nil {
			withFacets++
			bySource[s.FacetSource]++
		}
	}
	fmt.Printf("window       : %s .. %s  (%dd)\n",
		r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"), o.Days)
	fmt.Printf("sessions     : %d in window, %d excluded (scratch/eval), %d substantive\n",
		len(r.All), r.ExcludedScratch, len(r.Substantive))
	fmt.Printf("facets       : %d canonical, %d builtin-only, %d missing\n",
		bySource[store.SourceCanonical], bySource[store.SourceBuiltin],
		len(r.Substantive)-withFacets)
	fmt.Printf("to extract   : %d\n", len(need))
	if r.Derived > 0 {
		fmt.Printf("derived meta : %d session(s) had no cached record; metadata was read from the transcript\n", r.Derived)
	}

	proj := map[string]int{}
	for _, s := range r.Substantive {
		proj[s.Meta.ProjectPath]++
	}
	fmt.Println("top projects :")
	for _, kv := range topN(proj, 6) {
		fmt.Printf("   %4d  %s\n", kv.n, kv.k)
	}
	return nil
}

// cmdFailures classifies every failed tool call in the window. It reads the
// transcripts directly rather than the session-meta counters, because the
// counters record how many tool errors happened and not what any of them was.
func cmdFailures(args []string) error {
	fs := flag.NewFlagSet("failures", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, o, err := cf.resolve()
	if err != nil {
		return err
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}
	sum := failures.Summarize(failures.Collect(p, r.Substantive), len(r.Substantive))
	if *asJSON {
		return writeJSONTo(os.Stdout, sum)
	}
	label := fmt.Sprintf("%s .. %s  (%dd)",
		r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"), o.Days)
	fmt.Print(failures.Report(sum, label))
	return nil
}

func cmdPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	out := fs.String("out", "", "directory to write rendered transcripts into (required)")
	limit := fs.Int("limit", 0, "cap the number of transcripts (0 means no cap)")
	all := fs.Bool("all", false, "render every substantive session, not only those needing facets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	p, o, err := cf.resolve()
	if err != nil {
		return err
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}
	// Rendered transcripts are the rawest form of the session data this tool
	// touches, so the directory holding them is owner-only.
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", *out, err)
	}
	n := 0
	for _, s := range r.Substantive {
		if !*all && !window.NeedsExtraction(s) {
			continue
		}
		if *limit > 0 && n >= *limit {
			break
		}
		m := s.Meta
		err := transcript.WriteFor(p, m.SessionID, m.ProjectPath, m.StartTime, *out,
			transcript.Options{})
		if err != nil {
			// A session whose transcript has been rotated away should not stop
			// the run; the rest of the window is still worth preparing.
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", m.SessionID[:8], err)
			continue
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "prepared %d transcripts in %s\n", n, *out)
	return nil
}

func cmdAggregate(args []string) error {
	fs := flag.NewFlagSet("aggregate", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	explain := fs.Bool("explain", false, "print how each free-form label collapsed")
	noSave := fs.Bool("no-save", false, "do not write the snapshot")
	quiet := fs.Bool("quiet", false, "do not print the snapshot JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, o, err := cf.resolve()
	if err != nil {
		return err
	}
	r, err := window.Select(p, o)
	if err != nil {
		return err
	}
	var collapses map[string]map[string]bool
	if *explain {
		collapses = map[string]map[string]bool{}
	}
	snap := snapshot.Build(r, o.Days, collapses)
	// Failures are counted from the transcripts, which Build does not read, so
	// the block is attached here. It is additive: a snapshot without it is
	// still a valid snapshot.
	snap.Failures = failures.SnapshotSummary(
		failures.Summarize(failures.Collect(p, r.Substantive), len(r.Substantive)))

	if *explain {
		fmt.Fprintln(os.Stderr, "label normalization (canonical <- free-form seen this window):")
		keys := make([]string, 0, len(collapses))
		for k := range collapses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kind, canon, _ := strings.Cut(k, ":")
			var noisy []string
			for raw := range collapses[k] {
				if raw != canon {
					noisy = append(noisy, raw)
				}
			}
			if len(noisy) == 0 {
				continue
			}
			sort.Strings(noisy)
			fmt.Fprintf(os.Stderr, "  [%s] %s <- %s\n", kind, canon, strings.Join(noisy, ", "))
		}
	}
	if !*noSave {
		path, err := snapshot.Save(p, snap)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "snapshot saved: %s\n", path)
	}
	if !*quiet {
		if err := writeJSONTo(os.Stdout, snap); err != nil {
			return err
		}
	}
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	root := fs.String("root", "", "override the usage-data directory (for testing)")
	current := fs.String("current", "", "snapshot label to report on (default: newest)")
	previous := fs.String("previous", "", "snapshot label to compare against (default: the one before)")
	trend := fs.Bool("trend", false, "print every snapshot as one trend table")
	asHTML := fs.Bool("html", false, "render as a standalone HTML page")
	outDir := fs.String("out", "", "with --html, write a file per snapshot into this directory and print the paths")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := store.DefaultPaths()
	if err != nil {
		return err
	}
	if *root != "" {
		p.Root = *root
	}
	snaps, err := snapshot.List(p)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return fmt.Errorf("no snapshots in %s: run \"weekly-insights aggregate\" first", p.Snapshots())
	}
	// --out writes the whole history as browsable files, which is the mode for
	// going through a week in a browser rather than a terminal.
	if *outDir != "" {
		if !*asHTML {
			return fmt.Errorf("--out requires --html")
		}
		// These pages quote the same session data the weekly reports do.
		if err := os.MkdirAll(*outDir, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", *outDir, err)
		}
		for i := range snaps {
			var prev *snapshot.Snapshot
			if i > 0 {
				prev = &snaps[i-1]
			}
			label := snaps[i].Window.Label
			path := filepath.Join(*outDir, "week-"+label+".html")
			page := report.WeeklyHTML(snaps[i], prev)
			if err := writeSensitive(path, []byte(page)); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			fmt.Println(path)
		}
		tp := filepath.Join(*outDir, "trend.html")
		if err := writeSensitive(tp, []byte(report.TrendHTML(snaps))); err != nil {
			return fmt.Errorf("writing %s: %w", tp, err)
		}
		fmt.Println(tp)
		return nil
	}

	if *trend {
		if *asHTML {
			fmt.Print(report.TrendHTML(snaps))
			return nil
		}
		fmt.Print(report.Trend(snaps))
		return nil
	}

	idx := len(snaps) - 1
	if *current != "" {
		idx = -1
		for i, s := range snaps {
			if s.Window.Label == *current {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("no snapshot labeled %q in %s", *current, p.Snapshots())
		}
	}
	cur := snaps[idx]

	var prev *snapshot.Snapshot
	switch {
	case *previous != "":
		for i := range snaps {
			if snaps[i].Window.Label == *previous {
				prev = &snaps[i]
				break
			}
		}
		if prev == nil {
			return fmt.Errorf("no snapshot labeled %q in %s", *previous, p.Snapshots())
		}
	case idx > 0:
		prev = &snaps[idx-1]
	}
	// Comparing a snapshot against itself would render every delta as zero and
	// read as a stable week, which is worse than saying there is no baseline.
	if prev != nil && prev.Window.Label == cur.Window.Label {
		prev = nil
	}
	if *asHTML {
		fmt.Print(report.WeeklyHTML(cur, prev))
		return nil
	}
	fmt.Print(report.Weekly(cur, prev))
	return nil
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	root := fs.String("root", "", "override the usage-data directory (for testing)")
	dir := fs.String("dir", "", "directory of facet files (default: the weekly-facets cache)")
	fix := fs.Bool("fix", false, "normalize off-vocabulary labels in place")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := store.DefaultPaths()
	if err != nil {
		return err
	}
	if *root != "" {
		p.Root = *root
	}
	target := p.WeeklyFacets()
	if *dir != "" {
		target = *dir
	}
	res, err := validate.Dir(target, *fix)
	if err != nil {
		return err
	}
	byFile := map[string][]string{}
	var order []string
	for _, pr := range res.Problems {
		if _, seen := byFile[pr.File]; !seen {
			order = append(order, pr.File)
		}
		byFile[pr.File] = append(byFile[pr.File], pr.Message)
	}
	fixed := map[string]bool{}
	for _, f := range res.Fixed {
		fixed[f] = true
	}
	for _, f := range order {
		suffix := ""
		if fixed[f] {
			suffix = " [fixed]"
		}
		fmt.Printf("%s%s\n", filepath.Base(f), suffix)
		for _, m := range byFile[f] {
			fmt.Printf("    %s\n", m)
		}
	}
	fmt.Printf("\n%d facet files, %d with problems\n", res.Checked, len(order))
	// Unfixed problems are an error so a pipeline stops before aggregating a
	// window whose labels would not be comparable.
	if len(order) > 0 && !*fix {
		os.Exit(1)
	}
	// --fix is not a guarantee: anything it could not normalize is still an
	// off-vocabulary label, and exiting zero here would hide that.
	if len(res.Unresolved) > 0 {
		fmt.Printf("\n%d problems remain after --fix:\n", len(res.Unresolved))
		for _, pr := range res.Unresolved {
			fmt.Printf("  %s: %s\n", filepath.Base(pr.File), pr.Message)
		}
		os.Exit(1)
	}
	return nil
}

type kv struct {
	k string
	n int
}

func topN(m map[string]int, n int) []kv {
	out := make([]kv, 0, len(m))
	for k, v := range m {
		out = append(out, kv{k, v})
	}
	// Ties break on the key so repeated runs print the same order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].k < out[j].k
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func writeJSONTo(f *os.File, v any) error {
	b, err := marshalIndent(v)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
