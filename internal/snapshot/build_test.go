package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/taxonomy"
	"github.com/brianleach/weekly-insights/internal/window"
)

func testWindow(sessions []model.Session) window.Result {
	end := time.Date(2020, 3, 8, 12, 0, 0, 0, time.UTC)
	metas := make([]model.SessionMeta, 0, len(sessions))
	for _, s := range sessions {
		metas = append(metas, s.Meta)
	}
	return window.Result{
		Start:       end.AddDate(0, 0, -7),
		End:         end,
		All:         metas,
		Kept:        metas,
		Substantive: sessions,
	}
}

func TestBuildVolumeAndWindow(t *testing.T) {
	r := testWindow([]model.Session{
		{Meta: model.SessionMeta{
			SessionID: "aaaaaaaa-1111-2222-3333-444444444444", ProjectPath: "/w/alpha",
			StartTime: "2020-03-02T09:00:00Z", DurationMinutes: 90, UserMessageCount: 10,
			GitCommits: 2, GitPushes: 1, LinesAdded: 100, LinesRemoved: 20, FilesModified: 5,
			InputTokens: 1000, OutputTokens: 200, UserInterruptions: 3, ToolErrors: 2,
			UserResponseTimes: []float64{10, 20, 30},
			ToolCounts:        map[string]int{"Read": 4, "Bash": 1},
		}},
		{Meta: model.SessionMeta{
			SessionID: "bbbbbbbb", ProjectPath: "/w/beta",
			StartTime: "2020-03-02T18:00:00Z", DurationMinutes: 30, UserMessageCount: 5,
			UserInterruptions: 1, ToolErrors: 0,
			UserResponseTimes: []float64{40},
			ToolCounts:        map[string]int{"Read": 1},
		}},
		{Meta: model.SessionMeta{
			SessionID: "cccccccc", ProjectPath: "/w/alpha",
			StartTime: "2020-03-05T08:00:00Z", DurationMinutes: 45, UserMessageCount: 1,
		}},
	})

	s := Build(r, 7, nil)

	if s.Window.Label != "2020-03-08" {
		t.Errorf("label = %q, want 2020-03-08", s.Window.Label)
	}
	if s.Window.Days != 7 || s.Window.Start != "2020-03-01T12:00:00Z" {
		t.Errorf("window = %+v", s.Window)
	}
	if _, err := time.Parse(time.RFC3339, s.GeneratedAt); err != nil {
		t.Errorf("generated_at %q not RFC3339: %v", s.GeneratedAt, err)
	}
	if s.Volume.Messages != 16 {
		t.Errorf("messages = %d, want 16", s.Volume.Messages)
	}
	// (90+30+45)/60 = 2.75 -> 2.8 at one decimal place.
	if s.Volume.Hours != 2.8 {
		t.Errorf("hours = %v, want 2.8", s.Volume.Hours)
	}
	if s.Volume.DaysActive != 2 {
		t.Errorf("days_active = %d, want 2", s.Volume.DaysActive)
	}
	// Pooled response times 10,20,30,40 -> median 25.
	if s.Volume.MedianResponseSeconds != 25 {
		t.Errorf("median = %v, want 25", s.Volume.MedianResponseSeconds)
	}
	if s.Volume.Commits != 2 || s.Volume.Pushes != 1 || s.Volume.LinesAdded != 100 ||
		s.Volume.LinesRemoved != 20 || s.Volume.FilesModified != 5 ||
		s.Volume.InputTokens != 1000 || s.Volume.OutputTokens != 200 {
		t.Errorf("volume counters = %+v", s.Volume)
	}
	if s.Tools["Read"] != 5 || s.Tools["Bash"] != 1 {
		t.Errorf("tools = %v", s.Tools)
	}
	if s.Projects["/w/alpha"] != 2 || s.Projects["/w/beta"] != 1 {
		t.Errorf("projects = %v", s.Projects)
	}
	// 3 sessions over 2 active days.
	if s.Rates[RateSessionsPerDay] != 1.5 {
		t.Errorf("sessions per day = %v, want 1.5", s.Rates[RateSessionsPerDay])
	}
	if s.Rates[RateInterruptions] != 1.33 {
		t.Errorf("interruptions per session = %v, want 1.33", s.Rates[RateInterruptions])
	}
	if s.Rates[RateToolErrors] != 0.67 {
		t.Errorf("tool errors per session = %v, want 0.67", s.Rates[RateToolErrors])
	}
}

