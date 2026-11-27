package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/coach"
	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// coachFixture builds a synthetic home: three saved 7-day snapshots with a
// rising unverified-claim rate and a correction repeated in two sessions, and
// one session whose transcript fails the same ways three times each.
func coachFixture(t *testing.T) (home string, args []string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claude := filepath.Join(home, ".claude")
	root := filepath.Join(claude, "usage-data")
	projects := filepath.Join(claude, "projects", "-work-app")
	for _, d := range []string{filepath.Join(root, "session-meta"), filepath.Join(root, "weekly"), projects} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for i, label := range []string{"2025-12-27", "2026-01-03", "2026-01-10"} {
		s := snapshot.Snapshot{
			Window:   snapshot.Window{Label: label, Days: 7},
			Sessions: snapshot.Sessions{Substantive: 12, FacetSource: map[string]int{"canonical": 12}},
			Rates:    map[string]float64{snapshot.RateUnverifiedClaim: 0.2 + 0.1*float64(i)},
		}
		if label == "2026-01-10" {
			s.UserCorrections = []snapshot.UserCorrection{
				{Session: "bbbbbbbb", Date: "2026-01-06", Quote: "Use the Makefile targets, not raw go commands."},
				{Session: "cccccccc", Date: "2026-01-09", Quote: "use the makefile targets not raw go commands"},
			}
		}
		b, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(root, "weekly", snapshot.FileName(label, 7)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id := "aaaaaaaa-1111-4111-8111-111111111111"
	var lines []string
	n := 0
	fail := func(tool, input, text string) {
		n++
		use := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%d","name":%q,"input":%s}]}}`, n, tool, input)
		res := fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","is_error":true,"content":%q}]}}`, n, text)
		lines = append(lines, use, res)
	}
	denied := "Permission for this action has been denied by the auto mode classifier. Reason: [Scope]"
	for i := 0; i < 3; i++ {
		fail("Bash", `{"command":"git status"}`, denied)
		fail("Bash", `{"command":"rm -rf build"}`, denied)
		fail("Edit", `{"file_path":"/work/app/a.go"}`, "<tool_use_error>File has not been read yet. Read it first.</tool_use_error>")
		fail("Bash", `{"command":"ls missing"}`, "ls: missing: No such file or directory")
	}
	meta := `{"session_id":"` + id + `","project_path":"/work/app","start_time":"2026-01-08T10:00:00Z","duration_minutes":30,"user_message_count":10}`
	for p, b := range map[string]string{
		filepath.Join(root, "session-meta", id+".json"): meta,
		filepath.Join(projects, id+".jsonl"):            strings.Join(lines, "\n") + "\n",
		filepath.Join(claude, "CLAUDE.md"):              "# Mine\n\n- Read a file before editing or overwriting it.\n",
		filepath.Join(claude, "settings.json"):          `{"permissions":{"allow":["Bash(make:*)"]}}`,
	} {
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, []string{"--root", root, "--days", "7", "--end", "2026-01-10"}
}

// tree hashes every file under dir by relative path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCmdCoachWritesOnlyItsProposalsFile runs coach over the fixture and
// asserts the Claude config directory is byte-identical afterwards and the
// only new file anywhere under the home is the proposals file.
func TestCmdCoachWritesOnlyItsProposalsFile(t *testing.T) {
	home, args := coachFixture(t)
	before := tree(t, home)
	out, err := captureStdout(t, func() error {
		return cmdCoach(append(args, "--claude-dir", filepath.Join(home, ".claude")))
	})
	if err != nil {
		t.Fatalf("coach: %v", err)
	}
	t.Logf("coach output:\n%s", out)
	after := tree(t, home)
	proposals := filepath.Join("claude-weekly-insights", "coach", "proposals-7d-2026-01-10.json")
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s changed", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok && rel != proposals {
			t.Errorf("unexpected new file %s", rel)
		}
	}
	fi, err := os.Stat(filepath.Join(home, proposals))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("proposals file: %v %v", fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(filepath.Join(home, proposals))); di.Mode().Perm() != 0o700 {
		t.Errorf("coach dir mode %v", di.Mode().Perm())
	}
	for _, want := range []string{"R1 Recurring correction", "R2 Verify before reporting done",
		"Bash(git status:*)", "uses rm, no allow rule", "R5 Check paths first", "already_present"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Contains(out, "Bash(rm") {
		t.Errorf("proposed an allow rule for rm")
	}
}

func TestCmdCoachJSONAndConfigDirEnv(t *testing.T) {
	home, args := coachFixture(t)
	// No saved snapshot for this window: the coach builds one in memory and
	// saves nothing. The config dir comes from CLAUDE_CONFIG_DIR.
	if err := os.Remove(filepath.Join(home, ".claude", "usage-data", "weekly", "2026-01-10.json")); err != nil {
		t.Fatal(err)
	}
	alt := t.TempDir()
	if err := os.WriteFile(filepath.Join(alt, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", alt)
	out, err := captureStdout(t, func() error { return cmdCoach(append(args, "--json")) })
	if err != nil {
		t.Fatalf("coach: %v", err)
	}
	var res coach.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "settings.json is not valid JSON") {
		t.Errorf("notes %v", res.Notes)
	}
	for _, p := range res.Proposals {
		if p.Status != coach.StatusNew {
			t.Errorf("%s is %s with an empty CLAUDE.md", p.Rule, p.Status)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "usage-data", "weekly", "2026-01-10.json")); err == nil {
		t.Errorf("coach saved a snapshot")
	}
	if got := resolveClaudeDir("", "/h"); got != alt {
		t.Errorf("resolveClaudeDir = %q", got)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := resolveClaudeDir("", "/h"); got != filepath.Join("/h", ".claude") {
		t.Errorf("resolveClaudeDir default = %q", got)
	}
}
