package report

import (
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/snapshot"
)

func TestWeeklyHTMLHasBuiltinLayout(t *testing.T) {
	cur, prev := base(), base()
	prev.Rates[snapshot.RateFrictionPerSession] = 5.0
	got := WeeklyHTML(cur, &prev)

	// The layout borrowed from the builtin: stats strip, bar-chart cards, no tables.
	for _, want := range []string{"<!doctype html>", `class="stats-row"`, `class="charts-row"`, `class="chart-card"`, `class="bar-row"`, `class="delta-grid"`, `class="nav-toc"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "<table") {
		t.Error("HTML report must not use tables; the builtin layout uses bar charts")
	}
	if !strings.Contains(got, `delta-card better`) {
		t.Error("expected the friction delta card to be marked better")
	}
}

func TestWeeklyHTMLFirstSnapshotHasNoDeltas(t *testing.T) {
	got := WeeklyHTML(base(), nil)
	if strings.Contains(got, `class="delta-grid"`) {
		t.Error("no prior week: delta grid must be omitted")
	}
	if !strings.Contains(got, "First snapshot") {
		t.Error("expected the first-snapshot note")
	}
}

func TestWeeklyHTMLEscapesUserText(t *testing.T) {
	cur := base()
	cur.UserCorrections = []snapshot.UserCorrection{{Session: "s", Date: "2026-09-07", Quote: `use <script> & "quotes"`}}
	cur.FrictionDetails = []snapshot.FrictionDetail{{Session: "s", Date: "2026-09-07", Project: "<b>", Detail: "a & b"}}
	got := WeeklyHTML(cur, nil)

	if strings.Contains(got, "<script>") || strings.Contains(got, "<strong><b>") {
		t.Error("user text was not escaped")
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;", "&lt;b&gt;"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected escaped %q", want)
		}
	}
}

func TestWeeklyHTMLKeepsEveryCorrectionGroupedByDay(t *testing.T) {
	cur := base()
	cur.UserCorrections = nil
	for i := 0; i < 20; i++ {
		day := "2026-09-07"
		if i >= 10 {
			day = "2026-09-09"
		}
		cur.UserCorrections = append(cur.UserCorrections, snapshot.UserCorrection{Session: "s", Date: day, Quote: quoteFor(i)})
	}
	got := WeeklyHTML(cur, nil)
	for i := 0; i < 20; i++ {
		if !strings.Contains(got, quoteFor(i)) {
			t.Errorf("correction %d dropped", i)
		}
	}
	if strings.Count(got, `class="quote-card"`) != 2 {
		t.Errorf("expected one quote card per day, got %d", strings.Count(got, `class="quote-card"`))
	}
	if strings.Index(got, "Sep 7, 2026") > strings.Index(got, "Sep 9, 2026") {
		t.Error("days are not in ascending order")
	}
}

func TestWeeklyHTMLNoEmDash(t *testing.T) {
	// The builtin's prose uses em dashes; this report's own text must not.
	// User quotes are verbatim and exempt, so test with none present.
	cur := base()
	cur.UserCorrections, cur.FrictionDetails = nil, nil
	if strings.Contains(WeeklyHTML(cur, nil), "—") {
		t.Error("report text contains an em dash")
	}
	if strings.Contains(TrendHTML([]snapshot.Snapshot{base()}), "—") {
		t.Error("trend text contains an em dash")
	}
}

func TestTrendHTMLOneChartPerCard(t *testing.T) {
	a, b := base(), base()
	a.Window.Label, b.Window.Label = "2026-09-04", "2026-09-11"
	got := TrendHTML([]snapshot.Snapshot{a, b})

	wantCards := len(tracked) + 3 // sessions, failures per session, self-inflicted share
	if n := strings.Count(got, `class="chart-card"`); n != wantCards {
		t.Errorf("expected %d chart cards, got %d", wantCards, n)
	}
	if n := strings.Count(got, `<svg `); n != wantCards {
		t.Errorf("expected one svg per card, got %d", n)
	}
	for _, gone := range []string{"<table", `class="bar-row`, "<script"} {
		if strings.Contains(got, gone) {
			t.Errorf("trend page must not contain %q", gone)
		}
	}
	// Neither week has failures, so those two cards draw no points at all.
	if n := strings.Count(got, "<circle"); n != (len(tracked)+1)*2 {
		t.Errorf("expected two points per card with data, got %d circles", n)
	}
	if !strings.Contains(got, "Friction events / session <span class=\"chart-better\">&darr; better</span>") ||
		!strings.Contains(got, "Fully or mostly achieved <span class=\"chart-better\">&uarr; better</span>") {
		t.Error("expected each rate card to say which direction is better")
	}
}

