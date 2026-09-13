package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clean is a facet record that already satisfies every check. Tests mutate a
// copy of it so each case differs from a known-good baseline in exactly one way.
// All content here is synthetic; no real session data appears in this package.
func clean(sid string) map[string]any {
	return map[string]any{
		"session_id":               sid,
		"underlying_goal":          "make the thing work",
		"goal_categories":          map[string]any{"fix_bug": 2},
		"outcome":                  "fully_achieved",
		"user_satisfaction_counts": map[string]any{"happy": 1},
		"claude_helpfulness":       "very_helpful",
		"session_type":             "single_task",
		"friction_counts":          map[string]any{"tool_failed": 1},
		"primary_success":          "correct_code_edits",
		"brief_summary":            "did the thing",
	}
}

func writeFacet(t *testing.T, dir, sid string, v any) string {
	t.Helper()
	path := filepath.Join(dir, sid+".json")
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshaling fixture: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func readFacet(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return m
}

// dirWith stages a single facet file in a fresh temp dir.
func dirWith(t *testing.T, sid string, f any) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, writeFacet(t, dir, sid, f)
}

func messages(res Result) string {
	var sb strings.Builder
	for _, p := range res.Problems {
		sb.WriteString(p.File + ": " + p.Message + "\n")
	}
	return sb.String()
}

