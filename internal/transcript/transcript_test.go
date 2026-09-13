package transcript

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// writeJSONL builds a synthetic transcript. All fixture content is invented;
// no real session data appears in these tests.
func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func render(t *testing.T, o Options, lines ...string) string {
	t.Helper()
	got, err := Render(writeJSONL(t, lines...), o)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return got
}

func TestRenderStringContent(t *testing.T) {
	got := render(t, Options{}, `{"type":"user","message":{"content":"fix the build"}}`)
	if got != "[User]: fix the build" {
		t.Errorf("got %q", got)
	}
}

func TestRenderArrayContent(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}}`)
	want := "[User]: one\n[User]: two"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderSkipsBlankAndUnparseableLines(t *testing.T) {
	got := render(t, Options{},
		"",
		"   ",
		`{"type":"user","message":{"content":"a"}}`,
		`{"type":"user","message":{"content":`, // truncated tail, session still live
		"not json at all",
	)
	if got != "[User]: a" {
		t.Errorf("got %q", got)
	}
}

func TestRenderSkipsWhitespaceOnlyText(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{"content":"   \n  "}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"  "}]}}`,
	)
	if got != "" {
		t.Errorf("expected empty render, got %q", got)
	}
}

func TestRenderAssistantToolOrdering(t *testing.T) {
	got := render(t, Options{},
		`{"type":"assistant","message":{"content":[`+
			`{"type":"text","text":"reading it"},`+
			`{"type":"tool_use","name":"Read"},`+
			`{"type":"text","text":"now editing"},`+
			`{"type":"tool_use","name":"Edit"},`+
			`{"type":"tool_use"}]}}`) // nameless tool_use is dropped
	want := strings.Join([]string{
		"[Assistant]: reading it",
		"[Tool: Read]",
		"[Assistant]: now editing",
		"[Tool: Edit]",
	}, "\n")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderToolResultErrors(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{"content":[`+
			`{"type":"tool_result","is_error":true},`+
			`{"type":"tool_result","is_error":false},`+
			`{"type":"tool_result"}]}}`)
	if got != "[Tool ERROR]" {
		t.Errorf("got %q", got)
	}
}

// Text and error lines come from two separate passes, so the errors trail the
// user text even when the blocks were interleaved.
func TestRenderToolResultErrorFollowsUserText(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{"content":[`+
			`{"type":"tool_result","is_error":true},`+
			`{"type":"text","text":"try again"}]}}`)
	want := "[User]: try again\n[Tool ERROR]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderInterruption(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{"content":"[Request interrupted by user for tool use]"}}`)
	want := "[User INTERRUPTED the assistant]\n[User]: [Request interrupted by user for tool use]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderCompactSummary(t *testing.T) {
	for name, line := range map[string]string{
		"envelope": `{"type":"user","isCompactSummary":true,"message":{"content":"earlier we shipped X"}}`,
		"message":  `{"type":"user","message":{"isCompactSummary":true,"content":"earlier we shipped X"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, Options{}, line)
			if got != "[Summary of earlier conversation]: earlier we shipped X" {
				t.Errorf("got %q", got)
			}
		})
	}
}

// A compact summary short-circuits the whole turn, tool results included.
func TestRenderCompactSummarySkipsToolResults(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","isCompactSummary":true,"message":{"content":[`+
			`{"type":"text","text":"recap"},{"type":"tool_result","is_error":true}]}}`)
	if got != "[Summary of earlier conversation]: recap" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTruncatesPerRole(t *testing.T) {
	got := render(t, Options{UserChars: 4, AssistantChars: 2},
		`{"type":"user","message":{"content":"abcdefgh"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"xyz"}]}}`)
	want := "[User]: abcd\n[Assistant]: xy"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Truncation must land on a rune boundary or the output is invalid UTF-8.
func TestRenderTruncatesOnRuneBoundary(t *testing.T) {
	got := render(t, Options{UserChars: 3},
		`{"type":"user","message":{"content":"héllo wörld"}}`)
	want := "[User]: hél"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderUnknownTypesIgnored(t *testing.T) {
	got := render(t, Options{},
		`{"type":"system","message":{"content":"boot"}}`,
		`{"type":"summary","summary":"whatever"}`)
	if got != "" {
		t.Errorf("expected empty render, got %q", got)
	}
}

func TestRenderElidesLongBody(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, `{"type":"user","message":{"content":"`+strings.Repeat("a", 50)+`"}}`)
	}
	got := render(t, Options{MaxChars: 100}, lines...)
	if !strings.Contains(got, ElisionMarker) {
		t.Fatalf("expected elision marker in %q", got)
	}
	if want := "\n\n[... middle of session elided for length ...]\n\n"; ElisionMarker != want {
		t.Fatalf("elision marker wording drifted: %q", ElisionMarker)
	}
	head, tail, _ := strings.Cut(got, ElisionMarker)
	if len([]rune(head)) != 50 || len([]rune(tail)) != 50 {
		t.Errorf("halves are %d and %d runes, want 50 each", len([]rune(head)), len([]rune(tail)))
	}
}

