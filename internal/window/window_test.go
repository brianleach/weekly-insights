package window

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// All fixtures below are synthetic. The tool reads a real cache under
// ~/.claude/usage-data, so tests point Paths.Root at a temp dir instead:
// a test that read the real cache would be non-deterministic and would put
// the user's own session data into CI output.
var windowEnd = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// meta builds a session-meta record that passes the substantive filter unless
// a test deliberately weakens it.
func meta(id, project string, start time.Time) model.SessionMeta {
	return model.SessionMeta{
		SessionID:        id,
		ProjectPath:      project,
		StartTime:        start.Format(time.RFC3339),
		DurationMinutes:  30,
		UserMessageCount: 5,
	}
}

// newStore writes the given metas into a temp usage-data tree.
func newStore(t *testing.T, metas ...model.SessionMeta) store.Paths {
	t.Helper()
	root := t.TempDir()
	p := store.Paths{Root: root, ClaudeHome: filepath.Join(root, "claude")}
	for _, m := range metas {
		writeJSON(t, filepath.Join(p.SessionMeta(), m.SessionID+".json"), m)
	}
	return p
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func ids(ms []model.SessionMeta) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.SessionID
	}
	return out
}

func sessionIDs(ss []model.Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Meta.SessionID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The window is inclusive at both ends, so a session landing exactly on a
// boundary must be kept and one a second beyond it must not.
func TestSelectWindowBoundaries(t *testing.T) {
	start := windowEnd.AddDate(0, 0, -7)
	p := newStore(t,
		meta("on-start", "/repo", start),
		meta("just-inside", "/repo", start.Add(time.Second)),
		meta("just-outside", "/repo", start.Add(-time.Second)),
		meta("on-end", "/repo", windowEnd),
		meta("after-end", "/repo", windowEnd.Add(time.Second)),
	)
	// A record whose timestamp cannot be parsed at all must be dropped, not
	// guessed into the window.
	bad := meta("garbage", "/repo", windowEnd)
	bad.StartTime = "last tuesday"
	writeJSON(t, filepath.Join(p.SessionMeta(), "garbage.json"), bad)

	r, err := Select(p, Options{End: windowEnd})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	got := ids(r.All)
	want := map[string]bool{"on-start": true, "just-inside": true, "on-end": true}
	if len(got) != len(want) {
		t.Fatalf("in-window sessions = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("session %q should not be in the window", id)
		}
	}
	if !r.Start.Equal(start) || !r.End.Equal(windowEnd) {
		t.Errorf("window = %s..%s, want %s..%s", r.Start, r.End, start, windowEnd)
	}
}

func TestSelectDefaultsToSevenDays(t *testing.T) {
	p := newStore(t)
	r, err := Select(p, Options{End: windowEnd})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if d := r.End.Sub(r.Start); d != 7*24*time.Hour {
		t.Errorf("default window = %v, want 168h", d)
	}
}

func TestSelectSubstantiveFilter(t *testing.T) {
	at := windowEnd.Add(-time.Hour)
	short := meta("too-short", "/repo", at)
	short.DurationMinutes = 0.5
	quiet := meta("too-quiet", "/repo", at)
	quiet.UserMessageCount = 1
	edge := meta("edge", "/repo", at)
	edge.DurationMinutes = 1
	edge.UserMessageCount = 2

	p := newStore(t, short, quiet, edge, meta("normal", "/repo", at))
	r, err := Select(p, Options{End: windowEnd})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(r.Kept) != 4 {
		t.Errorf("kept = %v, want all four (the filter must not drop them earlier)", ids(r.Kept))
	}
	got := map[string]bool{}
	for _, s := range r.Substantive {
		got[s.Meta.SessionID] = true
	}
	if len(got) != 2 || !got["edge"] || !got["normal"] {
		t.Errorf("substantive = %v, want edge and normal", sessionIDs(r.Substantive))
	}
}

func TestSelectExclusions(t *testing.T) {
	at := windowEnd.Add(-time.Hour)
	p := newStore(t,
		meta("real", "/Users/x/code/repo", at),
		meta("tmp", "/private/tmp/agent-123", at),
		meta("scratch", "/Users/x/code/repo/scratchpad/run", at),
		meta("worktree", "/Users/x/code/repo/.claude/worktrees/wt1", at),
	)
	cfg := store.DefaultConfig()

	r, err := Select(p, Options{End: windowEnd, Config: cfg})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(r.All) != 4 {
		t.Errorf("All = %v, want every in-window session regardless of exclusion", ids(r.All))
	}
	if !equal(ids(r.Kept), []string{"real"}) {
		t.Errorf("kept = %v, want [real]", ids(r.Kept))
	}
	if r.ExcludedScratch != 3 {
		t.Errorf("ExcludedScratch = %d, want 3", r.ExcludedScratch)
	}

	// IncludeScratch is the audit switch: it must bypass the config entirely.
	r2, err := Select(p, Options{End: windowEnd, Config: cfg, IncludeScratch: true})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(r2.Kept) != 4 || r2.ExcludedScratch != 0 {
		t.Errorf("IncludeScratch kept %d (excluded %d), want 4 (0)", len(r2.Kept), r2.ExcludedScratch)
	}
}

// Canonical facets must win over builtin ones for the same session, since the
// two vocabularies are not comparable and only one of them can be trended.
func TestSelectFacetSourcePreference(t *testing.T) {
	at := windowEnd.Add(-time.Hour)
	p := newStore(t,
		meta("both", "/repo", at),
		meta("builtin-only", "/repo", at.Add(time.Minute)),
		meta("none", "/repo", at.Add(2*time.Minute)),
	)
	writeJSON(t, filepath.Join(p.WeeklyFacets(), "both.json"),
		model.Facets{SessionID: "both", UnderlyingGoal: "fix_bug"})
	writeJSON(t, filepath.Join(p.BuiltinFacets(), "both.json"),
		model.Facets{SessionID: "both", UnderlyingGoal: "ui_bug_fix"})
	writeJSON(t, filepath.Join(p.BuiltinFacets(), "builtin-only.json"),
		model.Facets{SessionID: "builtin-only", UnderlyingGoal: "ci_debugging"})

	r, err := Select(p, Options{End: windowEnd})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Ordering is by start time ascending, which the fixtures stagger.
	if !equal(sessionIDs(r.Substantive), []string{"both", "builtin-only", "none"}) {
		t.Fatalf("substantive order = %v, want ascending by start", sessionIDs(r.Substantive))
	}
	byID := map[string]model.Session{}
	for _, s := range r.Substantive {
		byID[s.Meta.SessionID] = s
	}
	if got := byID["both"]; got.FacetSource != store.SourceCanonical ||
		got.Facets == nil || got.Facets.UnderlyingGoal != "fix_bug" {
		t.Errorf("both: source=%q facets=%+v, want canonical fix_bug", got.FacetSource, got.Facets)
	}
	if got := byID["builtin-only"]; got.FacetSource != store.SourceBuiltin || got.Facets == nil {
		t.Errorf("builtin-only: source=%q facets=%+v, want builtin", got.FacetSource, got.Facets)
	}
	if got := byID["none"]; got.Facets != nil || got.FacetSource != "" {
		t.Errorf("none: source=%q facets=%+v, want empty", got.FacetSource, got.Facets)
	}
}

func TestNeedsExtraction(t *testing.T) {
	cases := []struct {
		name string
		s    model.Session
		want bool
	}{
		{"no facets", model.Session{}, true},
		{"builtin facets are re-extracted", model.Session{
			Facets: &model.Facets{}, FacetSource: store.SourceBuiltin}, true},
		{"canonical facets are done", model.Session{
			Facets: &model.Facets{}, FacetSource: store.SourceCanonical}, false},
	}
	for _, c := range cases {
		if got := NeedsExtraction(c.s); got != c.want {
			t.Errorf("%s: NeedsExtraction = %v, want %v", c.name, got, c.want)
		}
	}
}

// The cache has been written with and without a zone offset over time, so both
// forms must land in the same window.
func TestParseStartFormats(t *testing.T) {
	want := time.Date(2026, 9, 8, 15, 4, 5, 0, time.UTC)
	for _, s := range []string{
		"2026-09-08T15:04:05Z",
		"2026-09-08T15:04:05.000Z",
		"2026-09-08T10:04:05-05:00",
		"2026-09-08T15:04:05",
	} {
		got, ok := parseStart(s)
		if !ok || !got.Equal(want) {
			t.Errorf("parseStart(%q) = %v, %v; want %v", s, got, ok, want)
		}
	}
	if _, ok := parseStart(""); ok {
		t.Error("parseStart(\"\") should fail")
	}
	if _, ok := parseStart("not a time"); ok {
		t.Error("parseStart(garbage) should fail")
	}
}
