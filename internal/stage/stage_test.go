package stage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// fakeHome lays out a synthetic ~/.claude with two sessions: one with a
// canonical facet, one with only a builtin facet and no session dir.
func fakeHome(t *testing.T) (store.Paths, string) {
	t.Helper()
	home := t.TempDir()
	ch := filepath.Join(home, ".claude")
	p := store.Paths{Root: filepath.Join(ch, "usage-data"), ClaudeHome: ch}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"plugins", "commands", filepath.Join("projects", "-Users-me-code"),
		"usage-data/session-meta", "usage-data/facets", "usage-data/weekly-facets"} {
		must(os.MkdirAll(filepath.Join(ch, d), 0o755))
	}
	must(os.WriteFile(filepath.Join(ch, "settings.json"), []byte(`{}`), 0o644))
	must(os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{}}`), 0o600))
	proj := filepath.Join(ch, "projects", "-Users-me-code")
	must(os.WriteFile(filepath.Join(proj, "aaa.jsonl"), []byte(`{}`), 0o644))
	must(os.MkdirAll(filepath.Join(proj, "aaa"), 0o755))
	must(os.WriteFile(filepath.Join(proj, "bbb.jsonl"), []byte(`{}`), 0o644))
	must(os.WriteFile(filepath.Join(proj, "zzz.jsonl"), []byte(`{}`), 0o644)) // out of window
	must(os.WriteFile(filepath.Join(p.SessionMeta(), "aaa.json"), []byte(`{"session_id":"aaa"}`), 0o644))
	must(os.WriteFile(filepath.Join(p.SessionMeta(), "bbb.json"), []byte(`{"session_id":"bbb"}`), 0o644))
	must(os.WriteFile(filepath.Join(p.WeeklyFacets(), "aaa.json"), []byte(`{"src":"weekly"}`), 0o644))
	must(os.WriteFile(filepath.Join(p.BuiltinFacets(), "aaa.json"), []byte(`{"src":"builtin"}`), 0o644))
	must(os.WriteFile(filepath.Join(p.BuiltinFacets(), "bbb.json"), []byte(`{"src":"builtin"}`), 0o644))
	return p, home
}

func TestBuildStagesOnlyTheWindow(t *testing.T) {
	p, home := fakeHome(t)
	dst := filepath.Join(t.TempDir(), "stage")
	sessions := []model.Session{
		{Meta: model.SessionMeta{SessionID: "aaa"}},
		{Meta: model.SessionMeta{SessionID: "bbb"}},
	}
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, sessions, dst)
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{Sessions: 2, Transcripts: 2, Meta: 2, Facets: 2}) {
		t.Errorf("counts = %+v", c)
	}

	// Shared config is linked; owned dirs are real directories.
	for _, name := range []string{"settings.json", "plugins", "commands"} {
		if fi, err := os.Lstat(filepath.Join(dst, name)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink into the real config dir", name)
		}
	}
	for _, name := range []string{"projects", "usage-data"} {
		if fi, err := os.Lstat(filepath.Join(dst, name)); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s must be a real directory owned by the stage", name)
		}
	}
	// The account file is a copy, never a link.
	if fi, err := os.Lstat(filepath.Join(dst, ".claude.json")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Error(".claude.json must be copied, not linked")
	}

	proj := filepath.Join(dst, "projects", "-Users-me-code")
	for _, f := range []string{"aaa.jsonl", "bbb.jsonl", "aaa"} {
		fi, err := os.Lstat(filepath.Join(proj, f))
		if err != nil {
			t.Errorf("expected %s in staged project dir", f)
			continue
		}
		// The builtin's directory listing skips symlinks, so these must be real.
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s must be a copy, not a symlink, or the builtin will not see it", f)
		}
	}
	if _, err := os.Lstat(filepath.Join(proj, "zzz.jsonl")); err == nil {
		t.Error("out-of-window transcript leaked into the stage")
	}

	// Canonical facet wins for aaa; builtin is the fallback for bbb.
	b, _ := os.ReadFile(filepath.Join(dst, "usage-data", "facets", "aaa.json"))
	if string(b) != `{"src":"weekly"}` {
		t.Errorf("aaa facet should come from weekly-facets, got %s", b)
	}
	b, _ = os.ReadFile(filepath.Join(dst, "usage-data", "facets", "bbb.json"))
	if string(b) != `{"src":"builtin"}` {
		t.Errorf("bbb facet should fall back to builtin, got %s", b)
	}
}

func TestBuildRefusesNonEmptyStage(t *testing.T) {
	p, home := fakeHome(t)
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "junk"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, nil, dst); err == nil {
		t.Error("expected an error for a non-empty stage directory")
	}
}

func TestBuildToleratesMissingAccountFile(t *testing.T) {
	p, _ := fakeHome(t)
	dst := filepath.Join(t.TempDir(), "stage")
	if _, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(t.TempDir(), "nope.json")}, nil, dst); err != nil {
		t.Errorf("a missing account file is not fatal on platforms that keep login state elsewhere: %v", err)
	}
}
