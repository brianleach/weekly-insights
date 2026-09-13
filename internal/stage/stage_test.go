package stage

import (
	"os"
	"path/filepath"
	"runtime"
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
	// Linux-style credentials file inside the config dir.
	must(os.WriteFile(filepath.Join(ch, ".credentials.json"), []byte(`{"k":"v"}`), 0o600))
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
	// The account and credentials files are copies, never links: the child
	// rewrites them on exit and must not race the live session's originals.
	for _, name := range []string{".claude.json", ".credentials.json"} {
		fi, err := os.Lstat(filepath.Join(dst, name))
		if err != nil {
			t.Errorf("%s missing from stage", name)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s must be copied, not linked", name)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, fi.Mode().Perm())
		}
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

// A session with a transcript but no cached meta record must still be staged.
// The builtin only writes session-meta when /insights runs, so requiring a
// cached record would drop exactly the sessions the report is newest about.
func TestBuildStagesTranscriptWithoutMeta(t *testing.T) {
	p, home := fakeHome(t)
	// ccc has a transcript and neither a meta record nor any facets.
	proj := filepath.Join(p.ClaudeHome, "projects", "-Users-me-code")
	if err := os.WriteFile(filepath.Join(proj, "ccc.jsonl"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "stage")
	sessions := []model.Session{{Meta: model.SessionMeta{SessionID: "ccc"}}}
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, sessions, dst)
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{Sessions: 1, Transcripts: 1}) {
		t.Errorf("counts = %+v, want one session and one transcript with no meta or facets", c)
	}
	if _, err := os.Stat(filepath.Join(dst, "projects", "-Users-me-code", "ccc.jsonl")); err != nil {
		t.Errorf("transcript should be staged even with no cached meta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "usage-data", "session-meta", "ccc.json")); !os.IsNotExist(err) {
		t.Errorf("no meta should be invented for ccc, stat err = %v", err)
	}
}

// The stage holds copies of raw transcripts, which carry prompts and project
// names, so every directory and file it owns is owner-only.
func TestBuildPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	p, home := fakeHome(t)
	// A world-readable file in the sidecar session dir, so the copy is shown to
	// tighten the mode rather than carry the source's over.
	if err := os.WriteFile(filepath.Join(p.ClaudeHome, "projects", "-Users-me-code", "aaa", "title.txt"),
		[]byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "stage")
	sessions := []model.Session{{Meta: model.SessionMeta{SessionID: "aaa"}}}
	if _, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, sessions, dst); err != nil {
		t.Fatal(err)
	}
	dirs := []string{
		dst,
		filepath.Join(dst, "projects"),
		filepath.Join(dst, "projects", "-Users-me-code"),
		filepath.Join(dst, "projects", "-Users-me-code", "aaa"),
		filepath.Join(dst, "usage-data", "session-meta"),
		filepath.Join(dst, "usage-data", "facets"),
	}
	for _, d := range dirs {
		fi, err := os.Stat(d)
		if err != nil {
			t.Errorf("stat %s: %v", d, err)
			continue
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %o, want 0700", d, got)
		}
	}
	files := []string{
		filepath.Join(dst, ".claude.json"),
		filepath.Join(dst, ".credentials.json"),
		filepath.Join(dst, "projects", "-Users-me-code", "aaa.jsonl"),
		filepath.Join(dst, "projects", "-Users-me-code", "aaa", "title.txt"),
		filepath.Join(dst, "usage-data", "session-meta", "aaa.json"),
		filepath.Join(dst, "usage-data", "facets", "aaa.json"),
	}
	for _, f := range files {
		fi, err := os.Lstat(f)
		if err != nil {
			t.Errorf("stat %s: %v", f, err)
			continue
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", f, got)
		}
	}
}

// copyDir carries sidecar directories along, and they get the same treatment as
// everything else the stage owns.
func TestCopyDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "title.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("copied dir mode = %o, want 0700", got)
	}
	fi, err = os.Stat(filepath.Join(dst, "nested", "title.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("copied file mode = %o, want 0600, source mode must not carry over", got)
	}
}

func TestStageTranscriptEdgeCases(t *testing.T) {
	p, _ := fakeHome(t)

	// No transcript on disk: nothing is staged and it is not an error.
	var c Counts
	sp := store.Paths{Root: filepath.Join(t.TempDir(), "usage-data"), ClaudeHome: t.TempDir()}
	if err := stageTranscript(p, sp, "nope", &c); err != nil {
		t.Errorf("missing transcript should be skipped, got %v", err)
	}
	if c.Transcripts != 0 {
		t.Errorf("transcripts = %d, want 0 for a session with no transcript", c.Transcripts)
	}

	// The staged projects path is a regular file, so the project dir cannot be created.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "projects"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c = Counts{}
	sp = store.Paths{Root: filepath.Join(blocked, "usage-data"), ClaudeHome: blocked}
	if err := stageTranscript(p, sp, "aaa", &c); err == nil {
		t.Error("expected an error when the project dir cannot be created")
	}
	if c.Transcripts != 0 {
		t.Errorf("transcripts = %d, want 0 when staging failed", c.Transcripts)
	}

	// The destination transcript already exists, so the exclusive copy fails.
	taken := t.TempDir()
	proj := filepath.Join(taken, "projects", "-Users-me-code")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "aaa.jsonl"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	c = Counts{}
	sp = store.Paths{Root: filepath.Join(taken, "usage-data"), ClaudeHome: taken}
	if err := stageTranscript(p, sp, "aaa", &c); err == nil {
		t.Error("expected an error when the staged transcript already exists")
	}
	if c.Transcripts != 0 {
		t.Errorf("transcripts = %d, want 0 when the copy failed", c.Transcripts)
	}
}

