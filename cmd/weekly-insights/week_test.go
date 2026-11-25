package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeClaude stands in for Claude Code in every step: `-p /insights` writes a
// report where the runner looks for it (exit 2 when FAKE_INSIGHTS_FAIL is
// set), an extraction call (stdin starting "Session:") answers canned facet
// JSON, and anything else is the progress call. Every stdin is appended to
// FAKE_LOG so a test can see what was sent.
const fakeClaude = `#!/bin/sh
if [ "$2" = "/insights" ]; then
  [ -n "$FAKE_INSIGHTS_FAIL" ] && exit 2
  mkdir -p "$CLAUDE_CONFIG_DIR/usage-data"
  echo '<html><body><h1>Claude Code Insights</h1><p>synthetic</p></body></html>' > "$CLAUDE_CONFIG_DIR/usage-data/report-20260110T000000.html"
  exit 0
fi
in=$(cat)
printf '%s\n' "$in" >> "$FAKE_LOG"
case "$in" in
Session:*)
  id=$(printf '%s\n' "$in" | sed -n 's/^Session: //p' | head -n 1)
  echo '{"session_id":"'"$id"'","underlying_goal":"fix","goal_categories":{"bug_fixing":1},"outcome":"fully_achieved","user_satisfaction_counts":{"satisfied":1},"claude_helpfulness":"very_helpful","session_type":"single_task","friction_counts":{},"primary_success":"none","brief_summary":"fixed"}'
  ;;
*) echo 'not json' ;;
esac
`

