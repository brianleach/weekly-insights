package report

import (
	"strconv"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// base is a minimal but complete snapshot. Every field is synthetic: the tests
// must never depend on a real corpus, which would make them unreproducible and
// would leak transcript content into the repo.
func base() snapshot.Snapshot {
	return snapshot.Snapshot{
		Window: snapshot.Window{
			Start: "2026-09-05T00:00:00Z",
			End:   "2026-09-11T00:00:00Z",
			Days:  7,
			Label: "2026-09-11",
		},
		Sessions: snapshot.Sessions{
			InWindow:        20,
			ExcludedScratch: 4,
			Substantive:     16,
			WithFacets:      12,
		},
		Volume: snapshot.Volume{
			Messages: 300, Hours: 12.5, Commits: 9,
			LinesAdded: 1200, LinesRemoved: 340, DaysActive: 5,
		},
		Tools:    map[string]int{"Read": 50, "Edit": 30, "Bash": 20},
		Projects: map[string]int{"alpha": 6, "beta": 4},
		Goals:    map[string]int{"fix_bug": 5, "write_tests": 3},
		Friction: map[string]int{"unverified_claim": 3, "tool_failed": 1},
		Rates: map[string]float64{
			snapshot.RateFacetCoveragePct:   75,
			snapshot.RateFrictionPerSession: 0.25,
			snapshot.RateNegativePct:        10,
			snapshot.RateUnverifiedClaim:    0.19,
			snapshot.RateUnsanctionedAction: 0.06,
			snapshot.RateIgnoredPreference:  0,
			snapshot.RateInterruptions:      0.5,
			snapshot.RateToolErrors:         1.25,
			snapshot.RateAchievedPct:        80,
		},
	}
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\n--- got ---\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if strings.Contains(got, w) {
			t.Errorf("output unexpectedly contains %q\n--- got ---\n%s", w, got)
		}
	}
}

func TestWeeklyNoPrevious(t *testing.T) {
	got := Weekly(base(), nil)

	mustContain(t, got,
		"# Weekly insights — 2026-09-05 to 2026-09-11",
		"**16 substantive sessions** across 5 active days · 300 messages · 12.5h · 9 commits · +1200/-340 lines",
		// Percentages carry a fixed single decimal, so a whole value still
		// prints one: the column stays aligned when read across weeks.
		"Facet coverage 75.0% (12/16). 4 scratch/eval sessions excluded.",
		"## Week over week",
		"_First snapshot. No prior week to compare against; next run will show deltas._",
	)
	// With no prior week there is nothing to diff, so neither the table nor the
	// per-goal delta annotations should appear.
	mustNotContain(t, got, "| Metric | This week |", "Comparing against")
	mustContain(t, got, "| fix_bug | 5 |")
}

func TestWeeklyDeltasAndVerdicts(t *testing.T) {
	cur := base()
	prev := base()
	prev.Window.Label = "2026-09-04"
	prev.Sessions.Substantive = 14
	prev.Rates[snapshot.RateFrictionPerSession] = 0.75 // down-good, improved
	prev.Rates[snapshot.RateToolErrors] = 0.25         // down-good, regressed
	prev.Rates[snapshot.RateAchievedPct] = 60          // up-good, improved
	prev.Rates[snapshot.RateNegativePct] = 4           // down-good, regressed
	prev.Rates[snapshot.RateInterruptions] = 0.5       // unchanged

	got := Weekly(cur, &prev)

	mustContain(t, got,
		"Comparing against **2026-09-04** (14 sessions).",
		"| Metric | This week | Last week | Change | |",
		"|---|---:|---:|---:|---|",
		// Lower is better: a negative delta is an improvement.
		"| Friction events / session | 0.25 | 0.75 | -0.50 | better |",
		"| Tool errors / session | 1.25 | 0.25 | +1 | worse |",
		// Higher is better: the same positive sign flips the verdict. Percent
		// metrics keep one decimal in every cell, value and delta alike.
		"| Fully or mostly achieved | 80.0% | 60.0% | +20.0% | better |",
		"| Dissatisfied or frustrated | 10.0% | 4.0% | +6.0% | worse |",
		// Unchanged, and a whole-number delta on a non-percent metric drops
		// its decimals.
		"| Your interruptions / session | 0.50 | 0.50 | +0 | flat |",
	)
	// The decimal is not optional on whole percentages: the pre-decimal
	// spelling must not reappear.
	mustNotContain(t, got,
		"| Fully or mostly achieved | 80% ",
		"| Dissatisfied or frustrated | 10% ",
	)
}