// A body exactly at the budget is kept whole.
func TestRenderDoesNotElideAtBudget(t *testing.T) {
	got := render(t, Options{MaxChars: len("[User]: hello")},
		`{"type":"user","message":{"content":"hello"}}`)
	if got != "[User]: hello" {
		t.Errorf("got %q", got)
	}
}

// Elision slices runes, not bytes, so a multi-byte body stays decodable.
func TestElideKeepsRunesIntact(t *testing.T) {
	body := strings.Repeat("é", 100)
	got := elide(body, 10)
	head, tail, _ := strings.Cut(got, ElisionMarker)
	if head != strings.Repeat("é", 5) || tail != strings.Repeat("é", 5) {
		t.Errorf("got head %q tail %q", head, tail)
	}
}

func TestRenderEmptyFile(t *testing.T) {
	got := render(t, Options{})
	if got != "" {
		t.Errorf("got %q", got)
	}
}

func TestRenderMissingFile(t *testing.T) {
	if _, err := Render(filepath.Join(t.TempDir(), "nope.jsonl"), Options{}); err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
}

func TestRenderDefaultsApplied(t *testing.T) {
	long := strings.Repeat("u", 600)
	got := render(t, Options{}, `{"type":"user","message":{"content":"`+long+`"}}`)
	if want := "[User]: " + strings.Repeat("u", DefaultUserChars); got != want {
		t.Errorf("user text not capped at the default: got %d chars", len(got))
	}
}

// claudeHome lays out the projects/<encoded path>/<session>.jsonl tree that
// store.Paths.TranscriptPath globs over.
func claudeHome(t *testing.T, sessionID string, lines ...string) store.Paths {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-tmp-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating fixture project dir: %v", err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture transcript: %v", err)
	}
	return store.Paths{Root: filepath.Join(home, "usage-data"), ClaudeHome: home}
}

func TestWriteFor(t *testing.T) {
	p := claudeHome(t, "sess-1", `{"type":"user","message":{"content":"hello"}}`)
	out := filepath.Join(t.TempDir(), "prepared")
	if err := WriteFor(p, "sess-1", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{}); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "sess-1.txt"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	want := "Session: sess-1\nDate: 2026-09-11T10:00:00Z\nProject: /tmp/fixture\n\n[User]: hello"
	if string(b) != want {
		t.Errorf("got %q, want %q", b, want)
	}
}

func TestWriteForSkipsEmptyRender(t *testing.T) {
	p := claudeHome(t, "sess-2", `{"type":"system","message":{"content":"boot"}}`)
	out := filepath.Join(t.TempDir(), "prepared")
	if err := WriteFor(p, "sess-2", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{}); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "sess-2.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no file for an empty session, stat err = %v", err)
	}
}

func TestWriteForMissingTranscript(t *testing.T) {
	p := claudeHome(t, "sess-3", `{"type":"user","message":{"content":"hi"}}`)
	err := WriteFor(p, "absent", "/tmp/fixture", "2026-09-11T10:00:00Z", t.TempDir(), Options{})
	if err == nil {
		t.Fatal("expected an error when the transcript cannot be located")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should unwrap to fs.ErrNotExist, got %v", err)
	}
}

// Rendered transcripts hold prompts and project paths, so the output directory
// and files must not be readable by anyone but the owner.
func TestWriteForPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	p := claudeHome(t, "sess-4", `{"type":"user","message":{"content":"hello"}}`)
	out := filepath.Join(t.TempDir(), "prepared")
	if err := WriteFor(p, "sess-4", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{}); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	di, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("output dir mode = %o, want 0700", got)
	}
	fi, err := os.Stat(filepath.Join(out, "sess-4.txt"))
	if err != nil {
		t.Fatalf("stat output file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("output file mode = %o, want 0600", got)
	}
}

// A destination left over from an earlier, looser run keeps its mode through a
// plain WriteFile, so overwriting has to tighten it explicitly.
func TestWriteForTightensExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	p := claudeHome(t, "sess-5", `{"type":"user","message":{"content":"hello"}}`)
	out := t.TempDir()
	stale := filepath.Join(out, "sess-5.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFor(p, "sess-5", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{}); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	fi, err := os.Stat(stale)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("overwritten file mode = %o, want 0600", got)
	}
}

