package stage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestStageTranscriptErrorsAndMissing(t *testing.T) {
	p, _ := fakeHome(t)
	dst := t.TempDir()
	sp := store.Paths{Root: filepath.Join(dst, "usage-data"), ClaudeHome: dst}

	// No transcript anywhere for this id: nothing is staged and it is not an error.
	var c Counts
	if err := stageTranscript(p, sp, "missing", &c); err != nil {
		t.Errorf("a session without a transcript should be skipped, got %v", err)
	}
	if c.Transcripts != 0 {
		t.Errorf("transcripts = %d, want 0 for a missing transcript", c.Transcripts)
	}

	// A transcript already present in the stage cannot be copied over.
	if err := stageTranscript(p, sp, "bbb", &c); err != nil {
		t.Fatal(err)
	}
	if err := stageTranscript(p, sp, "bbb", &c); err == nil {
		t.Error("expected an error copying a transcript onto an existing file")
	}
	if c.Transcripts != 1 {
		t.Errorf("transcripts = %d, want 1 after a failed second copy", c.Transcripts)
	}

	// The stage's projects location is blocked by a regular file.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := store.Paths{Root: filepath.Join(blocked, "usage-data"), ClaudeHome: blocked}
	var c2 Counts
	if err := stageTranscript(p, bad, "aaa", &c2); err == nil {
		t.Error("expected an error creating the project dir under a regular file")
	}
	if c2.Transcripts != 0 {
		t.Errorf("transcripts = %d, want 0 when the project dir cannot be created", c2.Transcripts)
	}
}

// A source that opens but cannot be read must surface the read failure, not
// whatever closing the half-written destination happens to report.
func TestCopyFileReportsReadFailure(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	err := copyFile(src, dst, 0o600)
	if err == nil {
		t.Fatal("expected an error copying from a directory")
	}
	if pe, ok := err.(*os.PathError); ok && pe.Err == os.ErrClosed {
		t.Errorf("copy error was masked by a close error: %v", err)
	}
}

func TestEnsureEmptyDirReportsCreateAndReadFailures(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := ensureEmptyDir(filepath.Join(file, "stage"))
	if err == nil {
		t.Fatal("expected an error when the stage cannot be created")
	}
	if got := err.Error(); len(got) < len("creating stage") || got[:len("creating stage")] != "creating stage" {
		t.Errorf("error = %q, want a creating stage failure", got)
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	unreadable := filepath.Join(t.TempDir(), "unreadable")
	if err := os.Mkdir(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o700) })
	err = ensureEmptyDir(unreadable)
	if err == nil {
		t.Fatal("expected an error when the stage cannot be read")
	}
	if got := err.Error(); len(got) < len("reading stage") || got[:len("reading stage")] != "reading stage" {
		t.Errorf("error = %q, want a reading stage failure", got)
	}
}

func TestBuildFailsWhenConfigDirUnreadable(t *testing.T) {
	p, home := fakeHome(t)
	p.ClaudeHome = filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "stage")
	c, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, nil, dst)
	if err == nil {
		t.Fatal("expected an error when the real config dir cannot be read")
	}
	if c != (Counts{}) {
		t.Errorf("counts = %+v, want zero", c)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("account file should not be staged after config sharing fails, stat err = %v", err)
	}
}

// An account file that exists but cannot be read is a real failure, unlike a
// missing one, and must stop the build rather than stage a child with no login.
func TestBuildFailsOnUnreadableAccountFile(t *testing.T) {
	p, _ := fakeHome(t)
	dst := filepath.Join(t.TempDir(), "stage")
	// A directory opens fine but cannot be copied as a file.
	if _, err := Build(Inputs{Paths: p, AccountFile: t.TempDir()}, nil, dst); err == nil {
		t.Error("expected an error when the account file cannot be copied")
	}
}

// A credentials path that exists but cannot be copied is a real failure, unlike
// an absent one, and must stop the build rather than stage a logged-out child.
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
	if _, err := Build(Inputs{Paths: p, AccountFile: filepath.Join(home, ".claude.json")}, nil, dst); err == nil {
		t.Error("expected an error when the credentials file cannot be copied")
	}
}

// Staging the same session twice must fail on the transcript copy rather than
// silently overwrite what is already in the stage.
func TestBuildFailsWhenTranscriptCannotBeStaged(t *testing.T) {
	p, home := fakeHome(t)
	// ccc has only a transcript, so nothing after the transcript step could fail.
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
		t.Fatal("expected an error when the transcript already exists in the stage")
	}
	if c != (Counts{Sessions: 2, Transcripts: 1}) {
		t.Errorf("counts = %+v, want two sessions seen and one transcript staged", c)
	}
}

// A missing source surfaces the walk error instead of creating an empty copy.
func TestCopyDirReportsMissingSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "copy")
	err := copyDir(src, dst)
	if !os.IsNotExist(err) {
		t.Fatalf("copyDir err = %v, want a not-exist error for the missing source", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("destination should not be created when the source is missing, stat err = %v", err)
	}
}

// The stage joins the session id into paths under both the real config
// directory and the stage, so an id that could escape either one stops the run
// rather than being staged.
func TestBuildRefusesAnUnusableSessionID(t *testing.T) {
	real := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stage")
	in := Inputs{
		Paths:       store.Paths{Root: filepath.Join(real, "usage-data"), ClaudeHome: real},
		AccountFile: filepath.Join(real, ".claude.json"),
	}
	sessions := []model.Session{{Meta: model.SessionMeta{SessionID: "../../../.claude"}}}
	if _, err := Build(in, sessions, dst); err == nil {
		t.Fatal("Build accepted a traversal session id")
	} else if !strings.Contains(err.Error(), "unusable id") {
		t.Fatalf("Build error = %v, want it to name the unusable id", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("a file was written beside the stage; the copy escaped it")
	}
}