func TestWeeklyMissingMetricsAreSkippedOrNA(t *testing.T) {
	cur := base()
	prev := base()
	prev.Window.Label = "2026-09-04"
	// Absent from both snapshots: the row is dropped entirely.
	delete(cur.Rates, snapshot.RateIgnoredPreference)
	delete(prev.Rates, snapshot.RateIgnoredPreference)
	// Absent from only the prior week: the row survives with an unknown delta.
	delete(prev.Rates, snapshot.RateUnverifiedClaim)

	got := Weekly(cur, &prev)

	mustNotContain(t, got, "Ignored preferences / session")
	mustContain(t, got, "| Unverified claims / session | 0.19 | n/a | n/a | flat |")
}

func TestWeeklyGoalAndFrictionDeltas(t *testing.T) {
	cur := base()
	prev := base()
	prev.Window.Label = "2026-09-04"
	prev.Goals = map[string]int{"fix_bug": 8}
	prev.Friction = map[string]int{"unverified_claim": 1}

	got := Weekly(cur, &prev)

	mustContain(t, got,
		"| fix_bug | 5 (-3) |",
		// write_tests is new this week, so it carries no parenthetical.
		"| write_tests | 3 |",
		"| unverified_claim | 3 | 1 |",
		// No prior count for tool_failed reads as "-", not as zero.
		"| tool_failed | 1 | - |",
	)
}

func TestWeeklyTiesOrderDeterministically(t *testing.T) {
	cur := base()
	cur.Goals = map[string]int{"zeta": 2, "alpha": 2, "mid": 2, "beta": 2}
	cur.Tools = map[string]int{"Write": 5, "Bash": 5, "Read": 5}

	// Map iteration order is randomized, so a tie that sorted only by count
	// would produce different output on different runs.
	first := Weekly(cur, nil)
	for i := 0; i < 20; i++ {
		if got := Weekly(cur, nil); got != first {
			t.Fatalf("output not deterministic across runs")
		}
	}
	goals := section(t, first, "## Where the time went")
	if want := "| alpha | 2 |\n| beta | 2 |\n| mid | 2 |\n| zeta | 2 |"; !strings.Contains(goals, want) {
		t.Errorf("ties not sorted by key ascending\n--- got ---\n%s", goals)
	}
	mustContain(t, first, "- Bash: 5 (33%)", "- Read: 5 (33%)", "- Write: 5 (33%)")
}

func TestWeeklyOmitsEmptyOptionalSections(t *testing.T) {
	cur := base()
	cur.Friction = nil

	got := Weekly(cur, nil)

	mustContain(t, got, "## Friction", "_None recorded._")
	mustNotContain(t, got, "### What you had to correct", "### Friction detail")
}

