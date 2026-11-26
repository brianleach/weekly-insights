package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdExtract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())
	root := filepath.Join(home, ".claude", "usage-data")
	projects := filepath.Join(home, ".claude", "projects", "-work-app")
	for _, d := range []string{filepath.Join(root, "session-meta"), projects} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	id := "aaaaaaaa-1111-4111-8111-111111111111"
	meta := `{"session_id":"` + id + `","project_path":"/work/app","start_time":"2026-01-08T10:00:00Z","duration_minutes":30,"user_message_count":10}`
	if err := os.WriteFile(filepath.Join(root, "session-meta", id+".json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"fix the build"}}` + "\n"
	if err := os.WriteFile(filepath.Join(projects, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	// A fake claude on PATH, found by the default --claude.
	bin := t.TempDir()
	fake := "#!/bin/sh\nid=$(sed -n 's/^Session: //p' | head -n 1)\n" +
		`echo '{"session_id":"'"$id"'","underlying_goal":"fix","goal_categories":{"bug_fixing":1},"outcome":"fully_achieved","user_satisfaction_counts":{"satisfied":1},"claude_helpfulness":"very_helpful","session_type":"single_task","friction_counts":{},"primary_success":"none","brief_summary":"fixed"}'` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{"--root", root, "--days", "7", "--end", "2026-01-10"}

	out, err := captureStdout(t, func() error { return cmdExtract(append(args, "--dry-run")) })
	if err != nil {
		t.Fatalf("--dry-run: %v", err)
	}
	if !strings.Contains(out, "aaaaaaaa") || !strings.Contains(out, "1 to extract") || !strings.Contains(out, "prompt is") {
		t.Errorf("dry run output: %q", out)
	}
	if m, _ := filepath.Glob(filepath.Join(root, "weekly-facets", "*")); len(m) != 0 {
		t.Fatalf("--dry-run wrote %v", m)
	}

	if err := cmdExtract(args); err != nil {
		t.Fatalf("extract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "weekly-facets", id+".json"))
	if err != nil || !strings.Contains(string(b), "fix_bug") {
		t.Fatalf("facet not landed normalized: %s, %v", b, err)
	}

	// A second run finds nothing left to do.
	out, _ = captureStdout(t, func() error { return cmdExtract(append(args, "--dry-run")) })
	if !strings.Contains(out, "0 to extract") {
		t.Errorf("second dry run: %q", out)
	}

	// --force puts the session back, and a logged-out child fails the command.
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >/dev/null\necho 'Not logged in'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = cmdExtract(append(args, "--force"))
	if err == nil || !strings.Contains(err.Error(), "weekly-insights auth") {
		t.Errorf("want a not-logged-in error, got %v", err)
	}
}

func TestCmdExtractRejectsBadEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", t.TempDir())

	// A usable root, so the unparsable --end is the only thing left to fail on.
	root := filepath.Join(home, ".claude", "usage-data")
	if err := os.MkdirAll(filepath.Join(root, "session-meta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cmdExtract([]string{"--root", root, "--days", "7", "--end", "not-a-date"}); err == nil {
		t.Fatal("want an error from an unparsable --end, got nil")
	}
	if m, _ := filepath.Glob(filepath.Join(root, "weekly-facets", "*")); len(m) != 0 {
		t.Fatalf("a rejected --end still wrote %v", m)
	}
}
