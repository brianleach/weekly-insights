package report

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repo had no golden files before this one; regenerate it with
// go test ./internal/report -run TestLineChartGolden -update
var update = flag.Bool("update", false, "rewrite golden files")

func sixWeeks() ([]series, chartOpts) {
	ss := []series{
		{name: "Tool errors / session", points: []point{{1.5, true}, {1.25, true}, {}, {0.75, true}, {1, true}, {0.5, true}}},
		{name: "Self-inflicted / session", points: []point{{0.5, true}, {0.25, true}, {0.25, true}, {}, {0.1, true}, {0, true}}},
	}
	o := chartOpts{
		title:  "Tool errors / session",
		weeks:  []string{"Aug 7", "Aug 14", "Aug 21", "Aug 28", "Sep 4", "Sep 11"},
		marks:  []mark{{index: 3, label: "facet source changed"}, {index: 3, label: "fix applied"}},
		suffix: "",
	}
	return ss, o
}

func TestLineChartGolden(t *testing.T) {
	ss, o := sixWeeks()
	got := lineChart(ss, o)
	path := filepath.Join("testdata", "linechart.golden.svg")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("chart differs from %s; rerun with -update if the change is intended\n--- got ---\n%s", path, got)
	}
}

func TestLineChartIsByteIdentical(t *testing.T) {
	ss, o := sixWeeks()
	if lineChart(ss, o) != lineChart(ss, o) {
		t.Error("two renders of the same input differ")
	}
}

func TestLineChartGapBreaksTheLine(t *testing.T) {
	ss, o := sixWeeks()
	got := lineChart(ss[:1], o)
	// Weeks 1-2 and 4-6 are two runs either side of the missing week 3.
	if n := strings.Count(got, "<polyline"); n != 2 {
		t.Errorf("expected the missing week to split the line in two, got %d polylines", n)
	}
	if n := strings.Count(got, "<circle"); n != 5 {
		t.Errorf("expected a marker for each of the 5 present weeks, got %d", n)
	}
	if strings.Contains(got, "Aug 21: ") {
		t.Error("the missing week must not be drawn as a point")
	}
}

func TestLineChartSingleWeek(t *testing.T) {
	got := lineChart([]series{{name: "x", points: []point{{2, true}}}}, chartOpts{title: "x", weeks: []string{"Sep 11"}})
	assertFinite(t, got)
	if strings.Contains(got, "<polyline") || strings.Contains(got, "4-wk mean") {
		t.Error("one week has no line and no previous mean")
	}
	if !strings.Contains(got, `<circle cx="318.0" cy="87.0" r="5"`) {
		t.Errorf("expected the single point centred and emphasised:\n%s", got)
	}
}

func TestLineChartFlatSeries(t *testing.T) {
	pts := []point{{3, true}, {3, true}, {3, true}}
	got := lineChart([]series{{name: "x", points: pts}}, chartOpts{title: "x", weeks: []string{"a", "b", "c"}})
	assertFinite(t, got)
	if !strings.Contains(got, `points="56.0,87.0 318.0,87.0 580.0,87.0"`) {
		t.Errorf("expected a flat line across the middle:\n%s", got)
	}
}

func TestLineChartNoData(t *testing.T) {
	got := lineChart([]series{{name: "x", points: []point{{}, {}}}}, chartOpts{title: "x", weeks: []string{"a", "b"}})
	assertFinite(t, got)
	if strings.Contains(got, "<circle") || !strings.Contains(got, "No value for the latest week.") {
		t.Errorf("an empty series draws axes only:\n%s", got)
	}
}

func TestLineChartEscapesText(t *testing.T) {
	got := lineChart([]series{{name: "<b>", points: []point{{1, true}}}},
		chartOpts{title: "<script>alert(1)</script>", weeks: []string{"<i>"}, marks: []mark{{0, "a & <b>"}}})
	if strings.Contains(got, "<script>") || strings.Contains(got, "<i>") || strings.Contains(got, "a & <b>") {
		t.Errorf("text was not escaped:\n%s", got)
	}
	if !strings.Contains(got, "<title>&lt;script&gt;alert(1)&lt;/script&gt;</title>") {
		t.Error("expected the escaped title")
	}
}

func assertFinite(t *testing.T, got string) {
	t.Helper()
	for _, bad := range []string{"NaN", "Inf"} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains %s:\n%s", bad, got)
		}
	}
}

func TestPrevMeanNoSeries(t *testing.T) {
	if m, ok := prevMean(nil); ok || m != 0 {
		t.Errorf("prevMean(nil) = (%v, %v), want (0, false)", m, ok)
	}
	if m, ok := prevMean([]series{}); ok || m != 0 {
		t.Errorf("prevMean(empty) = (%v, %v), want (0, false)", m, ok)
	}
	got := lineChart(nil, chartOpts{title: "x", weeks: []string{"a", "b"}})
	assertFinite(t, got)
	if strings.Contains(got, "4-wk mean") || strings.Contains(got, "--wi-baseline") {
		t.Errorf("no series means no baseline:\n%s", got)
	}
}
