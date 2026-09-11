package progress

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/runner"
)

// fixture mimics the builtin report's markup with synthetic content.
const fixture = `<html><body><div class="container">
<div class="stats-row"><div class="stat"><div class="stat-value">120</div><div class="stat-label">Messages</div></div>
<div class="stat"><div class="stat-value">4</div><div class="stat-label">Days</div></div></div>
<div class="at-a-glance"><div class="glance-title">At a Glance</div><div class="glance-sections">
<div class="glance-section"><strong>What's working:</strong> Steady &amp; focused. <a href="#x" class="see-more">More →</a></div>
</div></div>
<div class="project-area"><div class="area-header"><div class="area-name">Widgets</div><div class="area-count">3</div></div><div class="area-desc">Widget work.</div></div>
<div class="big-win"><div class="big-win-title">Shipped the thing</div><div class="big-win-desc">It shipped.</div></div>
<div class="friction-category"><div class="friction-title">Unverified claims</div><div class="friction-desc">Said done when not.</div>
<ul class="friction-examples"><li>Claimed a URL worked; it 404&apos;d</li><li>Second &lt;example&gt;</li></ul></div>
<div class="claude-md-item"><code class="cmd-code">## Rules
- Always link PRs
- Verify first</code><div class="cmd-why">Because links were missing.</div></div>
<div class="feature-card"><div class="feature-title">Hooks</div></div>
<div class="horizon-card"><div class="horizon-title">Self-verifying reviews</div></div>
</div></body></html>`

func writeReport(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractPullsEverySection(t *testing.T) {
	p := writeReport(t, t.TempDir(), "insights-7d-2026-09-11.html")
	w, err := Extract(p)
	if err != nil {
		t.Fatal(err)
	}
	if w.Label != "2026-09-11" || w.Days != 7 {
		t.Errorf("label/days = %s/%d", w.Label, w.Days)
	}
	if len(w.Stats) != 2 || w.Stats[0].Value != "120" || w.Stats[0].Label != "Messages" {
		t.Errorf("stats = %+v", w.Stats)
	}
	if len(w.Glance) != 1 || !strings.Contains(w.Glance[0], "Steady & focused") || strings.Contains(w.Glance[0], "<") {
		t.Errorf("glance not cleaned: %q", w.Glance)
	}
	if len(w.Areas) != 1 || w.Areas[0].Title != "Widgets" {
		t.Errorf("areas = %+v", w.Areas)
	}
	if len(w.Wins) != 1 || w.Wins[0].Desc != "It shipped." {
		t.Errorf("wins = %+v", w.Wins)
	}
	if len(w.Friction) != 1 || len(w.Friction[0].Examples) != 2 {
		t.Fatalf("friction = %+v", w.Friction)
	}
	if w.Friction[0].Examples[0] != "Claimed a URL worked; it 404'd" || w.Friction[0].Examples[1] != "Second <example>" {
		t.Errorf("examples not unescaped: %q", w.Friction[0].Examples)
	}
	if len(w.Suggestions) != 1 || !strings.Contains(w.Suggestions[0].Text, "- Always link PRs\n- Verify first") || w.Suggestions[0].Why != "Because links were missing." {
		t.Errorf("suggestions = %+v", w.Suggestions)
	}
	if len(w.Features) != 1 || w.Features[0] != "Hooks" || len(w.Horizon) != 1 {
		t.Errorf("features/horizon = %v / %v", w.Features, w.Horizon)
	}
}

func TestExtractRejectsNonWindowedName(t *testing.T) {
	p := writeReport(t, t.TempDir(), "report-2026-09-11-164317.html")
	if _, err := Extract(p); err == nil {
		t.Error("a cumulative builtin report must be rejected; it has no window")
	}
}

func TestDiscoverOrdersByEndDate(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"insights-7d-2026-09-11.html", "insights-7d-2026-08-28.html", "insights-30d-2026-09-04.html", "week-2026-09-11.html", "trend.html"} {
		writeReport(t, dir, n)
	}
	got, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range got {
		names = append(names, filepath.Base(g))
	}
	want := "insights-7d-2026-08-28.html insights-30d-2026-09-04.html insights-7d-2026-09-11.html"
	if strings.Join(names, " ") != want {
		t.Errorf("order = %v", names)
	}
}

func TestInputTextLabelsWeeksAndTruncatesClaudeMD(t *testing.T) {
	p := writeReport(t, t.TempDir(), "insights-7d-2026-09-11.html")
	w, _ := Extract(p)
	in := Input{Weeks: []Week{w}, ClaudeMD: strings.Repeat("x", maxClaudeMD+100), Trend: "| Week | x |"}
	txt := in.Text()
	for _, want := range []string{"WEEK ENDING 2026-09-11 (7 days)", "Friction: Unverified claims.", "Suggested CLAUDE.md addition:", "NUMERIC TREND", "[... truncated ...]"} {
		if !strings.Contains(txt, want) {
			t.Errorf("input text missing %q", want)
		}
	}
}

func TestParseToleratesFenceAndProse(t *testing.T) {
	reply := "Here you go:\n```json\n{\"verdict\":\"flat\",\"recurring\":[],\"resolved\":[],\"suggestions\":[],\"already_have\":[],\"numbers\":\"\",\"one_change\":\"x\"}\n```\nthanks"
	m, err := Parse(reply)
	if err != nil || m.Verdict != "flat" || m.OneChange != "x" {
		t.Errorf("parse failed: %v %+v", err, m)
	}
	if _, err := Parse("no json here"); err == nil {
		t.Error("expected an error for a reply with no object")
	}
}

