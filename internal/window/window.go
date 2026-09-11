// Package window narrows the session-meta cache down to one time window.
//
// Every report in this tool is scoped to a window, which is the difference
// between it and the builtin /insights: the builtin is cumulative and cannot
// answer "did this week differ from last week". Selection therefore has to be
// reproducible: the window is a closed interval on start_time, and nothing
// here reads the clock except when the caller leaves the end unset.
package window

import (
	"fmt"
	"sort"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// DefaultDays is one week, the cadence the whole tool is built around.
const DefaultDays = 7

// Options describes the window to select. The zero value is usable: it means
// the last seven days ending now, with the default exclusions applied.
type Options struct {
	Days   int       // window length, default 7
	End    time.Time // window end, default now (UTC)
	Config store.Config
	// IncludeScratch skips the config exclusions entirely. It exists for
	// auditing what the exclusions are throwing away, not for reporting.
	IncludeScratch bool
}

// Result is the selected population at three widths. Reports quote all three
// so a quiet week can be told apart from an over-aggressive exclusion rule.
type Result struct {
	Start, End      time.Time
	All             []model.SessionMeta // everything in the window
	Kept            []model.SessionMeta // after exclusions
	Substantive     []model.Session     // after exclusions AND the substantive filter, facets attached
	ExcludedScratch int
}

// Select reads the session-meta cache and returns the sessions in the window.
func Select(p store.Paths, o Options) (Result, error) {
	days := o.Days
	if days <= 0 {
		days = DefaultDays
	}
	end := o.End
	if end.IsZero() {
		end = time.Now().UTC()
	}
	end = end.UTC()
	start := end.AddDate(0, 0, -days)

	metas, err := p.LoadAllMeta()
	if err != nil {
		return Result{}, fmt.Errorf("loading session metadata: %w", err)
	}

	res := Result{Start: start, End: end}
	for _, m := range metas {
		ts, ok := parseStart(m.StartTime)
		// A record with no usable timestamp cannot be placed in any window, so
		// it is dropped rather than guessed at.
		if !ok || ts.Before(start) || ts.After(end) {
			continue
		}
		res.All = append(res.All, m)
		if !o.IncludeScratch && o.Config.Excluded(m.ProjectPath) {
			res.ExcludedScratch++
			continue
		}
		res.Kept = append(res.Kept, m)
		if !isSubstantive(m) {
			continue
		}
		f, src := p.LoadFacets(m.SessionID)
		res.Substantive = append(res.Substantive, model.Session{Meta: m, Facets: f, FacetSource: src})
	}

	// Ascending start order, so every downstream listing reads as a timeline
	// and two runs over the same window produce byte-identical output.
	sort.SliceStable(res.Substantive, func(i, j int) bool {
		a, _ := parseStart(res.Substantive[i].Meta.StartTime)
		b, _ := parseStart(res.Substantive[j].Meta.StartTime)
		return a.Before(b)
	})
	return res, nil
}

// isSubstantive reports whether a session is worth analyzing. Same filter the
// builtin applies before analyzing a session.
func isSubstantive(m model.SessionMeta) bool {
	return m.UserMessageCount >= 2 && m.DurationMinutes >= 1
}

// NeedsExtraction reports whether a session still needs facets extracted.
// Builtin facets count as missing: they use free-form labels invented per
// session, so counts built on them cannot be trended across weeks, and
// re-extracting is the only way to get the session into the fixed vocabulary.
func NeedsExtraction(s model.Session) bool {
	return s.Facets == nil || s.FacetSource == store.SourceBuiltin
}

// startLayouts are tried in order. The cache is written by several versions of
// the builtin and timestamps have appeared both with and without a zone, so a
// single layout would silently drop otherwise-valid records.
var startLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// parseStart parses a start_time, normalizing to UTC. A zoneless timestamp is
// read as UTC, matching how the builtin writes them.
func parseStart(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range startLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