func TestBuildCollapsesFreeFormLabels(t *testing.T) {
	r := testWindow([]model.Session{
		{
			Meta: model.SessionMeta{SessionID: "aaaaaaaa11", StartTime: "2020-03-02T09:00:00Z"},
			Facets: &model.Facets{
				GoalCategories: map[string]int{"bug_fix_implementation": 2, "ui_bug_fix": 1},
				FrictionCounts: map[string]int{"hallucinated_api": 1},
				PrimarySuccess: "correct_code_edits",
				Outcome:        "fully_achieved",
				SessionType:    "single_task",
				Helpfulness:    "essential",
				BriefSummary:   "x",
			},
			FacetSource: store.SourceCanonical,
		},
		{
			Meta: model.SessionMeta{SessionID: "bbbbbbbb22", StartTime: "2020-03-03T09:00:00Z"},
			Facets: &model.Facets{
				GoalCategories: map[string]int{"bug_fixing": 3, "dropped_label": 0},
				FrictionCounts: map[string]int{"unverified_claim": 2},
				Outcome:        "mostly_achieved",
			},
			FacetSource: store.SourceBuiltin,
		},
	})

	collapses := map[string]map[string]bool{}
	s := Build(r, 7, collapses)

	if got := s.Goals[taxonomy.GoalFixBug]; got != 6 {
		t.Errorf("fix_bug = %d, want 6 (all three free-form spellings summed)", got)
	}
	if _, ok := s.Goals["dropped_label"]; ok {
		t.Errorf("zero-valued label should not be counted: %v", s.Goals)
	}
	if got := s.Friction[taxonomy.FrictionUnverifiedClaim]; got != 3 {
		t.Errorf("unverified_claim = %d, want 3", got)
	}
	// Collapse keys are kind-prefixed so goal "other" and friction "other"
	// stay distinct in --explain output.
	raws := collapses["goal:"+taxonomy.GoalFixBug]
	for _, want := range []string{"bug_fix_implementation", "ui_bug_fix", "bug_fixing"} {
		if !raws[want] {
			t.Errorf("collapse record missing raw label %q: %v", want, raws)
		}
	}
	if s.Sessions.FacetSource[store.SourceCanonical] != 1 || s.Sessions.FacetSource[store.SourceBuiltin] != 1 {
		t.Errorf("facet_source = %v", s.Sessions.FacetSource)
	}
	// Both sessions have facets and both achieved.
	if s.Rates[RateAchievedPct] != 100 {
		t.Errorf("achieved_pct = %v, want 100", s.Rates[RateAchievedPct])
	}
	if s.Success["correct_code_edits"] != 1 || len(s.Success) != 1 {
		t.Errorf("success = %v", s.Success)
	}
	// The second session has no primary_success, so it falls back to "none"
	// and must be skipped rather than recorded.
	if _, ok := s.Success["none"]; ok {
		t.Errorf("none should not be recorded as a success: %v", s.Success)
	}
	if s.Outcomes["fully_achieved"] != 1 || s.Outcomes["mostly_achieved"] != 1 {
		t.Errorf("outcomes = %v", s.Outcomes)
	}
	// Missing enums fall back rather than dropping the session.
	if s.Helpfulness["very_helpful"] != 1 || s.SessionTypes["multi_task"] != 1 {
		t.Errorf("helpfulness=%v session_types=%v", s.Helpfulness, s.SessionTypes)
	}
}

