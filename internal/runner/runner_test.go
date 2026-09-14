package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/store"
)

// fakeClaude writes a script that stands in for the real binary. It records
// the environment it saw and produces the files a real /insights run would.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func realPaths(t *testing.T) store.Paths {
	t.Helper()
	ch := filepath.Join(t.TempDir(), ".claude")
	p := store.Paths{Root: filepath.Join(ch, "usage-data"), ClaudeHome: ch}
	for _, d := range []string{p.SessionMeta(), p.BuiltinFacets()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestRunCopiesReportAndHarvestsCaches(t *testing.T) {
	real := realPaths(t)
	// Pre-existing file must survive untouched.
	if err := os.WriteFile(filepath.Join(real.SessionMeta(), "old.json"), []byte(`{"v":"real"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	bin := fakeClaude(t, `
set -e
[ -z "$CLAUDECODE" ] || { echo "CLAUDECODE leaked"; exit 3; }
[ -n "$CLAUDE_CONFIG_DIR" ] || { echo "no CLAUDE_CONFIG_DIR"; exit 3; }
[ "$1" = "-p" ] && [ "$2" = "/insights" ] || { echo "bad args $*"; exit 3; }
mkdir -p "$CLAUDE_CONFIG_DIR/usage-data/session-meta" "$CLAUDE_CONFIG_DIR/usage-data/facets"
echo '<html>report</html>' > "$CLAUDE_CONFIG_DIR/usage-data/report-2026-09-11-120000.html"
echo '<html>older</html>'  > "$CLAUDE_CONFIG_DIR/usage-data/report-2026-09-10-120000.html"
echo '{"v":"stage"}' > "$CLAUDE_CONFIG_DIR/usage-data/session-meta/old.json"
echo '{"v":"new"}'   > "$CLAUDE_CONFIG_DIR/usage-data/session-meta/new.json"
echo '{"f":1}'       > "$CLAUDE_CONFIG_DIR/usage-data/facets/new.json"
echo "Your shareable insights report is ready"
`)
	out := filepath.Join(t.TempDir(), "reports", "insights-7d-2026-09-11.html")
	r, err := Run(Options{Real: real, Stage: stage, ClaudeBin: bin, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.Report)
	if err != nil || !strings.Contains(string(b), "report</html>") {
		t.Errorf("expected the NEWEST report copied to %s, got %q err=%v", out, b, err)
	}
	if r.HarvestedMeta != 1 || r.HarvestedFacets != 1 {
		t.Errorf("harvest counts = %d meta, %d facets; want 1 and 1", r.HarvestedMeta, r.HarvestedFacets)
	}
	b, _ = os.ReadFile(filepath.Join(real.SessionMeta(), "old.json"))
	if string(b) != `{"v":"real"}` {
		t.Error("harvest overwrote an existing real cache file")
	}
	if _, err := os.Stat(filepath.Join(real.SessionMeta(), "new.json")); err != nil {
		t.Error("new session-meta was not harvested into the real cache")
	}
	if r.HarvestErrors != 0 {
		t.Errorf("HarvestErrors = %d, want 0", r.HarvestErrors)
	}
	// The report quotes prompts and project names, so neither it nor the
	// directory holding it may be readable by anyone else.
	fi, err := os.Stat(r.Report)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("report mode = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(r.Report))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("report directory mode = %o, want 700", di.Mode().Perm())
	}
}

// Overwriting a report left behind with looser permissions must tighten it,
// not inherit the old mode.
func TestRunTightensAnExistingReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "insights.html")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := fakeClaude(t, `
mkdir -p "$CLAUDE_CONFIG_DIR/usage-data"
echo '<html>fresh</html>' > "$CLAUDE_CONFIG_DIR/usage-data/report-2026-09-11-120000.html"
`)
	r, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(r.Report)
	if !strings.Contains(string(b), "fresh") {
		t.Errorf("report not overwritten, got %q", b)
	}
	fi, _ := os.Stat(out)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("existing report kept mode %o; want it tightened to 600", fi.Mode().Perm())
	}
}

func TestHarvestSkipsExistingAndCountsFailures(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	for _, n := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(from, n), []byte(`{"v":"stage"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(to, "a.json"), []byte(`{"v":"real"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	copied, failed := harvest(from, to)
	if copied != 1 || failed != 0 {
		t.Errorf("harvest = %d copied, %d failed; want 1 and 0", copied, failed)
	}
	b, _ := os.ReadFile(filepath.Join(to, "a.json"))
	if string(b) != `{"v":"real"}` {
		t.Error("an existing file was overwritten")
	}
	fi, err := os.Stat(filepath.Join(to, "b.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("harvested file mode = %o, want 600", fi.Mode().Perm())
	}

	// A destination that cannot be written to is a counted failure, not a
	// silent one and not a copy.
	blocked := filepath.Join(t.TempDir(), "nope")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if copied, failed = harvest(from, filepath.Join(blocked, "sub")); copied != 0 || failed != 2 {
		t.Errorf("harvest into an unusable directory = %d copied, %d failed; want 0 and 2", copied, failed)
	}
}

func TestRunReportsNotLoggedIn(t *testing.T) {
	bin := fakeClaude(t, `echo "Not logged in · Please run /login"; exit 0`)
	_, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: filepath.Join(t.TempDir(), "r.html")})
	if err != ErrNotLoggedIn {
		t.Errorf("expected ErrNotLoggedIn, got %v", err)
	}
}

func TestRunFailsWhenNoReportProduced(t *testing.T) {
	bin := fakeClaude(t, `echo "did nothing"; exit 0`)
	_, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: filepath.Join(t.TempDir(), "r.html")})
	if err == nil || !strings.Contains(err.Error(), "no report") {
		t.Errorf("expected a no-report error, got %v", err)
	}
}

func TestChildEnvPassesTokenAndStripsNesting(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "x")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-parent-should-not-leak")
	env := childEnv(Options{Stage: "/s", Real: store.Paths{ClaudeHome: "/h/.claude"}, Token: "tok"})
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"CLAUDECODE=", "CLAUDE_CODE_SESSION_ID=", "from-parent-should-not-leak"} {
		if strings.Contains(joined, bad) {
			t.Errorf("child env must not contain %q", bad)
		}
	}
	for _, want := range []string{"CLAUDE_CONFIG_DIR=/s", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/h/.claude", "CLAUDE_CODE_OAUTH_TOKEN=tok"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child env missing %q", want)
		}
	}
}

func TestCopyIntoReportsChmodAndCopyFailures(t *testing.T) {
	// A source that opens but cannot be read, such as a directory, must fail
	// the copy rather than leave an empty destination looking successful.
	dst := filepath.Join(t.TempDir(), "out.html")
	if err := copyFile(t.TempDir(), dst, 0o600); err == nil {
		t.Error("copying from a directory succeeded; want a read error")
	}

	// A destination whose mode cannot be forced must fail rather than keep
	// looser permissions. /dev/null is not owned by the test user.
	src := filepath.Join(t.TempDir(), "src.html")
	if err := os.WriteFile(src, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, os.DevNull, 0o666); err == nil {
		t.Error("copying onto a destination that refuses chmod succeeded; want an error")
	}
}

func TestRunFailsWhenReportCannotBeWritten(t *testing.T) {
	bin := fakeClaude(t, `
mkdir -p "$CLAUDE_CONFIG_DIR/usage-data"
echo '<html>fresh</html>' > "$CLAUDE_CONFIG_DIR/usage-data/report-2026-09-11-120000.html"
`)

	// A regular file where the output directory should be cannot be created.
	blocked := filepath.Join(t.TempDir(), "nope")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: filepath.Join(blocked, "sub", "r.html")})
	if err == nil || !strings.Contains(err.Error(), "creating output directory") {
		t.Errorf("expected an output directory error, got %v", err)
	}
	if r.Report != "" {
		t.Errorf("Report = %q, want empty on failure", r.Report)
	}

	// An existing directory at the output path cannot be opened as a file.
	outDir := filepath.Join(t.TempDir(), "r.html")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err = Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: outDir})
	if err == nil || !strings.Contains(err.Error(), "copying report") {
		t.Errorf("expected a copying report error, got %v", err)
	}
	if r.Report != "" {
		t.Errorf("Report = %q, want empty on failure", r.Report)
	}
}

func TestRunReportsChildFailureWithOutput(t *testing.T) {
	bin := fakeClaude(t, `echo "boom happened"; exit 7`)
	_, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: filepath.Join(t.TempDir(), "r.html")})
	if err == nil {
		t.Fatal("expected an error from a failing child")
	}
	if !strings.Contains(err.Error(), "-p /insights") || !strings.Contains(err.Error(), "boom happened") {
		t.Errorf("error should name the command and include child output, got %v", err)
	}
	if strings.Contains(err.Error(), "no report") {
		t.Errorf("a failing child must not be reported as a missing report, got %v", err)
	}
}

func TestCopyFileMissingSourceFailsWithoutCreatingDestination(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.html")
	err := copyFile(filepath.Join(t.TempDir(), "missing.html"), dst, 0o600)
	if !os.IsNotExist(err) {
		t.Errorf("copyFile from a missing source = %v, want a not-exist error", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("destination must not be created when the source is missing, stat err = %v", statErr)
	}
}

func TestRunDefaultsToClaudeOnPath(t *testing.T) {
	bin := fakeClaude(t, `echo "Not logged in · Please run /login"; exit 0`)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), OutPath: filepath.Join(t.TempDir(), "r.html")})
	if err != ErrNotLoggedIn {
		t.Errorf("expected the claude found on PATH to run and report ErrNotLoggedIn, got %v", err)
	}
}

func TestRunPassesChildStderrThrough(t *testing.T) {
	bin := fakeClaude(t, `echo "child diagnostics" >&2; exit 1`)
	var stderr strings.Builder
	_, err := Run(Options{Real: realPaths(t), Stage: t.TempDir(), ClaudeBin: bin, OutPath: filepath.Join(t.TempDir(), "r.html"), Stderr: &stderr})
	if err == nil {
		t.Fatal("expected an error from a failing child")
	}
	if !strings.Contains(stderr.String(), "child diagnostics") {
		t.Errorf("child stderr not passed through, got %q", stderr.String())
	}
}

func TestHarvestCountsUnreadableSourceAsFailure(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "good.json"), []byte(`{"v":"stage"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink matches the glob but cannot be opened, which is a
	// failure that is not "already there".
	if err := os.Symlink(filepath.Join(from, "missing"), filepath.Join(from, "broken.json")); err != nil {
		t.Fatal(err)
	}
	copied, failed := harvest(from, to)
	if copied != 1 || failed != 1 {
		t.Errorf("harvest = %d copied, %d failed; want 1 and 1", copied, failed)
	}
}
