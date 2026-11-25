// Package extract runs canonical facet extraction headless, through the
// user's own `claude -p`, one session per call.
//
// The output directory is the one aggregate reads, so nothing reaches it
// unvalidated: each reply is written to a private scratch directory, run
// through validate --fix there, and only a file with no remaining problems is
// moved into place. A rejected session is simply left out, so it stays on next
// week's worklist.
//
// Every run is bounded: a session cap, a concurrency cap, a per-session
// timeout, a total wall-clock budget, and a circuit breaker on consecutive
// child failures.
package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/prompt"
	"github.com/brianleach/weekly-insights/internal/runner"
	"github.com/brianleach/weekly-insights/internal/safeio"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/transcript"
	"github.com/brianleach/weekly-insights/internal/validate"
)

// Defaults and hard limits. A zero Limit means the default, never "no cap":
// All is the only way past it, so a forgotten flag cannot spend a whole
// subscription window on one run.
const (
	DefaultLimit       = 20
	DefaultConcurrency = 2
	MaxConcurrency     = 4
	DefaultTimeout     = 3 * time.Minute
	DefaultBudget      = 30 * time.Minute
	breakerTrips       = 3
)

// ErrCircuitOpen stops a run after breakerTrips child failures in a row. A
// child that keeps failing is almost always failing for a reason that applies
// to every session (quota, network, a broken install), so carrying on would
// only repeat it.
var ErrCircuitOpen = errors.New("stopped after 3 consecutive failed extractions")

// Options configures one run.
type Options struct {
	Paths             store.Paths
	Sessions          []model.Session // candidates; callers pass the worklist
	ClaudeBin         string          // defaults to "claude"
	Token             string          // optional; never logged
	Limit             int             // sessions to attempt, default DefaultLimit
	All               bool            // lift the Limit cap
	Concurrency       int             // default DefaultConcurrency, at most MaxConcurrency
	PerSessionTimeout time.Duration   // default DefaultTimeout
	Budget            time.Duration   // total wall clock, default DefaultBudget
	Force             bool            // overwrite an existing facet file
	Stderr            io.Writer       // child stderr and per-session notes; nil discards
}

