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

func TestTrendHTMLOneCardPerMetricOneBarPerWeek(t *testing.T) {
	a, b := base(), base()
	a.Window.Label, b.Window.Label = "2026-09-04", "2026-09-11"
	got := TrendHTML([]snapshot.Snapshot{a, b})

	wantCards := len(tracked) + 1 // plus the sessions card
	if n := strings.Count(got, `class="chart-card"`); n != wantCards {
		t.Errorf("expected %d chart cards, got %d", wantCards, n)
	}
	// Two weeks means two bars per card.
	if n := strings.Count(got, `class="bar-row`); n != wantCards*2 {
		t.Errorf("expected %d bar rows, got %d", wantCards*2, n)
	}
	if !strings.Contains(got, `bar-row latest`) {
		t.Error("latest week should be highlighted")
	}
	if strings.Contains(got, "<table") {
		t.Error("trend must not use tables")
	}
}

func TestTrendHTMLEmpty(t *testing.T) {
	if !strings.Contains(TrendHTML(nil), "No snapshots yet") {
		t.Error("empty trend should say so rather than render an empty grid")
	}
}