func summarize(t *testing.T, id string, lines ...string) model.SessionMeta {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, id+".jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	m, err := Summarize(path)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	return m
}

// The derived record has to carry everything selection reads: an id, a project,
// a start, a duration and the two message counts.
func TestSummarize(t *testing.T) {
	// Timestamps deliberately out of order: nothing guarantees the file is
	// sorted, and the window bounds must still be the min and the max.
	m := summarize(t, "sess-summary",
		`{"type":"user","timestamp":"2026-09-10T10:30:00Z","cwd":"/Users/me/code/fixture","message":{"content":"fix the build"}}`,
		`{"type":"assistant","timestamp":"2026-09-10T10:00:00Z","cwd":"/Users/me/code/fixture","message":{"content":[{"type":"text","text":"looking"},{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"user","timestamp":"2026-09-10T11:00:00Z","message":{"content":[{"type":"text","text":"try again"}]}}`,
		`{"type":"assistant","timestamp":"2026-09-10T10:45:00Z","message":{"content":[{"type":"text","text":"done"}]}}`,
	)
	if m.SessionID != "sess-summary" {
		t.Errorf("SessionID = %q, want the filename without .jsonl", m.SessionID)
	}
	if m.ProjectPath != "/Users/me/code/fixture" {
		t.Errorf("ProjectPath = %q, want the transcript's own cwd", m.ProjectPath)
	}
	if m.StartTime != "2026-09-10T10:00:00Z" {
		t.Errorf("StartTime = %q, want the earliest timestamp as written", m.StartTime)
	}
	if m.DurationMinutes != 60 {
		t.Errorf("DurationMinutes = %v, want 60", m.DurationMinutes)
	}
	if m.UserMessageCount != 2 {
		t.Errorf("UserMessageCount = %d, want 2 (string and array form both count)", m.UserMessageCount)
	}
	if m.AssistantMsgCount != 2 {
		t.Errorf("AssistantMsgCount = %d, want 2", m.AssistantMsgCount)
	}
	// Everything the builtin derives from tool payloads stays zero rather than
	// being half-guessed from the transcript.
	if m.ToolErrors != 0 || m.InputTokens != 0 || m.GitCommits != 0 || m.FilesModified != 0 {
		t.Errorf("non-selection counters should be left zero, got %+v", m)
	}
}

// A user turn only counts when it carries text, which is the same rule Render
// uses to decide whether to emit a [User] line.
func TestSummarizeCountsOnlyTextUserTurns(t *testing.T) {
	m := summarize(t, "sess-counts",
		`{"type":"user","timestamp":"2026-09-10T10:00:00Z","message":{"content":"real ask"}}`,
		`{"type":"user","timestamp":"2026-09-10T10:01:00Z","message":{"content":"   "}}`,
		`{"type":"user","timestamp":"2026-09-10T10:02:00Z","message":{"content":[{"type":"tool_result","is_error":true}]}}`,
		`{"type":"user","timestamp":"2026-09-10T10:03:00Z","isCompactSummary":true,"message":{"content":"recap"}}`,
		`{"type":"system","timestamp":"2026-09-10T10:04:00Z","message":{"content":"boot"}}`,
	)
	if m.UserMessageCount != 1 {
		t.Errorf("UserMessageCount = %d, want 1", m.UserMessageCount)
	}
	if m.AssistantMsgCount != 0 {
		t.Errorf("AssistantMsgCount = %d, want 0", m.AssistantMsgCount)
	}
}

// The tail of a live transcript is routinely half-written, and a session may
// carry no cwd at all; neither is fatal.
func TestSummarizeTolerantOfPartialLines(t *testing.T) {
	m := summarize(t, "sess-partial",
		`{"type":"user","timestamp":"2026-09-10T10:00:00Z","message":{"content":"a"}}`,
		"",
		"not json",
		`{"type":"user","timestamp":"2026-09-10T10:05:00Z","message":{"content":`,
	)
	if m.UserMessageCount != 1 || m.ProjectPath != "" || m.StartTime != "2026-09-10T10:00:00Z" {
		t.Errorf("got %+v", m)
	}
	if m.DurationMinutes != 0 {
		t.Errorf("DurationMinutes = %v, want 0 for a single-timestamp session", m.DurationMinutes)
	}
}

