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