func TestBuildDenominatorSplit(t *testing.T) {
	// Four substantive sessions, only two with facets. Facet-derived rates must
	// divide by 2, meta-derived rates by 4.
	sessions := []model.Session{
		{
			Meta: model.SessionMeta{SessionID: "s1", StartTime: "2020-03-02T09:00:00Z", UserInterruptions: 2, ToolErrors: 4},
			Facets: &model.Facets{
				FrictionCounts: map[string]int{"unverified_claim": 1, "unwanted_autonomous_action": 2, "ignored_stated_preference": 1},
				Outcome:        "fully_achieved",
			},
			FacetSource: store.SourceCanonical,
		},
		{
			Meta:        model.SessionMeta{SessionID: "s2", StartTime: "2020-03-02T10:00:00Z", UserInterruptions: 2, ToolErrors: 4},
			Facets:      &model.Facets{Outcome: "not_achieved"},
			FacetSource: store.SourceCanonical,
		},
		{Meta: model.SessionMeta{SessionID: "s3", StartTime: "2020-03-02T11:00:00Z"}},
		{Meta: model.SessionMeta{SessionID: "s4", StartTime: "2020-03-02T12:00:00Z"}},
	}
	s := Build(testWindow(sessions), 7, nil)

	if s.Sessions.Substantive != 4 || s.Sessions.WithFacets != 2 {
		t.Fatalf("sessions = %+v", s.Sessions)
	}
	if s.Rates[RateFrictionPerSession] != 2 {
		t.Errorf("friction per session = %v, want 2 (4 events / 2 with facets)", s.Rates[RateFrictionPerSession])
	}
	if s.Rates[RateUnverifiedClaim] != 0.5 || s.Rates[RateUnsanctionedAction] != 1 || s.Rates[RateIgnoredPreference] != 0.5 {
		t.Errorf("named friction rates = %v", s.Rates)
	}
	if s.Rates[RateAchievedPct] != 50 {
		t.Errorf("achieved_pct = %v, want 50", s.Rates[RateAchievedPct])
	}
	if s.Rates[RateFacetCoveragePct] != 50 {
		t.Errorf("facet_coverage_pct = %v, want 50", s.Rates[RateFacetCoveragePct])
	}
	if s.Rates[RateInterruptions] != 1 || s.Rates[RateToolErrors] != 2 {
		t.Errorf("meta-derived rates = %v", s.Rates)
	}
}

func TestBuildNegativeSatisfactionPct(t *testing.T) {
	s := Build(testWindow([]model.Session{
		{
			Meta: model.SessionMeta{SessionID: "s1", StartTime: "2020-03-02T09:00:00Z"},
			Facets: &model.Facets{Satisfaction: map[string]int{
				"happy": 5, "frustrated": 2, "dissatisfied": 1, "thrilled": 0,
			}},
			FacetSource: store.SourceCanonical,
		},
		{
			Meta:        model.SessionMeta{SessionID: "s2", StartTime: "2020-03-02T09:00:00Z"},
			Facets:      &model.Facets{Satisfaction: map[string]int{"pleased": 2}},
			FacetSource: store.SourceCanonical,
		},
	}), 7, nil)

	// pleased collapses into happy, so 7 happy + 2 frustrated + 1 dissatisfied.
	if s.Satisfaction["happy"] != 7 {
		t.Errorf("satisfaction = %v", s.Satisfaction)
	}
	if _, ok := s.Satisfaction["delighted"]; ok {
		t.Errorf("zero-valued satisfaction should not be counted: %v", s.Satisfaction)
	}
	if s.Rates[RateNegativePct] != 30 {
		t.Errorf("negative pct = %v, want 30", s.Rates[RateNegativePct])
	}
}

func TestBuildEmptyWindowIsSafe(t *testing.T) {
	s := Build(testWindow(nil), 7, map[string]map[string]bool{})

	for _, k := range []string{
		RateSessionsPerDay, RateFrictionPerSession, RateNegativePct, RateInterruptions,
		RateToolErrors, RateAchievedPct, RateFacetCoveragePct, RateUnverifiedClaim,
		RateUnsanctionedAction, RateIgnoredPreference,
	} {
		v, ok := s.Rates[k]
		if !ok {
			t.Errorf("rate %s missing", k)
			continue
		}
		if v != 0 {
			t.Errorf("rate %s = %v, want 0 on an empty window", k, v)
		}
	}
	if s.Volume.MedianResponseSeconds != 0 || s.Volume.Hours != 0 || s.Volume.DaysActive != 0 {
		t.Errorf("volume = %+v", s.Volume)
	}
	if s.FrictionDetails == nil || s.UserCorrections == nil {
		t.Errorf("slices should be non-nil so the JSON carries [] rather than null")
	}
}