// A source that opens but cannot be read must surface the read error rather
// than leave a silently truncated copy behind that looks like success.
func TestCopyFileReportsReadFailure(t *testing.T) {
	src := t.TempDir() // a directory opens fine but fails on read
	dst := filepath.Join(t.TempDir(), "out.json")
	err := copyFile(src, dst, 0o600)
	if err == nil {
		t.Fatal("expected an error copying from a directory")
	}
	if os.IsNotExist(err) {
		t.Errorf("read failure must not look like a missing file to callers: %v", err)
	}
}

func TestEnsureEmptyDirReportsFilesystemErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(blocker, "stage")
	err := ensureEmptyDir(under)
	want := "creating stage " + under + ": "
	if err == nil || len(err.Error()) < len(want) || err.Error()[:len(want)] != want {
		t.Errorf("stage under a regular file: err = %v, want prefix %q", err, want)
	}

	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	err = ensureEmptyDir(locked)
	want = "reading stage " + locked + ": "
	if err == nil || len(err.Error()) < len(want) || err.Error()[:len(want)] != want {
		t.Errorf("unreadable stage dir: err = %v, want prefix %q", err, want)
	}
}

func TestBuildFailsWhenConfigDirUnreadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-claude")
	p := store.Paths{Root: filepath.Join(missing, "usage-data"), ClaudeHome: missing}
	dst := filepath.Join(t.TempDir(), "stage")
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(t.TempDir(), "nope.json")}, nil, dst)
	if err == nil {
		t.Fatal("expected an error when the real config dir cannot be read")
	}
	if c != (Counts{}) {
		t.Errorf("counts = %+v, want zero", c)
	}
	if _, err := os.Stat(filepath.Join(dst, "usage-data")); !os.IsNotExist(err) {
		t.Errorf("stage should not be populated after a config read failure, stat err = %v", err)
	}
}

func TestBuildFailsOnUnreadableAccountFile(t *testing.T) {
	p, home := fakeHome(t)
	dst := filepath.Join(t.TempDir(), "stage")
	// A path whose parent is a regular file fails with ENOTDIR, which is not a
	// "does not exist" error and so must not be tolerated.
	account := filepath.Join(home, ".claude.json", "nested")
	if _, err := Build(Inputs{Paths: p, AccountFile: account}, nil, dst); err == nil {
		t.Error("expected an error when the account file cannot be read for a reason other than absence")
	}
}

// A credentials entry that exists but cannot be copied is a real failure, not
// the macOS absent-file case, so staging must stop rather than launch a child
// that is silently logged out.
func TestBuildFailsOnUnreadableCredentials(t *testing.T) {
	p, home := fakeHome(t)
	cred := filepath.Join(p.ClaudeHome, ".credentials.json")
	if err := os.Remove(cred); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cred, 0o700); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "stage")
	sessions := []model.Session{{Meta: model.SessionMeta{SessionID: "aaa"}}}
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, sessions, dst)
	if err == nil {
		t.Fatal("expected an error when the credentials file cannot be copied")
	}
	if os.IsNotExist(err) {
		t.Errorf("error should not look like a missing file: %v", err)
	}
	if c != (Counts{}) {
		t.Errorf("counts = %+v, want nothing staged", c)
	}
	if _, err := os.Stat(filepath.Join(dst, "usage-data")); !os.IsNotExist(err) {
		t.Errorf("staging should stop before creating usage-data, stat err = %v", err)
	}
}

// A stage whose owned directories cannot be created must fail before any
// session is staged, rather than silently producing an empty report.
func TestBuildFailsWhenStageDirsCannotBeCreated(t *testing.T) {
	home := t.TempDir()
	p := store.Paths{Root: filepath.Join(home, "usage-data"), ClaudeHome: home}
	// Grow dst until dst itself fits within PATH_MAX but dst/projects does
	// not, so creating the stage's own directories is the first thing to fail.
	seg := "a"
	for len(seg) < 200 {
		seg += "a"
	}
	dst := t.TempDir()
	for len(dst)+len(seg)+1 <= 4090 {
		dst = filepath.Join(dst, seg)
	}
	if rem := 4090 - len(dst); rem >= 2 {
		dst = filepath.Join(dst, seg[:rem-1])
	}
	sessions := []model.Session{{Meta: model.SessionMeta{SessionID: "aaa"}}}
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, "nope.json")}, sessions, dst)
	if err == nil {
		t.Fatal("expected an error when the stage's transcript directory cannot be created")
	}
	if c != (Counts{}) {
		t.Errorf("counts = %+v, want nothing staged", c)
	}
}

// A failure staging a transcript stops the build rather than producing a stage
// that silently lacks sessions.
func TestBuildFailsWhenTranscriptCannotBeStaged(t *testing.T) {
	p, home := fakeHome(t)
	// ccc has a transcript and neither a meta record nor any facets, so the
	// only thing that can fail on the repeated session is the transcript copy.
	proj := filepath.Join(p.ClaudeHome, "projects", "-Users-me-code")
	if err := os.WriteFile(filepath.Join(proj, "ccc.jsonl"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "stage")
	sessions := []model.Session{
		{Meta: model.SessionMeta{SessionID: "ccc"}},
		{Meta: model.SessionMeta{SessionID: "ccc"}},
	}
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, sessions, dst)
	if err == nil {
		t.Fatal("expected an error when the transcript copy fails")
	}
	if c != (Counts{Sessions: 2, Transcripts: 1}) {
		t.Errorf("counts = %+v, want two sessions and one staged transcript", c)
	}
}
