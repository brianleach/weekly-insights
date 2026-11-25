package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/brianleach/weekly-insights/internal/auth"
	"github.com/brianleach/weekly-insights/internal/extract"
	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/prompt"
	"github.com/brianleach/weekly-insights/internal/runner"
	"github.com/brianleach/weekly-insights/internal/window"
)

// cmdExtract fills weekly-facets for the window's sessions that lack canonical
// facets, one headless `claude -p` per session, validated before it lands.
func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	limit := fs.Int("limit", extract.DefaultLimit, "sessions to extract this run (--all lifts the cap)")
	all := fs.Bool("all", false, "extract every session that needs facets, ignoring --limit")
	conc := fs.Int("concurrency", extract.DefaultConcurrency,
		fmt.Sprintf("parallel model calls (at most %d)", extract.MaxConcurrency))
	timeout := fs.Duration("timeout", extract.DefaultTimeout, "limit for one session's model call")
	budget := fs.Duration("budget", extract.DefaultBudget, "total wall-clock limit; no session starts after it")
	force := fs.Bool("force", false, "re-extract sessions that already have canonical facets, replacing them")
	dryRun := fs.Bool("dry-run", false, "print the worklist and prompt size; make no model call")
	claudeBin := fs.String("claude", "claude", "path to the Claude Code binary")
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
	var sessions []model.Session
	for _, s := range r.Substantive {
		if *force || window.NeedsExtraction(s) {
			sessions = append(sessions, s)
		}
	}
	opts := extract.Options{
		Paths: p, Sessions: sessions, ClaudeBin: *claudeBin, Limit: *limit, All: *all,
		Concurrency: *conc, PerSessionTimeout: *timeout, Budget: *budget, Force: *force,
		Stderr: os.Stderr,
	}

	if *dryRun {
		w := extract.Plan(opts)
		for _, it := range w.Items {
			fmt.Printf("%s  %7d chars  %s\n", shortID(it.ID), len(it.Input), it.Project)
		}
		fmt.Printf("%d to extract, %d over the limit, %d already have facets, %d skipped; prompt is %d chars\n",
			len(w.Items), w.OverLimit, w.SkippedExisting, w.Skipped, len(prompt.Extraction()))
		return nil
	}

	// Ctrl-C cancels the children through the context, and Run removes its
	// scratch directory on the way out, so an interrupt leaves no partial file.
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	opts.Token, _ = auth.Resolve()
	res, err := extract.Run(ctx, opts)
	if len(res.Rejected) > 0 {
		fmt.Fprintf(os.Stderr, "rejected (left for next run): %s\n", strings.Join(res.Rejected, ", "))
	}
	if res.BudgetExceeded {
		fmt.Fprintf(os.Stderr, "budget of %s spent; the rest stays on the worklist\n", *budget)
	}
	fmt.Fprintf(os.Stderr, "extracted %d (%d fixed by validate), rejected %d, skipped-existing %d, failed %d, unfinished %d, elapsed %s\n",
		res.Extracted, res.Fixed, len(res.Rejected), res.SkippedExisting, res.Failed, res.Unfinished,
		res.Elapsed.Round(time.Second))
	if errors.Is(err, runner.ErrNotLoggedIn) {
		return fmt.Errorf("%w: run `claude` once to log in, or store a token with `weekly-insights auth`", err)
	}
	return err
}