func (o Options) withDefaults() Options {
	if o.ClaudeBin == "" {
		o.ClaudeBin = "claude"
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.Concurrency > MaxConcurrency {
		o.Concurrency = MaxConcurrency
	}
	if o.PerSessionTimeout <= 0 {
		o.PerSessionTimeout = DefaultTimeout
	}
	if o.Budget <= 0 {
		o.Budget = DefaultBudget
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	return o
}

// Item is one session queued for the model, with the exact text it will read.
type Item struct {
	ID      string
	Project string
	Input   string
}

// Worklist is what a run would send, and why the rest was left out.
type Worklist struct {
	Items           []Item
	SkippedExisting int // a canonical facet file is already there
	Skipped         int // no transcript, or nothing renderable
	OverLimit       int // left for a later run by the cap
}

// Plan orders the candidates longest first, the same order `select
// --worklist` uses, and renders the ones a run would send.
func Plan(o Options) Worklist {
	o = o.withDefaults()
	ss := append([]model.Session(nil), o.Sessions...)
	sort.SliceStable(ss, func(i, j int) bool {
		return ss[i].Meta.UserMessageCount > ss[j].Meta.UserMessageCount
	})
	var w Worklist
	for _, s := range ss {
		id := s.Meta.SessionID
		if !store.ValidSessionID(id) {
			w.Skipped++
			continue
		}
		if !o.Force && exists(facetPath(o.Paths, id)) {
			w.SkippedExisting++
			continue
		}
		if !o.All && len(w.Items) >= o.Limit {
			w.OverLimit++
			continue
		}
		tp := o.Paths.TranscriptPath(id)
		if tp == "" {
			w.Skipped++
			continue
		}
		body, err := transcript.Render(tp, transcript.Options{})
		if err != nil || body == "" {
			w.Skipped++
			continue
		}
		// The same header prepare writes, so the model sees the session id it
		// is asked to echo back.
		in := fmt.Sprintf("Session: %s\nDate: %s\nProject: %s\n\n%s",
			id, s.Meta.StartTime, s.Meta.ProjectPath, body)
		w.Items = append(w.Items, Item{ID: id, Project: s.Meta.ProjectPath, Input: in})
	}
	return w
}

// Result counts what a run did. Fixed is a subset of Extracted.
type Result struct {
	Extracted       int
	Fixed           int
	Rejected        []string // short session ids
	SkippedExisting int
	Skipped         int
	Failed          int
	Unfinished      int // over the cap, never started, or cancelled in flight
	BudgetExceeded  bool
	Elapsed         time.Duration
}

type outcome int

const (
	landed outcome = iota
	landedFixed
	rejected
	failed
	cancelled
	existing
	notLoggedIn
)

// Run extracts facets for the planned sessions. It returns runner's
// ErrNotLoggedIn or ErrCircuitOpen when it stopped early, and the context's
// error when ctx was cancelled; a spent budget is reported on the Result, not
// as an error, because whatever landed before it is good.
func Run(ctx context.Context, o Options) (res Result, err error) {
	start := time.Now()
	o = o.withDefaults()
	w := Plan(o)
	res = Result{SkippedExisting: w.SkippedExisting, Skipped: w.Skipped, Unfinished: w.OverLimit}
	defer func() { res.Elapsed = time.Since(start) }()
	if len(w.Items) == 0 {
		return res, nil
	}
	if err := os.MkdirAll(o.Paths.WeeklyFacets(), 0o700); err != nil {
		return res, fmt.Errorf("creating %s: %w", o.Paths.WeeklyFacets(), err)
	}
	// MkdirTemp creates 0700. Replies hold the user's own summaries, so the
	// scratch copy is as private as the destination, and it goes on every path
	// out of this function.
	scratch, err := os.MkdirTemp("", "weekly-insights-extract-")
	if err != nil {
		return res, fmt.Errorf("creating scratch directory: %w", err)
	}
	defer os.RemoveAll(scratch)

	budgetCtx, cancelBudget := context.WithTimeout(ctx, o.Budget)
	defer cancelBudget()
	runCtx, stop := context.WithCancel(budgetCtx)
	defer stop()

	var (
		mu          sync.Mutex
		consecutive int
		stopErr     error
	)
	jobs := make(chan Item)
	var wg sync.WaitGroup
	for i := 0; i < o.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				if runCtx.Err() != nil {
					mu.Lock()
					res.Unfinished++
					mu.Unlock()
					continue
				}
				oc, err := one(runCtx, o, scratch, it)
				mu.Lock()
				switch oc {
				case landed, landedFixed:
					res.Extracted++
					if oc == landedFixed {
						res.Fixed++
					}
					consecutive = 0
				case rejected:
					res.Rejected = append(res.Rejected, short(it.ID))
					fmt.Fprintf(o.Stderr, "rejected %s: %v\n", short(it.ID), err)
					consecutive = 0
				case existing:
					res.SkippedExisting++
				case cancelled:
					res.Unfinished++
				case notLoggedIn:
					if stopErr == nil {
						stopErr = runner.ErrNotLoggedIn
					}
					res.Unfinished++
					stop()
				case failed:
					res.Failed++
					consecutive++
					fmt.Fprintf(o.Stderr, "failed %s: %v\n", short(it.ID), err)
					if consecutive >= breakerTrips && stopErr == nil {
						stopErr = fmt.Errorf("%w; last error: %v", ErrCircuitOpen, err)
						stop()
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, it := range w.Items {
		jobs <- it
	}
	close(jobs)
	wg.Wait()

	if stopErr != nil {
		return res, stopErr
	}
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("extraction interrupted: %w", err)
	}
	if errors.Is(budgetCtx.Err(), context.DeadlineExceeded) {
		res.BudgetExceeded = true
	}
	return res, nil
}

// one runs the model for a single session and lands the result if it is clean.
func one(ctx context.Context, o Options, scratch string, it Item) (outcome, error) {
	sctx, cancel := context.WithTimeout(ctx, o.PerSessionTimeout)
	defer cancel()
	// Instructions as the prompt, data on stdin: the same split progress uses,
	// which keeps a long transcript clear of argv limits.
	cmd := exec.CommandContext(sctx, o.ClaudeBin, "-p", prompt.Extraction(), "--output-format", "text")
	cmd.Dir = os.TempDir()
	env := runner.BaseEnv()
	// BaseEnv strips any inherited token, so it is put back only when resolved.
	if o.Token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+o.Token)
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader(it.Input)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = o.Stderr
	// A killed child can leave a grandchild holding stdout open; without this
	// Wait would block until it exits, defeating the timeout.
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()

	if ctx.Err() != nil {
		// The run was stopped (budget, Ctrl-C, another session's stop); that is
		// not this child's failure and nothing it printed is kept.
		return cancelled, ctx.Err()
	}
	if strings.Contains(out.String(), "Not logged in") {
		return notLoggedIn, runner.ErrNotLoggedIn
	}
	if sctx.Err() != nil {
		return failed, fmt.Errorf("timed out after %s", o.PerSessionTimeout)
	}
	if runErr != nil {
		return failed, fmt.Errorf("running %s -p: %v", o.ClaudeBin, runErr)
	}

	raw := firstObject(out.String())
	if raw == "" || !json.Valid([]byte(raw)) {
		return rejected, errors.New("reply held no JSON object")
	}
	dir := filepath.Join(scratch, it.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return failed, fmt.Errorf("creating scratch for %s: %w", short(it.ID), err)
	}
	tmp := filepath.Join(dir, it.ID+".json")
	if err := safeio.WriteFile(tmp, []byte(raw), 0o600); err != nil {
		return failed, err
	}
	vr, err := validate.Dir(dir, true)
	if err != nil {
		return failed, err
	}
	if len(vr.Unresolved) > 0 {
		return rejected, errors.New(vr.Unresolved[0].Message)
	}
	if ctx.Err() != nil {
		return cancelled, ctx.Err()
	}
	dst := facetPath(o.Paths, it.ID)
	// Re-checked here: another writer may have produced the file while the
	// model was running, and without --force it is never replaced.
	if !o.Force && exists(dst) {
		return existing, nil
	}
	// safeio renames into place, so an interrupted copy never leaves a partial
	// facet file for aggregate to read.
	if err := safeio.CopyFile(tmp, dst, 0o600); err != nil {
		return failed, err
	}
	if len(vr.Fixed) > 0 {
		return landedFixed, nil
	}
	return landed, nil
}

var reObject = regexp.MustCompile(`(?s)\{.*\}`)

// firstObject pulls the JSON object out of a reply that may carry prose or a
// code fence around it, the tolerance progress applies to its own replies.
func firstObject(reply string) string { return reObject.FindString(reply) }

func facetPath(p store.Paths, id string) string {
	return filepath.Join(p.WeeklyFacets(), id+".json")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
