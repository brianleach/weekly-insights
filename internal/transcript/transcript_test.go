package transcript

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