// weekFixture builds a synthetic home with one session in the window ending
// 2026-01-10, a global CLAUDE.md carrying a marker, and the fake claude.
func weekFixture(t *testing.T) (args []string, out, log string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("FAKE_INSIGHTS_FAIL", "")
	root := filepath.Join(home, ".claude", "usage-data")
	projects := filepath.Join(home, ".claude", "projects", "-work-app")
	for _, d := range []string{filepath.Join(root, "session-meta"), projects} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	id := "aaaaaaaa-1111-4111-8111-111111111111"
	meta := `{"session_id":"` + id + `","project_path":"/work/app","start_time":"2026-01-08T10:00:00Z","duration_minutes":30,"user_message_count":10}`
	line := `{"type":"user","message":{"role":"user","content":"fix the build"}}` + "\n"
	for p, b := range map[string]string{
		filepath.Join(root, "session-meta", id+".json"): meta,
		filepath.Join(projects, id+".jsonl"):            line,
		filepath.Join(home, ".claude", "CLAUDE.md"):     "claude-md-marker-7f3a",
	} {
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the fake is on PATH, beside the shell tools it needs, so --open
	// and the default --claude can launch nothing real.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	log = filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv("FAKE_LOG", log)
	out = filepath.Join(home, "claude-weekly-insights")
	return []string{"--root", root, "--days", "7", "--end", "2026-01-10", "--out", out}, out, log
}

func readBundle(t *testing.T, dir string) weekBundle {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m weekBundle
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func statuses(m weekBundle) map[string]string {
	s := map[string]string{}
	for _, st := range m.Steps {
		s[st.Name] = st.Status
	}
	return s
}

func TestCmdWeekHappyPath(t *testing.T) {
	args, out, log := weekFixture(t)
	// An older weekly report, so --progress has two weeks to compare.
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	old := "<html><body><h1>Claude Code Insights</h1><p>last week</p></body></html>"
	if err := os.WriteFile(filepath.Join(out, "insights-7d-2026-01-03.html"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, err := captureStdout(t, func() error { return cmdWeek(append(args, "--progress")) })
	if err != nil {
		t.Fatalf("week: %v", err)
	}
	dir := filepath.Join(out, "week-7d-2026-01-10")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if lines[len(lines)-1] != filepath.Join(dir, "index.html") {
		t.Errorf("last stdout line = %q", lines[len(lines)-1])
	}

	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("bundle dir: %v, %v", fi, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var tree []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		tree = append(tree, fmt.Sprintf("%v  %s", info.Mode(), e.Name()))
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", e.Name(), info.Mode().Perm())
		}
	}
	t.Logf("bundle tree:\n%v  %s/\n  %s", fi.Mode(), filepath.Base(dir), strings.Join(tree, "\n  "))

	want := []string{"bundle.json", "failures.txt", "index.html", "insights-7d-2026-01-10.html",
		"progress.html", "trend.html", "weekly.html"}
	m := readBundle(t, dir)
	if strings.Join(m.Files, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v", m.Files, want)
	}
	for _, f := range want {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	for name, st := range statuses(m) {
		if st != "ok" {
			t.Errorf("step %s = %s", name, st)
		}
	}
	if len(m.Steps) != 8 || m.Window.End != "2026-01-10" || m.Window.Days != 7 || m.Version == "" {
		t.Errorf("bundle.json = %+v", m)
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	if !strings.Contains(string(idx), `href="weekly.html"`) {
		t.Errorf("index does not link weekly.html")
	}
	// Extraction landed, so the snapshot counts the canonical facet.
	if _, err := os.Stat(filepath.Join(args[1], "weekly-facets", "aaaaaaaa-1111-4111-8111-111111111111.json")); err != nil {
		t.Errorf("facet not extracted: %v", err)
	}
	sent, _ := os.ReadFile(log)
	if !strings.Contains(string(sent), "claude-md-marker-7f3a") {
		t.Errorf("--progress did not send CLAUDE.md")
	}
}

func TestCmdWeekInsightsFailureIsNotFatal(t *testing.T) {
	args, out, log := weekFixture(t)
	t.Setenv("FAKE_INSIGHTS_FAIL", "1")
	stdout, err := captureStdout(t, func() error { return cmdWeek(args) })
	if err == nil || !strings.Contains(err.Error(), "insights") {
		t.Fatalf("want a failed-step error, got %v", err)
	}
	dir := filepath.Join(out, "week-7d-2026-01-10")
	if !strings.HasSuffix(strings.TrimSpace(stdout), filepath.Join(dir, "index.html")) {
		t.Errorf("stdout = %q", stdout)
	}
	st := statuses(readBundle(t, dir))
	if st["insights"] != "failed" || st["extract"] != "ok" || st["aggregate"] != "ok" ||
		st["weekly"] != "ok" || st["trend"] != "ok" || st["progress"] != "skipped" {
		t.Errorf("statuses = %v", st)
	}
	for _, f := range []string{"weekly.html", "trend.html", "failures.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s after a failed insights run", f)
		}
	}
	// Without --progress the global CLAUDE.md never reaches the model.
	sent, _ := os.ReadFile(log)
	if strings.Contains(string(sent), "claude-md-marker-7f3a") {
		t.Errorf("CLAUDE.md sent without --progress")
	}
}

func TestCmdWeekOfflineAndRerun(t *testing.T) {
	args, out, log := weekFixture(t)
	args = append(args, "--skip-insights", "--skip-extract", "--claude", filepath.Join(out, "no-such-claude"))
	for range 2 {
		if _, err := captureStdout(t, func() error { return cmdWeek(args) }); err != nil {
			t.Fatalf("offline week: %v", err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("an offline run called claude")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "week-7d-2026-01-10" {
		t.Errorf("out dir after rerun = %v, want only the bundle", names)
	}
	st := statuses(readBundle(t, filepath.Join(out, "week-7d-2026-01-10")))
	if st["insights"] != "skipped" || st["extract"] != "skipped" || st["aggregate"] != "ok" {
		t.Errorf("statuses = %v", st)
	}
}

// TestCmdInsightsStdoutIsReportPath pins the default command's output across
// the runInsights refactor: stdout is the report path and nothing else.
func TestCmdInsightsStdoutIsReportPath(t *testing.T) {
	args, out, _ := weekFixture(t)
	stdout, err := captureStdout(t, func() error { return cmdInsights(args) })
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	want := filepath.Join(out, "insights-7d-2026-01-10.html") + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if fi, err := os.Stat(strings.TrimSpace(want)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("report: %v, %v", fi, err)
	}
}