func TestTrendHTMLFailureCards(t *testing.T) {
	a, b := base(), base()
	a.Window.Label = "2026-09-04"
	a.Rates[snapshot.RateIgnoredPreference] = 0.1 // so the only possible zero is a failures one
	b.Failures = &snapshot.FailureSummary{Total: 8, PerSession: 0.5, SelfInflicted: 2, External: 6}
	got := TrendHTML([]snapshot.Snapshot{a, b})
	for _, want := range []string{"<title>Failures / session</title>", "<title>Sep 11: 0.50</title>", "<title>Sep 11: 25.0%</title>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "<title>Sep 4: 0</title>") {
		t.Error("a week without a failures block must be a gap, not a zero")
	}
}

func TestTrendHTMLMarksFacetSourceBoundary(t *testing.T) {
	week := func(label string, src map[string]int) snapshot.Snapshot {
		s := base()
		s.Window.Label = label
		s.Sessions.FacetSource = src
		return s
	}
	builtin, canonical := map[string]int{"builtin": 9, "canonical": 1}, map[string]int{"canonical": 7}
	cards := len(tracked) + 3

	same := TrendHTML([]snapshot.Snapshot{week("2026-09-04", builtin), week("2026-09-11", builtin), week("2026-09-18", nil)})
	if strings.Contains(same, "facet source changed") || strings.Contains(same, "Facet source changed") {
		t.Error("no source change: expected no annotation and no warning")
	}

	got := TrendHTML([]snapshot.Snapshot{
		week("2026-09-04", builtin), week("2026-09-11", builtin), week("2026-09-18", nil), week("2026-09-25", canonical),
	})
	if n := strings.Count(got, ">facet source changed</text>"); n != cards {
		t.Errorf("expected the boundary on every chart (%d), got %d", cards, n)
	}
	if !strings.Contains(got, "Facet source changed between the weeks ending Sep 11, 2026 (mostly builtin) and Sep 25, 2026 (mostly canonical).") {
		t.Error("expected the warning to name the weeks on either side of the boundary")
	}
	// The mark sits at the fourth week, the last x position.
	if !strings.Contains(got, `<path d="M580.0 22.0V152.0" stroke-dasharray="2 3"`) {
		t.Error("expected the boundary line at the week the source changed")
	}
}

func TestTrendHTMLWithAnnotations(t *testing.T) {
	a, b := base(), base()
	a.Window.Label = "2026-09-04"
	got := TrendHTMLWith([]snapshot.Snapshot{a, b}, []Annotation{
		{Week: "2026-09-11", Label: "<script>fix</script>", Metric: snapshot.RateToolErrors},
		{Week: "2026-09-11", Label: "everywhere"},
		{Week: "2030-01-01", Label: "no such week"},
	})
	if n := strings.Count(got, "&lt;script&gt;fix&lt;/script&gt;</text>"); n != 1 {
		t.Errorf("metric annotation should appear on exactly one chart, escaped; got %d", n)
	}
	if strings.Contains(got, "<script>") {
		t.Error("annotation label was not escaped")
	}
	if n := strings.Count(got, ">everywhere</text>"); n != len(tracked)+3 {
		t.Errorf("an annotation with no metric belongs on every chart, got %d", n)
	}
	if strings.Contains(got, "no such week") {
		t.Error("an annotation for an unknown week must be dropped")
	}
	if TrendHTMLWith([]snapshot.Snapshot{a, b}, nil) != TrendHTML([]snapshot.Snapshot{a, b}) {
		t.Error("TrendHTML must equal TrendHTMLWith with no annotations")
	}
}