func TestRenderEscapesAndFallsBack(t *testing.T) {
	p := writeReport(t, t.TempDir(), "insights-7d-2026-09-11.html")
	w, _ := Extract(p)
	in := Input{Weeks: []Week{w}}
	m := Memo{
		Verdict:     "Flat <b>overall</b>",
		Recurring:   []Recurring{{Theme: "Links & anchors", Weeks: []string{"2026-09-04", "2026-09-11"}, Evidence: "e", Trend: "same"}},
		Suggestions: []Suggestion2{{Suggestion: "Link PRs", FirstSuggested: "2026-09-04", Adopted: true, Effect: "recurred anyway"}},
		OneChange:   "Add a hook",
	}
	got := Render(m, "", in)
	if strings.Contains(got, "<b>overall</b>") || !strings.Contains(got, "&lt;b&gt;overall&lt;/b&gt;") {
		t.Error("verdict was not escaped")
	}
	for _, want := range []string{"Links &amp; anchors", `class="trend same"`, `class="adopt yes"`, "Add a hook", "insights-7d-2026-09-11.html", "Sep 4", "Sep 11"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q", want)
		}
	}
	if strings.Contains(got, "—") {
		t.Error("render contains an em dash")
	}
	// Unparseable reply: show it verbatim rather than an empty page.
	fb := Render(Memo{}, "the model said <something>", in)
	if !strings.Contains(fb, "could not be parsed") || !strings.Contains(fb, "&lt;something&gt;") {
		t.Error("fallback did not show the escaped raw reply")
	}
}

func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func oneWeek(t *testing.T) Input {
	t.Helper()
	p := writeReport(t, t.TempDir(), "insights-7d-2026-09-11.html")
	w, err := Extract(p)
	if err != nil {
		t.Fatal(err)
	}
	return Input{Weeks: []Week{w}}
}

const memoJSON = `{"verdict":"ok","recurring":[],"resolved":[],"suggestions":[],"already_have":[],"numbers":"","one_change":"c"}`

func TestRunWithFakeClaude(t *testing.T) {
	bin := fakeClaude(t, `
[ -z "$CLAUDECODE" ] || { echo nested; exit 3; }
[ "$1" = "-p" ] || { echo bad args; exit 3; }
grep -q 'WEEK ENDING 2026-09-11' || { echo 'stdin missing weeks'; exit 3; }
echo '`+memoJSON+`'
`)
	t.Setenv("CLAUDECODE", "1")
	m, raw, err := Run(oneWeek(t), Options{ClaudeBin: bin})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if m.Verdict != "ok" || m.OneChange != "c" {
		t.Errorf("memo = %+v", m)
	}
}

// A token in the parent environment is stripped by runner.BaseEnv, so the only
// way the child sees one is Options.Token putting it back.
func TestRunPassesTokenOnlyWhenSet(t *testing.T) {
	bin := fakeClaude(t, `
[ "$WANT_TOKEN" = "$CLAUDE_CODE_OAUTH_TOKEN" ] || { echo "token = '$CLAUDE_CODE_OAUTH_TOKEN', want '$WANT_TOKEN'"; exit 3; }
echo '`+memoJSON+`'
`)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-parent-should-not-leak")
	in := oneWeek(t)

	t.Setenv("WANT_TOKEN", "tok")
	if _, raw, err := Run(in, Options{ClaudeBin: bin, Token: "tok"}); err != nil {
		t.Errorf("with a token: %v\n%s", err, raw)
	}

	t.Setenv("WANT_TOKEN", "")
	if _, raw, err := Run(in, Options{ClaudeBin: bin}); err != nil {
		t.Errorf("without a token the child must see none: %v\n%s", err, raw)
	}
}

// A failed process and an unparseable reply are different outcomes: only the
// second leaves an answer the caller may keep.
func TestRunSeparatesExecFailureFromParseFailure(t *testing.T) {
	in := oneWeek(t)

	bin := fakeClaude(t, `echo "partial reply {"; exit 2`)
	_, raw, err := Run(in, Options{ClaudeBin: bin})
	if !errors.Is(err, ErrExec) {
		t.Errorf("a nonzero exit must report ErrExec, got %v", err)
	}
	if errors.Is(err, ErrParse) {
		t.Error("a failed process must not be reported as a parse failure")
	}
	if raw != "" {
		t.Errorf("a failed process must not offer a fallback reply, got %q", raw)
	}

	bin = fakeClaude(t, `echo "no json at all"; exit 0`)
	_, raw, err = Run(in, Options{ClaudeBin: bin})
	if !errors.Is(err, ErrParse) {
		t.Errorf("a successful run with an unparseable reply must report ErrParse, got %v", err)
	}
	if !strings.Contains(raw, "no json at all") {
		t.Errorf("the raw reply must survive for the fallback page, got %q", raw)
	}
}

func TestRunDetectsNotLoggedIn(t *testing.T) {
	bin := fakeClaude(t, `echo "Not logged in · Please run /login"; exit 0`)
	if _, _, err := Run(oneWeek(t), Options{ClaudeBin: bin}); !errors.Is(err, runner.ErrNotLoggedIn) {
		t.Errorf("expected runner.ErrNotLoggedIn, got %v", err)
	}
}
