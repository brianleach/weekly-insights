package progress

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRunWithFakeClaude(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"[ -z \"$CLAUDECODE\" ] || { echo nested; exit 3; }\n" +
		"[ \"$1\" = \"-p\" ] || { echo bad args; exit 3; }\n" +
		"grep -q 'WEEK ENDING 2026-09-11' || { echo 'stdin missing weeks'; exit 3; }\n" +
		"echo '{\"verdict\":\"ok\",\"recurring\":[],\"resolved\":[],\"suggestions\":[],\"already_have\":[],\"numbers\":\"\",\"one_change\":\"c\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")
	p := writeReport(t, t.TempDir(), "insights-7d-2026-09-11.html")
	w, _ := Extract(p)
	m, raw, err := Run(Input{Weeks: []Week{w}}, Options{ClaudeBin: bin})
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if m.Verdict != "ok" || m.OneChange != "c" {
		t.Errorf("memo = %+v", m)
	}
}