func TestDominantSourceTieIsStable(t *testing.T) {
	if got := dominantSource(map[string]int{"canonical": 3, "builtin": 3}); got != "builtin" {
		t.Errorf("tie should go to the alphabetically first source, got %q", got)
	}
	if got := dominantSource(map[string]int{"builtin": 0}); got != "" {
		t.Errorf("zero counts mean no source, got %q", got)
	}
}

func TestTrendHTMLEmpty(t *testing.T) {
	if !strings.Contains(TrendHTML(nil), "No snapshots yet") {
		t.Error("empty trend should say so rather than render an empty grid")
	}
}

func TestWeeklyHTMLFootnoteSummarisesFacetSources(t *testing.T) {
	cur := base()
	cur.Sessions.FacetSource = map[string]int{"builtin": 2, "canonical": 3}
	got := WeeklyHTML(cur, nil)
	if !strings.Contains(got, "Facets: 3 canonical, 2 builtin.") {
		t.Error("expected facet sources listed canonical first, then builtin")
	}
}

func TestWeeklyHTMLSkipsMetricsMissingFromBothWeeks(t *testing.T) {
	cur, prev := base(), base()
	cur.Rates, prev.Rates = nil, nil
	got := WeeklyHTML(cur, &prev)

	if !strings.Contains(got, `class="delta-grid"`) {
		t.Fatal("expected the delta grid when a prior week exists")
	}
	if n := strings.Count(got, `class="delta-card`); n != 0 {
		t.Errorf("metrics absent from both weeks must be skipped, got %d delta cards", n)
	}
}

func TestHumanizeEmptyLabel(t *testing.T) {
	if got := humanize(""); got != "" {
		t.Errorf("humanize(\"\") = %q, want empty string", got)
	}
}

func TestWeeklyHTMLEscapesUnparseableWindowDate(t *testing.T) {
	cur := base()
	cur.Window.Start = "<soon>"
	got := WeeklyHTML(cur, nil)

	if strings.Contains(got, "<soon>") {
		t.Error("unparseable date was not escaped")
	}
	if !strings.Contains(got, `<div class="subtitle">&lt;soon&gt; to `) {
		t.Error("expected the unparseable start date to fall back to its escaped raw text")
	}
}

func TestTrendHTMLEscapesUnparseableWeekLabel(t *testing.T) {
	a := base()
	a.Window.Label = "not a <date> label"
	got := TrendHTML([]snapshot.Snapshot{a})

	if !strings.Contains(got, `>not a &lt;date&gt; label</text>`) {
		t.Error("unparseable week label should be shown escaped as the axis label")
	}
	if strings.Contains(got, "<date>") {
		t.Error("week label was not escaped")
	}
}

func TestChartMarksBarDeltasAgainstLastWeek(t *testing.T) {
	var b strings.Builder
	cur := map[string]int{"fix_bug": 5, "refactor": 2, "write_docs": 3}
	prev := map[string]int{"fix_bug": 3, "refactor": 4}
	chart(&b, "Goals", cur, prev, 10, "#2563eb")
	got := b.String()

	for _, want := range []string{
		`<div class="bar-value">5 <span class="bar-delta up">+2</span></div>`,
		`<div class="bar-value">2 <span class="bar-delta down">-2</span></div>`,
		`<div class="bar-value">3 <span class="bar-delta new">new</span></div>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestShortProjectsKeepsLastTwoSegmentsAndMerges(t *testing.T) {
	got := shortProjects(map[string]int{
		"/home/me/code/weekly-insights/": 3,
		"/work/code/weekly-insights":     2,
		"tools":                          1,
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 projects after shortening, got %v", got)
	}
	if got["code/weekly-insights"] != 5 {
		t.Errorf("expected long paths trimmed to their last two segments and merged to 5, got %v", got)
	}
	if got["tools"] != 1 {
		t.Errorf("expected short path kept as is, got %v", got)
	}
}
