package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/runner"
	"github.com/brianleach/weekly-insights/internal/store"
)

// facetJSON is a valid canonical facet for the id the fake reads off stdin.
const facetJSON = `{"session_id":"'"$id"'","underlying_goal":"fix the build","goal_categories":{"%s":1},"outcome":"fully_achieved","user_satisfaction_counts":{"satisfied":1},"claude_helpfulness":"very_helpful","session_type":"single_task","friction_counts":{},"primary_success":"correct_code_edits","brief_summary":"fixed it"}`

// fakeClaude writes a shell script standing in for claude. The body runs after
// $id is read from the Session header on stdin.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nid=$(sed -n 's/^Session: //p' | head -n 1)\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func echoFacet(goal string) string {
	return "echo 'Here you go:'\necho '" + fmt.Sprintf(facetJSON, goal) + "'"
}

// fixture builds n synthetic sessions with transcripts under a temp home.
func fixture(t *testing.T, n int) (store.Paths, []model.Session) {
	t.Helper()
	home := t.TempDir()
	p := store.Paths{Root: filepath.Join(home, "usage-data"), ClaudeHome: home}
	proj := filepath.Join(p.Transcripts(), "-work-app")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	var ss []model.Session
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%08x-1111-4111-8111-111111111111", i+1)
		line := `{"type":"user","message":{"role":"user","content":"fix the build please"}}` + "\n"
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		ss = append(ss, model.Session{Meta: model.SessionMeta{
			SessionID: id, ProjectPath: "/work/app", StartTime: "2026-01-08T10:00:00Z", UserMessageCount: 10 + i,
		}})
	}
	return p, ss
}

func facets(t *testing.T, p store.Paths) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(p.WeeklyFacets(), "*"))
	return m
}

// isolateTemp points os.TempDir at an empty directory so a test can prove the
// scratch directory is gone afterwards.
func isolateTemp(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("TMPDIR", d)
	return d
}

func assertNoScratch(t *testing.T, tmp string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(tmp, "weekly-insights-extract-*"))
	if len(m) != 0 {
		t.Errorf("scratch directory left behind: %v", m)
	}
}

func TestValidReplyLands(t *testing.T) {
	tmp := isolateTemp(t)
	p, ss := fixture(t, 1)
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: fakeClaude(t, echoFacet("fix_bug"))})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extracted != 1 || res.Fixed != 0 || len(res.Rejected) != 0 {
		t.Fatalf("result = %+v", res)
	}
	got := facets(t, p)
	if len(got) != 1 || filepath.Base(got[0]) != ss[0].Meta.SessionID+".json" {
		t.Fatalf("facets = %v", got)
	}
	fi, _ := os.Stat(got[0])
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	assertNoScratch(t, tmp)
}

func TestOffVocabularyIsFixed(t *testing.T) {
	p, ss := fixture(t, 1)
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: fakeClaude(t, echoFacet("bug_fixing"))})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extracted != 1 || res.Fixed != 1 {
		t.Fatalf("result = %+v", res)
	}
	b, _ := os.ReadFile(facets(t, p)[0])
	if strings.Contains(string(b), "bug_fixing") || !strings.Contains(string(b), "fix_bug") {
		t.Errorf("landed file not normalized: %s", b)
	}
}

func TestMissingFieldIsRejected(t *testing.T) {
	tmp := isolateTemp(t)
	p, ss := fixture(t, 1)
	var stderr bytes.Buffer
	bin := fakeClaude(t, `echo '{"session_id":"'"$id"'","outcome":"fully_achieved"}'`)
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: bin, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if res.Extracted != 0 || len(res.Rejected) != 1 || res.Rejected[0] != ss[0].Meta.SessionID[:8] {
		t.Fatalf("result = %+v", res)
	}
	if len(facets(t, p)) != 0 {
		t.Error("a rejected facet reached weekly-facets")
	}
	if !strings.Contains(stderr.String(), "missing field") {
		t.Errorf("rejection reason not reported: %q", stderr.String())
	}
	assertNoScratch(t, tmp)

	// No JSON at all is a rejection too, not a child failure.
	res, err = Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: fakeClaude(t, "echo sorry")})
	if err != nil || len(res.Rejected) != 1 || res.Failed != 0 {
		t.Fatalf("no-JSON reply: %+v, %v", res, err)
	}
}

func TestCircuitBreaker(t *testing.T) {
	p, ss := fixture(t, 5)
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, Concurrency: 1, ClaudeBin: fakeClaude(t, "exit 3")})
	if !errors.Is(err, ErrCircuitOpen) || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
	if res.Failed != 3 || res.Unfinished != 2 {
		t.Errorf("result = %+v, want 3 failed and 2 unfinished", res)
	}
}