func hasProblem(res Result, substr string) bool {
	for _, p := range res.Problems {
		if strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

func TestCleanFileHasNoProblemsAndIsNotRewritten(t *testing.T) {
	dir, path := dirWith(t, "sess-clean", clean("sess-clean"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
	if len(res.Problems) != 0 {
		t.Errorf("want no problems, got:\n%s", messages(res))
	}
	if len(res.Fixed) != 0 {
		t.Errorf("clean file should not be rewritten, Fixed = %v", res.Fixed)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading fixture: %v", err)
	}
	if string(before) != string(after) {
		t.Error("clean file was rewritten byte-for-byte differently")
	}
}

func TestEmptyDirIsNotAnError(t *testing.T) {
	res, err := Dir(t.TempDir(), false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if res.Checked != 0 || len(res.Problems) != 0 {
		t.Errorf("want empty result, got %+v", res)
	}
}

func TestOffVocabularyLabelIsReportedAndNormalized(t *testing.T) {
	f := clean("sess-offvocab")
	f["goal_categories"] = map[string]any{"bug_fixing": 3}
	dir, path := dirWith(t, "sess-offvocab", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "goal_categories: off-vocabulary label 'bug_fixing' -> 'fix_bug'") {
		t.Errorf("want off-vocabulary problem, got:\n%s", messages(res))
	}
	if len(res.Fixed) != 0 {
		t.Errorf("dry run must not rewrite, Fixed = %v", res.Fixed)
	}

	res, err = Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(res.Fixed) != 1 {
		t.Fatalf("want 1 fixed file, got %v", res.Fixed)
	}
	got := readFacet(t, path)["goal_categories"].(map[string]any)
	if len(got) != 1 || got["fix_bug"] != float64(3) {
		t.Errorf("goal_categories = %v, want {fix_bug: 3}", got)
	}
}

func TestCollapsingLabelsSumRatherThanOverwrite(t *testing.T) {
	f := clean("sess-collapse")
	// Both normalize to fix_bug; the counts must add up to 5, not land on 2 or 3.
	f["goal_categories"] = map[string]any{"bug_fixing": 2, "hotfix_work": 3}
	dir, path := dirWith(t, "sess-collapse", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(res.Problems) != 2 {
		t.Errorf("want 2 off-vocabulary problems, got:\n%s", messages(res))
	}
	got := readFacet(t, path)["goal_categories"].(map[string]any)
	if len(got) != 1 {
		t.Fatalf("want a single collapsed key, got %v", got)
	}
	if got["fix_bug"] != float64(5) {
		t.Errorf("fix_bug = %v, want 5 (summed, not overwritten)", got["fix_bug"])
	}
}

func TestFrictionAndSatisfactionLabelsAreValidated(t *testing.T) {
	f := clean("sess-labels")
	f["friction_counts"] = map[string]any{"stale_context": 1}
	f["user_satisfaction_counts"] = map[string]any{"thrilled": 2}
	dir, path := dirWith(t, "sess-labels", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if !hasProblem(res, "friction_counts: off-vocabulary label 'stale_context' -> 'context_loss'") {
		t.Errorf("want friction problem, got:\n%s", messages(res))
	}
	if !hasProblem(res, "user_satisfaction_counts: off-vocabulary label 'thrilled' -> 'delighted'") {
		t.Errorf("want satisfaction problem, got:\n%s", messages(res))
	}
	out := readFacet(t, path)
	if got := out["friction_counts"].(map[string]any)["context_loss"]; got != float64(1) {
		t.Errorf("context_loss = %v, want 1", got)
	}
	if got := out["user_satisfaction_counts"].(map[string]any)["delighted"]; got != float64(2) {
		t.Errorf("delighted = %v, want 2", got)
	}
}

func TestSessionIDMismatchIsReportedAndFixed(t *testing.T) {
	f := clean("sess-id")
	f["session_id"] = "some-other-id"
	dir, path := dirWith(t, "sess-id", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "session_id mismatch (some-other-id != sess-id)") {
		t.Errorf("want session_id problem, got:\n%s", messages(res))
	}

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if got := readFacet(t, path)["session_id"]; got != "sess-id" {
		t.Errorf("session_id = %v, want sess-id", got)
	}
}

func TestMissingFieldIsReported(t *testing.T) {
	f := clean("sess-missing")
	delete(f, "brief_summary")
	dir, _ := dirWith(t, "sess-missing", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "missing field: brief_summary") {
		t.Errorf("want missing-field problem, got:\n%s", messages(res))
	}
}

func TestInvalidEnumIsReportedAndFixedToFallback(t *testing.T) {
	f := clean("sess-enum")
	f["outcome"] = "went_great"
	f["session_type"] = "sprawling"
	dir, path := dirWith(t, "sess-enum", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "outcome: invalid value 'went_great'") {
		t.Errorf("want outcome problem, got:\n%s", messages(res))
	}

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	out := readFacet(t, path)
	if out["outcome"] != "unclear_from_transcript" {
		t.Errorf("outcome = %v, want unclear_from_transcript", out["outcome"])
	}
	if out["session_type"] != "multi_task" {
		t.Errorf("session_type = %v, want multi_task", out["session_type"])
	}
}

func TestNonNumericCountIsReportedAndDroppedOnFix(t *testing.T) {
	f := clean("sess-nonnum")
	f["friction_counts"] = map[string]any{"tool_failed": "several"}
	dir, path := dirWith(t, "sess-nonnum", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "friction_counts.tool_failed is not a number") {
		t.Errorf("want non-numeric problem, got:\n%s", messages(res))
	}

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if got := readFacet(t, path)["friction_counts"].(map[string]any); len(got) != 0 {
		t.Errorf("friction_counts = %v, want the unusable entry dropped", got)
	}
}

func TestCountFieldThatIsNotAnObject(t *testing.T) {
	f := clean("sess-notobj")
	f["goal_categories"] = []any{"fix_bug"}
	dir, _ := dirWith(t, "sess-notobj", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if !hasProblem(res, "goal_categories is not an object") {
		t.Errorf("want shape problem, got:\n%s", messages(res))
	}
}

func TestMalformedJSONIsReportedAndNeverFixed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-broken.json")
	raw := `{"session_id": "sess-broken", `
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if !hasProblem(res, "unparseable JSON") {
		t.Errorf("want unparseable problem, got:\n%s", messages(res))
	}
	if len(res.Fixed) != 0 {
		t.Errorf("malformed file must not be rewritten, Fixed = %v", res.Fixed)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading fixture: %v", err)
	}
	if string(b) != raw {
		t.Error("malformed file was modified")
	}
}

func TestTopLevelNonObjectIsReportedAndNeverFixed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-array.json")
	if err := os.WriteFile(path, []byte(`["not", "a", "facet"]`), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if !hasProblem(res, "not a JSON object") {
		t.Errorf("want non-object problem, got:\n%s", messages(res))
	}
	if len(res.Fixed) != 0 {
		t.Errorf("non-object file must not be rewritten, Fixed = %v", res.Fixed)
	}
}

func TestUnknownFieldsSurviveAFix(t *testing.T) {
	f := clean("sess-unknown")
	f["outcome"] = "went_great"
	f["user_corrections"] = []any{"use the other helper"}
	f["friction_detail"] = "retried twice"
	f["some_future_field"] = map[string]any{"nested": true}
	dir, path := dirWith(t, "sess-unknown", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(res.Fixed) != 1 {
		t.Fatalf("want the file rewritten, Fixed = %v", res.Fixed)
	}
	out := readFacet(t, path)
	if got := out["user_corrections"].([]any); len(got) != 1 || got[0] != "use the other helper" {
		t.Errorf("user_corrections = %v, want preserved", out["user_corrections"])
	}
	if out["friction_detail"] != "retried twice" {
		t.Errorf("friction_detail = %v, want preserved", out["friction_detail"])
	}
	if got := out["some_future_field"].(map[string]any); got["nested"] != true {
		t.Errorf("some_future_field = %v, want preserved", out["some_future_field"])
	}
	if out["underlying_goal"] != "make the thing work" {
		t.Errorf("underlying_goal = %v, want preserved", out["underlying_goal"])
	}
}

func TestIntegerCountsStayIntegersAcrossAFix(t *testing.T) {
	f := clean("sess-ints")
	f["goal_categories"] = map[string]any{"bug_fixing": 2}
	dir, path := dirWith(t, "sess-ints", f)

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(b), `"fix_bug": 2`) {
		t.Errorf("want an integer count on disk, got:\n%s", b)
	}
}

func TestFixIsIdempotentAndSecondRunIsClean(t *testing.T) {
	dir := t.TempDir()

	bad := clean("sess-a")
	bad["session_id"] = "wrong"
	bad["goal_categories"] = map[string]any{"bug_fixing": 2, "hotfix_work": 3}
	bad["friction_counts"] = map[string]any{"stale_context": 1, "tool_error": "lots"}
	bad["user_satisfaction_counts"] = map[string]any{"pleased": 1}
	bad["outcome"] = "went_great"
	bad["claude_helpfulness"] = "kind of"
	bad["primary_success"] = "vibes"
	writeFacet(t, dir, "sess-a", bad)
	writeFacet(t, dir, "sess-b", clean("sess-b"))

	first, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(first.Problems) == 0 {
		t.Fatal("want problems on the first pass")
	}
	if len(first.Fixed) != 1 || first.Fixed[0] != "sess-a.json" {
		t.Errorf("Fixed = %v, want only sess-a.json", first.Fixed)
	}

	second, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix) second pass: %v", err)
	}
	if len(second.Problems) != 0 {
		t.Errorf("want a clean second pass, got:\n%s", messages(second))
	}
	if len(second.Fixed) != 0 {
		t.Errorf("second pass should rewrite nothing, Fixed = %v", second.Fixed)
	}
	if second.Checked != 2 {
		t.Errorf("Checked = %d, want 2", second.Checked)
	}
}

func TestProblemsAreAttributedToTheirFile(t *testing.T) {
	dir := t.TempDir()
	a := clean("sess-a")
	a["outcome"] = "nope"
	writeFacet(t, dir, "sess-a", a)
	writeFacet(t, dir, "sess-b", clean("sess-b"))

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(res.Problems) != 1 {
		t.Fatalf("want 1 problem, got:\n%s", messages(res))
	}
	if res.Problems[0].File != "sess-a.json" {
		t.Errorf("File = %q, want sess-a.json", res.Problems[0].File)
	}
}

func unresolved(res Result) string {
	var sb strings.Builder
	for _, p := range res.Unresolved {
		sb.WriteString(p.File + ": " + p.Message + "\n")
	}
	return sb.String()
}

func hasUnresolved(res Result, substr string) bool {
	for _, p := range res.Unresolved {
		if strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

func TestUnresolvedReportsWhatFixCouldNotRepair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sess-broken.json"), []byte(`{"session_id": "sess-broken", `), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	missing := clean("sess-missing")
	delete(missing, "brief_summary")
	writeFacet(t, dir, "sess-missing", missing)
	notObj := clean("sess-notobj")
	notObj["goal_categories"] = []any{"fix_bug"}
	writeFacet(t, dir, "sess-notobj", notObj)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if !hasUnresolved(res, "unparseable JSON") {
		t.Errorf("malformed file should stay unresolved, got:\n%s", unresolved(res))
	}
	if !hasUnresolved(res, "missing field: brief_summary") {
		t.Errorf("missing field should stay unresolved, got:\n%s", unresolved(res))
	}
	if !hasUnresolved(res, "goal_categories is not an object") {
		t.Errorf("non-object count field should stay unresolved, got:\n%s", unresolved(res))
	}
	for _, p := range res.Unresolved {
		if p.File == "" {
			t.Errorf("unresolved problem not attributed to a file: %+v", p)
		}
	}
}

func TestUnresolvedIsEmptyWhenFixRepairsEverything(t *testing.T) {
	f := clean("sess-fixable")
	f["goal_categories"] = map[string]any{"bug_fixing": 3}
	f["friction_counts"] = map[string]any{"stale_context": 1}
	dir, _ := dirWith(t, "sess-fixable", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(res.Problems) == 0 {
		t.Fatal("want off-vocabulary problems on the fix pass")
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("everything was repairable, want no unresolved, got:\n%s", unresolved(res))
	}

	second, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir second run: %v", err)
	}
	if len(second.Problems) != 0 {
		t.Errorf("second run should be clean, got:\n%s", messages(second))
	}
}

func TestUnresolvedIsEmptyOnADryRun(t *testing.T) {
	f := clean("sess-dry")
	delete(f, "brief_summary")
	dir, _ := dirWith(t, "sess-dry", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if len(res.Problems) == 0 {
		t.Fatal("want the missing field reported through Problems")
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("a dry run reports through Problems only, got:\n%s", unresolved(res))
	}
}

func TestFractionalCountIsReportedAndDropped(t *testing.T) {
	f := clean("sess-frac")
	f["goal_categories"] = map[string]any{"fix_bug": 1.5}
	dir, path := dirWith(t, "sess-frac", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "goal_categories.fix_bug is not a whole number (1.5)") {
		t.Errorf("want a fractional-count problem, got:\n%s", messages(res))
	}

	res, err = Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if got := readFacet(t, path)["goal_categories"].(map[string]any); len(got) != 0 {
		t.Errorf("goal_categories = %v, want the fractional entry dropped", got)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("dropping the entry resolves it, got:\n%s", unresolved(res))
	}
}

func TestNegativeCountIsReportedAndDropped(t *testing.T) {
	f := clean("sess-neg")
	f["friction_counts"] = map[string]any{"tool_failed": -1}
	dir, path := dirWith(t, "sess-neg", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "friction_counts.tool_failed is negative (-1)") {
		t.Errorf("want a negative-count problem, got:\n%s", messages(res))
	}

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if got := readFacet(t, path)["friction_counts"].(map[string]any); len(got) != 0 {
		t.Errorf("friction_counts = %v, want the negative entry dropped", got)
	}
}

func TestStringCountIsReportedEvenWhenItLooksNumeric(t *testing.T) {
	f := clean("sess-strcount")
	f["goal_categories"] = map[string]any{"fix_bug": "3"}
	dir, path := dirWith(t, "sess-strcount", f)

	res, err := Dir(dir, false)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if !hasProblem(res, "goal_categories.fix_bug is not a number") {
		t.Errorf("want a type problem for a quoted count, got:\n%s", messages(res))
	}

	if _, err := Dir(dir, true); err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if got := readFacet(t, path)["goal_categories"].(map[string]any); len(got) != 0 {
		t.Errorf("goal_categories = %v, want the quoted count dropped", got)
	}
}

func TestWholeNumberCountIsAccepted(t *testing.T) {
	f := clean("sess-int")
	f["goal_categories"] = map[string]any{"fix_bug": 3}
	dir, path := dirWith(t, "sess-int", f)

	res, err := Dir(dir, true)
	if err != nil {
		t.Fatalf("Dir(fix): %v", err)
	}
	if len(res.Problems) != 0 {
		t.Errorf("3 is a valid count, got:\n%s", messages(res))
	}
	if len(res.Fixed) != 0 {
		t.Errorf("a valid count must not trigger a rewrite, Fixed = %v", res.Fixed)
	}
	if got := readFacet(t, path)["goal_categories"].(map[string]any)["fix_bug"]; got != float64(3) {
		t.Errorf("fix_bug = %v, want 3", got)
	}
}

func TestAsCountHandlesEachNumericShape(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		wantN   float64
		wantBad string
	}{
		{"json.Number out of range", json.Number("1e400"), 0, "is not a number"},
		{"float64 whole", float64(4), 4, ""},
		{"float64 fractional", float64(2.5), 0, "is not a whole number (2.5)"},
		{"int whole", 7, 7, ""},
		{"int negative", -2, 0, "is negative (-2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, bad := asCount(tc.in)
			if n != tc.wantN || bad != tc.wantBad {
				t.Errorf("asCount(%v) = (%v, %q), want (%v, %q)", tc.in, n, bad, tc.wantN, tc.wantBad)
			}
		})
	}
}

func TestWriteJSONReportsEncodeAndWriteFailures(t *testing.T) {
	dir := t.TempDir()

	encPath := filepath.Join(dir, "sess-enc.json")
	err := writeJSON(encPath, map[string]any{"bad": make(chan int)})
	if err == nil || !strings.Contains(err.Error(), "encoding "+encPath) {
		t.Errorf("writeJSON(unencodable) err = %v, want an encoding error", err)
	}
	if _, statErr := os.Stat(encPath); !os.IsNotExist(statErr) {
		t.Errorf("unencodable value must not produce a file, stat err = %v", statErr)
	}

	writePath := filepath.Join(dir, "no-such-dir", "sess-write.json")
	err = writeJSON(writePath, clean("sess-write"))
	if err == nil || !strings.Contains(err.Error(), "writing "+writePath) {
		t.Errorf("writeJSON(missing dir) err = %v, want a writing error", err)
	}
}

func TestMalformedDirPatternIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "[]")

	res, err := Dir(dir, false)
	if err == nil {
		t.Fatalf("want a glob error for a malformed pattern, got %+v", res)
	}
	if !strings.Contains(err.Error(), "globbing facets in "+dir) {
		t.Errorf("error = %q, want it to name the directory being globbed", err)
	}
	if res.Checked != 0 || len(res.Problems) != 0 {
		t.Errorf("want an empty result on error, got %+v", res)
	}
}

func TestUnreadableFacetIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-dir.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("creating fixture: %v", err)
	}

	res, err := Dir(dir, false)
	if err == nil {
		t.Fatal("want an error for a facet path that cannot be read")
	}
	if !strings.Contains(err.Error(), "sess-dir.json") {
		t.Errorf("error = %v, want it to name the unreadable file", err)
	}
	if res.Checked != 0 || len(res.Problems) != 0 {
		t.Errorf("want an empty result on I/O failure, got %+v", res)
	}

	problems, changed, err := checkFile(path, true)
	if err == nil {
		t.Fatal("checkFile: want an error for an unreadable path")
	}
	if changed {
		t.Error("checkFile reported a change for a file it could not read")
	}
	if problems != nil {
		t.Errorf("checkFile problems = %v, want none on I/O failure", problems)
	}
}

func TestRereadFailureAfterFixIsReturnedAsError(t *testing.T) {
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	fd, err := json.Marshal(r.Fd())
	if err != nil {
		t.Fatalf("formatting fd: %v", err)
	}
	path := filepath.Join(dir, "sess-gone.json")
	if err := os.Symlink("/proc/self/fd/"+string(fd), path); err != nil {
		t.Fatalf("linking fixture: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		// The payload exceeds the pipe buffer, so Write only returns once Dir
		// has opened the file and is draining it. Swapping the path for a
		// directory then lands between the fix pass and the re-read.
		_, err := w.Write([]byte(strings.Repeat(" ", 1<<20) + "[]"))
		if err == nil {
			err = os.Remove(path)
		}
		if err == nil {
			err = os.Mkdir(path, 0o755)
		}
		w.Close()
		done <- err
	}()

	_, err = Dir(dir, true)
	r.Close()
	if gerr := <-done; gerr != nil {
		t.Fatalf("staging the swap: %v", gerr)
	}
	if err == nil || !strings.Contains(err.Error(), "reading facets") {
		t.Errorf("Dir(fix) err = %v, want the re-read failure returned", err)
	}
}

func TestFixReturnsErrorWhenRewriteFails(t *testing.T) {
	f := clean("sess-readonly")
	f["outcome"] = "went_great"
	_, path := dirWith(t, "sess-readonly", f)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	problems, changed, err := checkFile(path, true)
	if err == nil {
		t.Fatal("want a write error for a read-only file")
	}
	if !strings.Contains(err.Error(), "writing") || !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, want a write failure naming %s", err, path)
	}
	if changed {
		t.Error("changed = true, want false when the rewrite failed")
	}
	if len(problems) != 1 || problems[0] != "outcome: invalid value 'went_great'" {
		t.Errorf("problems = %v, want the outcome problem still reported", problems)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading fixture: %v", err)
	}
	if string(before) != string(after) {
		t.Error("read-only file content changed despite the write failure")
	}
}

func TestInfiniteAndNaNCountsAreNotNumbers(t *testing.T) {
	huge := 1e308
	inf := huge * 10
	nan := inf - inf

	for _, v := range []float64{inf, -inf, nan} {
		n, bad := checkCount(v)
		if bad != "is not a number" {
			t.Errorf("checkCount(%v) reason = %q, want %q", v, bad, "is not a number")
		}
		if n != 0 {
			t.Errorf("checkCount(%v) = %v, want 0", v, n)
		}
	}
}