func TestBuildDetailsAndCorrections(t *testing.T) {
	long := ""
	for len(long) < 200 {
		long += "abcdefghij"
	}
	quotes := []string{"one", "two", "three", "four", "five", "six", "seven", "  ", long}

	s := Build(testWindow([]model.Session{
		{
			Meta: model.SessionMeta{
				SessionID: "abcdefgh-ijkl", ProjectPath: "/Users/x/code/weekly-insights",
				StartTime: "2020-03-02T09:00:00Z",
			},
			Facets: &model.Facets{
				FrictionDetail:  "  claimed the tests passed without running them  ",
				UserCorrections: quotes,
			},
			FacetSource: store.SourceCanonical,
		},
		{
			Meta:        model.SessionMeta{SessionID: "zzzzzzzz", StartTime: "2020-03-03T09:00:00Z"},
			Facets:      &model.Facets{FrictionDetail: "   "},
			FacetSource: store.SourceCanonical,
		},
	}), 7, nil)

	if len(s.FrictionDetails) != 1 {
		t.Fatalf("friction details = %+v, want only the non-empty one", s.FrictionDetails)
	}
	d := s.FrictionDetails[0]
	if d.Session != "abcdefgh" || d.Date != "2020-03-02" || d.Project != "weekly-insights" ||
		d.Detail != "claimed the tests passed without running them" {
		t.Errorf("friction detail = %+v", d)
	}
	if len(s.UserCorrections) != 6 {
		t.Fatalf("user corrections = %d, want 6 (per-session cap)", len(s.UserCorrections))
	}
	for _, c := range s.UserCorrections {
		if c.Session != "abcdefgh" || c.Date != "2020-03-02" {
			t.Errorf("correction = %+v", c)
		}
		if len(c.Quote) > 160 {
			t.Errorf("quote not trimmed: %d chars", len(c.Quote))
		}
	}
}

func TestSaveLoadListRoundTrip(t *testing.T) {
	p := store.Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}

	older := Build(testWindow([]model.Session{
		{Meta: model.SessionMeta{SessionID: "s1", ProjectPath: "/w/a", StartTime: "2020-03-01T09:00:00Z", UserMessageCount: 3}},
	}), 7, nil)
	older.Window.Label = "2020-03-01"
	newer := Build(testWindow([]model.Session{
		{
			Meta:        model.SessionMeta{SessionID: "s2", ProjectPath: "/w/b", StartTime: "2020-03-07T09:00:00Z", UserMessageCount: 9},
			Facets:      &model.Facets{GoalCategories: map[string]int{"bug_fixing": 2}, FrictionDetail: "slow"},
			FacetSource: store.SourceCanonical,
		},
	}), 7, nil)

	path, err := Save(p, newer)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := filepath.Join(p.Snapshots(), "2020-03-08.json"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if _, err := Save(p, older); err != nil {
		t.Fatalf("Save older: %v", err)
	}

	got, err := Load(p, "2020-03-08")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Volume.Messages != 9 || got.Goals[taxonomy.GoalFixBug] != 2 ||
		len(got.FrictionDetails) != 1 || got.Window.Label != "2020-03-08" ||
		got.Rates[RateFacetCoveragePct] != 100 {
		t.Errorf("round trip lost data: %+v", got)
	}

	if _, err := Load(p, "1999-01-01"); err == nil {
		t.Errorf("Load of a missing snapshot should error")
	}

	list, err := List(p)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Window.Label != "2020-03-01" || list[1].Window.Label != "2020-03-08" {
		t.Errorf("list not sorted ascending: %+v", list)
	}
}

// buildFor makes a minimal snapshot for a given label and window length, so
// the filename tests are not entangled with aggregation behaviour.
func buildFor(label string, days int, messages int) Snapshot {
	s := Build(testWindow([]model.Session{
		{Meta: model.SessionMeta{SessionID: "s1", ProjectPath: "/w/a", StartTime: "2020-03-02T09:00:00Z", UserMessageCount: messages}},
	}), days, nil)
	s.Window.Label = label
	return s
}