func TestWeeklyKeepsEveryCorrectionAndDetail(t *testing.T) {
	// These two sections are read line by line, so nothing may be dropped.
	// A flat capped list used to be dominated by whichever day had the most
	// sessions, silently hiding the rest of the week.
	cur := base()
	for i := 0; i < 20; i++ {
		day := "2026-09-07"
		if i >= 10 {
			day = "2026-09-09"
		}
		cur.UserCorrections = append(cur.UserCorrections, snapshot.UserCorrection{
			Session: "s", Date: day, Quote: quoteFor(i),
		})
		cur.FrictionDetails = append(cur.FrictionDetails, snapshot.FrictionDetail{
			Session: "s", Date: day, Project: "alpha", Detail: quoteFor(i),
		})
	}

	got := Weekly(cur, nil)

	// Headings carry the total so a reader can tell at a glance how much there is.
	mustContain(t, got, "### What you had to correct (20)", "### Friction detail (20)")

	// Every entry survives, including the last one on the later day.
	for i := 0; i < 20; i++ {
		mustContain(t, got, "\""+quoteFor(i)+"\"")
		mustContain(t, got, "— "+quoteFor(i))
	}

	// Corrections are grouped under a date heading, oldest day first.
	mustContain(t, got, "**2026-09-07**", "**2026-09-09**")
	if strings.Index(got, "**2026-09-07**") > strings.Index(got, "**2026-09-09**") {
		t.Error("correction day groups are not in ascending date order")
	}
}

func TestWeeklyGroupsCorrectionsByDayNotSessionOrder(t *testing.T) {
	// Snapshot order follows session order, which interleaves days. The report
	// must still group them, or a reader scanning by date sees duplicates.
	cur := base()
	for _, d := range []string{"2026-09-09", "2026-09-07", "2026-09-09", "2026-09-07"} {
		cur.UserCorrections = append(cur.UserCorrections, snapshot.UserCorrection{
			Session: "s", Date: d, Quote: d + "-q",
		})
	}

	got := Weekly(cur, nil)

	if n := strings.Count(got, "**2026-09-07**"); n != 1 {
		t.Errorf("expected one heading for 2026-09-07, got %d", n)
	}
	if n := strings.Count(got, "**2026-09-09**"); n != 1 {
		t.Errorf("expected one heading for 2026-09-09, got %d", n)
	}
}

func TestTrend(t *testing.T) {
	older := base()
	older.Window.Label = "2026-09-04"
	older.Sessions.Substantive = 14
	older.Rates[snapshot.RateFrictionPerSession] = 0.75

	newer := base()

	got := Trend([]snapshot.Snapshot{older, newer})

	want := strings.Join([]string{
		"# Weekly trend",
		"",
		"| Week | Sessions | Coverage | Friction/sess | Negative % | Unverified/sess | Unsanctioned/sess | Achieved % |",
		"|---|---|---|---|---|---|---|---|",
		// Coverage carries the sign; Negative %/Achieved % are bare because
		// their headers already do. Both keep one decimal so the columns line
		// up when read down across weeks.
		"| 2026-09-04 | 14 | 75.0% | 0.75 | 10.0 | 0.19 | 0.06 | 80.0 |",
		"| 2026-09-11 | 16 | 75.0% | 0.25 | 10.0 | 0.19 | 0.06 | 80.0 |",
		"",
	}, "\n")
	if !strings.HasPrefix(got, want) {
		t.Errorf("trend table mismatch\n--- want prefix ---\n%s\n--- got ---\n%s", want, got)
	}
	mustContain(t, got, "Check `facet_source` in each snapshot before reading a jump across that boundary as a real change.")
}

func TestNumFormatting(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{3, "3"},
		{0.25, "0.25"},
		{1.5, "1.50"},
		{9.999, "10.00"}, // below 10 before rounding, so two places apply
		{12.345, "12.3"},
		{-0.5, "-0.50"},
		{100, "100"},
	}
	for _, c := range cases {
		if got := num(c.in); got != c.want {
			t.Errorf("num(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := fmtOpt(0, false, "%"); got != "n/a" {
		t.Errorf("missing value = %q, want n/a", got)
	}
}

func quoteFor(i int) string {
	return "q" + strconv.Itoa(i)
}

// section returns the text from heading to the end of the report, so a table
// assertion cannot accidentally match an identical row in another section.
func section(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("heading %q not found", heading)
	}
	return doc[i:]
}