func TestSummarizeMissingFile(t *testing.T) {
	if _, err := Summarize(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
}

// A destination that cannot be tightened (here a symlink loop) must stop the
// write rather than rewrite a file whose mode is unknown.
func TestWriteForChmodFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliably creatable on windows")
	}
	p := claudeHome(t, "sess-6", `{"type":"user","message":{"content":"hello"}}`)
	out := t.TempDir()
	dest := filepath.Join(out, "sess-6.txt")
	if err := os.Symlink("sess-6.txt", dest); err != nil {
		t.Fatal(err)
	}
	err := WriteFor(p, "sess-6", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{})
	if err == nil {
		t.Fatal("expected an error when the destination cannot be chmodded")
	}
	if !strings.Contains(err.Error(), "tightening") {
		t.Errorf("expected a tightening error, got %v", err)
	}
}

func TestWriteForWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not reliably creatable on windows")
	}
	p := claudeHome(t, "sess-7", `{"type":"user","message":{"content":"hello"}}`)
	out := t.TempDir()
	dest := filepath.Join(out, "sess-7.txt")
	if err := os.Symlink(filepath.Join(out, "missing", "target.txt"), dest); err != nil {
		t.Fatal(err)
	}
	err := WriteFor(p, "sess-7", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{})
	if err == nil {
		t.Fatal("expected an error when the destination cannot be written")
	}
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "writing") {
		t.Errorf("expected a writing error unwrapping to fs.ErrNotExist, got %v", err)
	}
}

// A bracketed but malformed content field must yield nothing rather than a
// partial or empty-but-allocated block list.
func TestBlocksMalformedArray(t *testing.T) {
	got := blocks([]byte(`[{"type":"text","text":"cut off"`))
	if got != nil {
		t.Errorf("got %#v, want nil for a malformed array", got)
	}
}

// A non-object element in a content array is skipped without discarding the
// blocks around it.
func TestRenderSkipsNonObjectBlock(t *testing.T) {
	got := render(t, Options{},
		`{"type":"assistant","message":{"content":[`+
			`{"type":"text","text":"before"},`+
			`"stray",`+
			`{"type":"tool_use","name":"Read"}]}}`)
	want := "[Assistant]: before\n[Tool: Read]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A user entry with no content field at all must neither fail nor emit a line.
func TestRenderUserWithoutContent(t *testing.T) {
	got := render(t, Options{},
		`{"type":"user","message":{}}`,
		`{"type":"user","message":{"content":"hi"}}`)
	if got != "[User]: hi" {
		t.Errorf("got %q", got)
	}
}

// A non-positive cap yields nothing rather than the whole string.
func TestTruncateNonPositiveLimit(t *testing.T) {
	for _, n := range []int{0, -1} {
		if got := truncate("héllo", n); got != "" {
			t.Errorf("truncate(%q, %d) = %q, want empty", "héllo", n, got)
		}
	}
}

// Opening a directory succeeds on Linux but reading it fails, which is a read
// error other than io.EOF and must surface rather than end the file quietly.
func TestForEachLineReadError(t *testing.T) {
	dir := t.TempDir()
	var got []string
	err := ForEachLine(dir, func(raw string) {
		got = append(got, raw)
	})
	if err == nil {
		t.Fatal("expected an error when the transcript cannot be read")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the transcript path, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no lines from an unreadable transcript, got %q", got)
	}
}

// A non-positive budget disables elision rather than cutting the body to nothing.
func TestElideNonPositiveMaxKeepsBody(t *testing.T) {
	body := strings.Repeat("a", 50)
	for _, max := range []int{0, -4} {
		if got := elide(body, max); got != body {
			t.Errorf("elide(body, %d) = %q, want body unchanged", max, got)
		}
	}
}

// A transcript that can be located but not read must surface as a render
// failure rather than a silently skipped session.
func TestWriteForRenderError(t *testing.T) {
	home := t.TempDir()
	// A directory where the .jsonl should be opens fine but fails on read.
	if err := os.MkdirAll(filepath.Join(home, "projects", "-tmp-fixture", "sess-6.jsonl"), 0o755); err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	p := store.Paths{Root: filepath.Join(home, "usage-data"), ClaudeHome: home}
	out := filepath.Join(t.TempDir(), "prepared")
	err := WriteFor(p, "sess-6", "/tmp/fixture", "2026-09-11T10:00:00Z", out, Options{})
	if err == nil {
		t.Fatal("expected an error when the transcript cannot be read")
	}
	if !strings.Contains(err.Error(), "rendering session sess-6") {
		t.Errorf("error should name the session being rendered, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "sess-6.txt")); !os.IsNotExist(statErr) {
		t.Errorf("expected no output file after a render failure, stat err = %v", statErr)
	}
}