func TestSaveSeparatesWindowLengthsEndingOnTheSameDate(t *testing.T) {
	p := store.Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}

	weekly, err := Save(p, buildFor("2020-03-08", 7, 7))
	if err != nil {
		t.Fatalf("Save 7d: %v", err)
	}
	monthly, err := Save(p, buildFor("2020-03-08", 30, 30))
	if err != nil {
		t.Fatalf("Save 30d: %v", err)
	}

	if want := filepath.Join(p.Snapshots(), "2020-03-08.json"); weekly != want {
		t.Errorf("7-day path = %q, want %q (the legacy unsuffixed name)", weekly, want)
	}
	if want := filepath.Join(p.Snapshots(), "2020-03-08-30d.json"); monthly != want {
		t.Errorf("30-day path = %q, want %q", monthly, want)
	}

	entries, err := filepath.Glob(filepath.Join(p.Snapshots(), "*.json"))
	if err != nil {
		t.Fatalf("globbing: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 files on disk, got %v", entries)
	}

	// Neither overwrote the other.
	got, err := LoadWindow(p, "2020-03-08", 7)
	if err != nil {
		t.Fatalf("LoadWindow 7d: %v", err)
	}
	if got.Window.Days != 7 || got.Volume.Messages != 7 {
		t.Errorf("7-day snapshot = %+v", got.Window)
	}
	got, err = LoadWindow(p, "2020-03-08", 30)
	if err != nil {
		t.Fatalf("LoadWindow 30d: %v", err)
	}
	if got.Window.Days != 30 || got.Volume.Messages != 30 {
		t.Errorf("30-day snapshot = %+v", got.Window)
	}
}

func TestListReturnsEveryWindowLengthOrderedByLabelThenDays(t *testing.T) {
	p := store.Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}
	for _, s := range []Snapshot{
		buildFor("2020-03-08", 30, 30),
		buildFor("2020-03-08", 7, 7),
		buildFor("2020-03-01", 7, 1),
	} {
		if _, err := Save(p, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	list, err := List(p)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 snapshots, got %d", len(list))
	}
	type key struct {
		label string
		days  int
	}
	want := []key{{"2020-03-01", 7}, {"2020-03-08", 7}, {"2020-03-08", 30}}
	for i, w := range want {
		if list[i].Window.Label != w.label || list[i].Window.Days != w.days {
			t.Errorf("list[%d] = %s/%dd, want %s/%dd", i, list[i].Window.Label, list[i].Window.Days, w.label, w.days)
		}
	}
}

func TestLoadFallsBackToAnotherWindowLength(t *testing.T) {
	p := store.Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}
	if _, err := Save(p, buildFor("2020-03-08", 30, 30)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(p, "2020-03-08")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Window.Days != 30 {
		t.Errorf("days = %d, want the 30-day snapshot", got.Window.Days)
	}

	// The 7-day form wins once it exists.
	if _, err := Save(p, buildFor("2020-03-08", 7, 7)); err != nil {
		t.Fatalf("Save 7d: %v", err)
	}
	got, err = Load(p, "2020-03-08")
	if err != nil {
		t.Fatalf("Load after 7d save: %v", err)
	}
	if got.Window.Days != 7 {
		t.Errorf("days = %d, want the 7-day snapshot to take precedence", got.Window.Days)
	}
	if _, err := Load(p, "1999-01-01"); err == nil {
		t.Error("Load of a missing label should error")
	}
}

func TestFileNameScheme(t *testing.T) {
	for _, c := range []struct {
		days int
		want string
	}{{7, "2020-03-08.json"}, {0, "2020-03-08.json"}, {-1, "2020-03-08.json"}, {30, "2020-03-08-30d.json"}, {1, "2020-03-08-1d.json"}} {
		if got := FileName("2020-03-08", c.days); got != c.want {
			t.Errorf("FileName(%d) = %q, want %q", c.days, got, c.want)
		}
	}
}

func TestSaveWritesOwnerOnlyPermissions(t *testing.T) {
	p := store.Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}
	path, err := Save(p, buildFor("2020-03-08", 7, 1))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %v, want 0600; snapshots hold verbatim corrections", fi.Mode().Perm())
	}
	di, err := os.Stat(p.Snapshots())
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("snapshot dir mode = %v, want 0700", di.Mode().Perm())
	}

	// A file left behind by an older version is tightened on overwrite.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := Save(p, buildFor("2020-03-08", 7, 2)); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode after overwrite = %v, want 0600", fi.Mode().Perm())
	}
}
