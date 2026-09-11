// Command weekly-insights reports on Claude Code usage over a time window,
// with week-over-week trends.
//
// Claude Code's builtin /insights is cumulative over all history and takes no
// arguments, so a week of changed behavior is averaged against months of the
// old pattern. This command scopes to a window, pins a fixed label vocabulary
// so counts stay comparable, and diffs each run against the previous one.
//
// It reads only local files and makes no network calls.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/prompt"
	"github.com/brianleach/weekly-insights/internal/report"
	"github.com/brianleach/weekly-insights/internal/snapshot"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/transcript"
	"github.com/brianleach/weekly-insights/internal/validate"
	"github.com/brianleach/weekly-insights/internal/window"
)

// version is overridden at build time via -ldflags.
var version = "dev"

const usage = `weekly-insights - time-windowed usage insights for Claude Code

Usage:
  weekly-insights <command> [flags]

Commands:
  select      show what is in the window and how many sessions need facets
  prepare     render the window's transcripts to text for facet extraction
  aggregate   build and save a snapshot of the window
  report      render the newest snapshot, diffed against the previous one
              (--html for a browsable page, --out DIR for one file per week)
  validate    enforce the canonical vocabulary on extracted facets
  prompt      print the facet-extraction prompt
  version     print the version

Run "weekly-insights <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "select":
		err = cmdSelect(os.Args[2:])
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
		fmt.Println(version)
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
	if err := os.MkdirAll(*out, 0o755); err != nil {
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
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
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
			if err := os.WriteFile(path, []byte(page), 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			fmt.Println(path)
		}
		tp := filepath.Join(*outDir, "trend.html")
		if err := os.WriteFile(tp, []byte(report.TrendHTML(snaps)), 0o644); err != nil {
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