func TestNotLoggedInStopsAtOnce(t *testing.T) {
	p, ss := fixture(t, 3)
	log := filepath.Join(t.TempDir(), "calls")
	bin := fakeClaude(t, "echo x >> "+log+"\necho 'Not logged in'")
	_, err := Run(context.Background(), Options{Paths: p, Sessions: ss, Concurrency: 1, ClaudeBin: bin})
	if !errors.Is(err, runner.ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(log); strings.Count(string(b), "x") != 1 {
		t.Errorf("want one call before stopping, got %q", b)
	}
}

func TestTimeoutCancelsWithoutPartialFile(t *testing.T) {
	p, ss := fixture(t, 1)
	bin := fakeClaude(t, "exec sleep 5")
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: bin, PerSessionTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || len(facets(t, p)) != 0 {
		t.Fatalf("result = %+v, facets = %v", res, facets(t, p))
	}
}

func TestBudgetStopsTheRun(t *testing.T) {
	p, ss := fixture(t, 3)
	bin := fakeClaude(t, "exec sleep 5")
	start := time.Now()
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, Concurrency: 1, ClaudeBin: bin, Budget: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BudgetExceeded || res.Unfinished != 3 || res.Failed != 0 {
		t.Errorf("result = %+v", res)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("in-flight session was not cancelled at the budget")
	}
}

func TestInterruptLeavesNothing(t *testing.T) {
	tmp := isolateTemp(t)
	p, ss := fixture(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, Options{Paths: p, Sessions: ss, ClaudeBin: fakeClaude(t, "exec sleep 5")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if len(facets(t, p)) != 0 {
		t.Error("interrupted run left a facet file")
	}
	assertNoScratch(t, tmp)
}

// maxConcurrent replays a start/end log and returns the peak overlap.
func maxConcurrent(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	cur, peak := 0, 0
	for _, l := range strings.Fields(string(b)) {
		if l == "s" {
			cur++
		} else {
			cur--
		}
		peak = max(peak, cur)
	}
	return peak
}

func TestConcurrencyBound(t *testing.T) {
	for _, tc := range []struct{ asked, bound int }{{2, 2}, {10, MaxConcurrency}} {
		p, ss := fixture(t, 8)
		log := filepath.Join(t.TempDir(), "log")
		bin := fakeClaude(t, "echo s >> "+log+"\nsleep 0.3\necho e >> "+log+"\n"+echoFacet("fix_bug"))
		res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, Concurrency: tc.asked, ClaudeBin: bin})
		if err != nil || res.Extracted != 8 {
			t.Fatalf("asked %d: %+v, %v", tc.asked, res, err)
		}
		if peak := maxConcurrent(t, log); peak > tc.bound || peak < 2 {
			t.Errorf("asked %d: peak %d, want 2..%d", tc.asked, peak, tc.bound)
		}
	}
}

func TestDefaultLimitAndAll(t *testing.T) {
	p, ss := fixture(t, DefaultLimit+2)
	if w := Plan(Options{Paths: p, Sessions: ss}); len(w.Items) != DefaultLimit || w.OverLimit != 2 {
		t.Errorf("default plan: %d items, %d over", len(w.Items), w.OverLimit)
	}
	if w := Plan(Options{Paths: p, Sessions: ss, Limit: 3}); len(w.Items) != 3 || w.Items[0].ID != ss[len(ss)-1].Meta.SessionID {
		t.Errorf("--limit 3 plan not longest first: %+v", w.Items)
	}
	if w := Plan(Options{Paths: p, Sessions: ss, All: true}); len(w.Items) != len(ss) {
		t.Errorf("--all plan: %d items", len(w.Items))
	}
}

func TestExistingFacetKeptWithoutForce(t *testing.T) {
	p, ss := fixture(t, 1)
	if err := os.MkdirAll(p.WeeklyFacets(), 0o700); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(p.WeeklyFacets(), ss[0].Meta.SessionID+".json")
	orig := []byte(`{"hand":"written"}`)
	if err := os.WriteFile(dst, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := fakeClaude(t, echoFacet("fix_bug"))
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: bin})
	if err != nil || res.SkippedExisting != 1 || res.Extracted != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, orig) {
		t.Errorf("existing facet changed without --force: %s", b)
	}
	res, err = Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: bin, Force: true})
	if err != nil || res.Extracted != 1 {
		t.Fatalf("--force: %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(dst); bytes.Equal(b, orig) {
		t.Error("--force did not replace the facet")
	}
}

func TestSkipsUnrenderableSessions(t *testing.T) {
	p, ss := fixture(t, 1)
	ss = append(ss,
		model.Session{Meta: model.SessionMeta{SessionID: "../escape"}},
		model.Session{Meta: model.SessionMeta{SessionID: "dddddddd-4444-4444-8444-444444444444"}})
	empty := "eeeeeeee-5555-4555-8555-555555555555"
	if err := os.WriteFile(filepath.Join(p.Transcripts(), "-work-app", empty+".jsonl"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ss = append(ss, model.Session{Meta: model.SessionMeta{SessionID: empty}})
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: fakeClaude(t, echoFacet("fix_bug"))})
	if err != nil || res.Skipped != 3 || res.Extracted != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	// Nothing to do is not an error and starts no child.
	res, err = Run(context.Background(), Options{Paths: p, Sessions: nil, ClaudeBin: "/nonexistent"})
	if err != nil || res.Extracted != 0 {
		t.Fatalf("empty run: %+v, %v", res, err)
	}
}

func TestTokenReachesChild(t *testing.T) {
	p, ss := fixture(t, 1)
	seen := filepath.Join(t.TempDir(), "tok")
	bin := fakeClaude(t, `printf %s "$CLAUDE_CODE_OAUTH_TOKEN" > `+seen+"\n"+echoFacet("fix_bug"))
	if _, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: bin, Token: "synthetic-token"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(seen); string(b) != "synthetic-token" {
		t.Errorf("child saw token %q", b)
	}
}

func TestMissingBinaryFails(t *testing.T) {
	p, ss := fixture(t, 1)
	res, err := Run(context.Background(), Options{Paths: p, Sessions: ss, ClaudeBin: filepath.Join(t.TempDir(), "nope")})
	if err != nil || res.Failed != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
}
