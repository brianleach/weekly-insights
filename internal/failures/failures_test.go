package failures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/snapshot"
	"github.com/brianleach/weekly-insights/internal/store"
)

// Every fixture here is synthetic and written into a temp directory. A test
// that read the real transcript tree would be non-deterministic and would put
// the user's own prompts and project paths into CI output.

// line builds one transcript line: an assistant tool_use, or a user tool_result.
func toolUse(id, name, command string) string {
	input := "{}"
	if command != "" {
		b, _ := json.Marshal(map[string]string{"command": command})
		input = string(b)
	}
	b, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": name, "input": json.RawMessage(input),
		}}},
	})
	return string(b)
}

func toolResult(id, text string, isError bool) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": text,
		}}},
	})
	return string(b)
}

// blockResult writes the array form of tool_result content, which some tools
// emit instead of a bare string.
func blockResult(id, text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": id, "is_error": true,
			"content": []any{map[string]any{"type": "text", "text": text}},
		}}},
	})
	return string(b)
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	return path
}

func scan(t *testing.T, lines ...string) []Failure {
	t.Helper()
	fs, err := ScanFile(writeTranscript(t, lines...), "abcdefgh-1234", "2026-09-10", "/p")
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	return fs
}

const denialText = "Permission for this action was denied by the Claude Code auto mode " +
	"classifier. Reason: [Credential Leakage]. If you have other tasks ..."

func TestClassifyCoversEveryClass(t *testing.T) {
	cases := []struct {
		name, tool, command, text string
		wantClass, wantDetail     string
	}{
		{"classifier", "Bash", "cat .env", denialText, ClassClassifierDenied, "Credential Leakage"},
		{"hook", "Bash", "git push", "PreToolUse:Bash hook ~/.claude/hooks/require-review-before-request.sh blocked this",
			ClassHookBlocked, "require-review-before-request.sh"},
		{"hook error", "Bash", "git push", "hook error: exited 1", ClassHookBlocked, ""},
		{"harness sleep", "Bash", "sleep 45 && ls",
			"<tool_use_error>Blocked: sleep 45 followed by: ls. Use Monitor.</tool_use_error>",
			ClassHarnessRule, "sleep_chain"},
		{"harness read", "Edit", "",
			"<tool_use_error>File has not been read yet. Read it first.</tool_use_error>",
			ClassHarnessRule, "edit_before_read"},
		{"harness worktree", "Bash", "cd x && ls",
			"<tool_use_error>This command is too complex to verify inside the worktree</tool_use_error>",
			ClassHarnessRule, "worktree_verify"},
		{"harness background", "Bash", "tail -f log",
			"<tool_use_error>Use run_in_background instead</tool_use_error>",
			ClassHarnessRule, "run_in_background"},
		{"api 503", "WebFetch", "", "server responded 503 Service Unavailable", ClassAPIFlake, ""},
		{"api 529", "Bash", "gh pr view", "Error 529 overloaded", ClassAPIFlake, ""},
		{"api rate limit", "mcp__linear__list_issues", "", "rate limit exceeded", ClassAPIFlake, ""},
		{"api sentry", "mcp__sentry__search_issues", "",
			"There was an error communicating with the Sentry API", ClassAPIFlake, ""},
		{"mcp", "mcp__linear__save_issue", "", "input validation failed for field title", ClassMCPError, "linear"},
		{"shell", "Bash", "git -C /repo status", "Exit code 128\nfatal: not a git repository", ClassShellError, "git"},
		{"other", "Read", "", "Exit code 1\nsomething unclassifiable", ClassOther, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			class, detail := Classify(c.tool, c.command, c.text)
			if class != c.wantClass || detail != c.wantDetail {
				t.Fatalf("Classify = (%q, %q), want (%q, %q)", class, detail, c.wantClass, c.wantDetail)
			}
		})
	}
}

// The rule set is ordered and first match wins, so a message that satisfies
// two rules must land in the earlier one. Without this the taxonomy drifts
// whenever a message happens to carry two markers.
func TestClassifyPrecedence(t *testing.T) {
	cases := []struct{ name, tool, text, want string }{
		{"denial mentioning 503", "Bash", denialText + " status 503", ClassClassifierDenied},
		{"hook on an mcp tool", "mcp__linear__save_issue", "PreToolUse: blocked", ClassHookBlocked},
		{"harness beats shell", "Bash", "<tool_use_error>sleep chain</tool_use_error>", ClassHarnessRule},
		{"flake beats mcp", "mcp__sentry__search_issues", "Overloaded", ClassAPIFlake},
		{"flake beats shell", "Bash", "curl: got 502 from upstream", ClassAPIFlake},
		{"mcp beats other", "mcp__notion__notion-fetch", "object not found", ClassMCPError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := Classify(c.tool, "cmd", c.text); got != c.want {
				t.Fatalf("Classify = %q, want %q", got, c.want)
			}
		})
	}
}

// A bare 5xx is a flake, but a three-digit run inside a longer number is not.
func TestAPIFlakeDoesNotFireOnLongNumbers(t *testing.T) {
	if got, _ := Classify("Bash", "wc -l", "Exit code 1\n1500234 lines"); got != ClassShellError {
		t.Fatalf("Classify = %q, want %q", got, ClassShellError)
	}
}

func TestShape(t *testing.T) {
	cases := []struct{ command, want string }{
		{"", ""},
		{"go test ./...", ShapeSimple},
		{"git status && git log", ShapeCompound},
		{"ls; pwd", ShapeCompound},
		{"cat x | wc -l", ShapeCompound},
		{"cd /tmp && ls", ShapeCDPrefix},
		{"cat <<'EOF' > f\nhi\nEOF", ShapeHeredoc},
		{"echo one\necho two", ShapeMultiline},
		// A heredoc that also has a cd prefix is a heredoc: the more specific
		// shape is the one that says what is wrong with the command.
		{"cd /tmp && cat <<EOF\nx\nEOF", ShapeHeredoc},
	}
	for _, c := range cases {
		if got := Shape(c.command); got != c.want {
			t.Fatalf("Shape(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestLeadingCommand(t *testing.T) {
	cases := []struct{ command, want string }{
		{"", ""},
		{"git status", "git"},
		{"cd /Users/x/repo && go build ./...", "go"},
		{"  cd /tmp  &&  rake db:migrate  ", "rake"},
		{"FOO=1 bundle exec rspec", "bundle"},
		{"gh pr list | head", "gh"},
	}
	for _, c := range cases {
		if got := LeadingCommand(c.command); got != c.want {
			t.Fatalf("LeadingCommand(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Exit code 1\nfatal: not a git repository", "Exit code N fatal: not a git repository"},
		{"failed to fetch https://api.github.com/repos/x/y", "failed to fetch <url>"},
		{"cannot stat /Users/someone/code/repo/file.go", "cannot stat <path>"},
		{"bad object 1a2b3c4d5e6f7890", "bad object <hash>"},
		{"<tool_use_error>Blocked: sleep 45</tool_use_error>", "Blocked: sleep N"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Fatalf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Two messages that differ only in a path, a number or a hash must collapse to
// one signature, which is the whole point of the top-signatures table.
func TestNormalizeCollapsesVariants(t *testing.T) {
	a := Normalize("Exit code 128\nfatal: not a git repository: /Users/a/one/.git")
	b := Normalize("Exit code 128\nfatal: not a git repository: /Users/b/two/.git")
	if a != b {
		t.Fatalf("signatures differ: %q vs %q", a, b)
	}
}

// Pairing is by tool_use_id, not by position: results arrive out of order and
// interleaved with other calls, so a positional pairing would attribute
// commands to the wrong failure.
func TestScanPairsByToolUseID(t *testing.T) {
	fs := scan(t,
		toolUse("t1", "Bash", "git status"),
		toolUse("t2", "Bash", "cd /tmp && cat .env"),
		toolResult("t2", denialText, true),
		toolResult("t1", "Exit code 128\nfatal: not a git repository", true),
		toolResult("t3", "fine", false),
	)
	if len(fs) != 2 {
		t.Fatalf("got %d failures, want 2", len(fs))
	}
	if fs[0].Command != "cd /tmp && cat .env" || fs[0].Class != ClassClassifierDenied {
		t.Fatalf("first failure = %+v", fs[0])
	}
	if fs[1].Command != "git status" || fs[1].Class != ClassShellError || fs[1].Detail != "git" {
		t.Fatalf("second failure = %+v", fs[1])
	}
	if fs[0].Session != "abcdefgh" || fs[0].Date != "2026-09-10" || fs[0].Project != "/p" {
		t.Fatalf("session metadata not carried: %+v", fs[0])
	}
}

// A result whose tool_use is missing (the call was compacted away, or the
// transcript starts mid-session) is still counted; it just has no tool name.
func TestScanUnpairedResultStillCounts(t *testing.T) {
	fs := scan(t, toolResult("gone", "Exit code 1\nboom", true))
	if len(fs) != 1 || fs[0].Class != ClassOther || fs[0].Tool != "" {
		t.Fatalf("got %+v", fs)
	}
}

func TestScanReadsBlockFormContent(t *testing.T) {
	fs := scan(t,
		toolUse("t1", "mcp__linear__save_issue", ""),
		blockResult("t1", "input validation failed"),
	)
	if len(fs) != 1 || fs[0].Class != ClassMCPError || fs[0].Detail != "linear" {
		t.Fatalf("got %+v", fs)
	}
}

// The transcript is appended to while a session runs, so a half-written tail
// must not lose the lines before it.
func TestScanToleratesUnparseableLines(t *testing.T) {
	fs := scan(t,
		toolUse("t1", "Bash", "ls /nope"),
		toolResult("t1", "Exit code 1\nNo such file or directory", true),
		`{"type":"user","message":{"content":[{"type":"too`,
	)
	if len(fs) != 1 || fs[0].ShellSignature != "no_such_file" {
		t.Fatalf("got %+v", fs)
	}
}

func TestSelfInflictedSplit(t *testing.T) {
	fs := scan(t,
		// Self-inflicted: a denial of a command whose shape defeats allow rules.
		toolUse("a", "Bash", "cd /tmp && cat x"), toolResult("a", denialText, true),
		// External: a denial of a plain command is the classifier's call.
		toolUse("b", "Bash", "cat x"), toolResult("b", denialText, true),
		// Self-inflicted: a harness guardrail.
		toolUse("c", "Bash", "sleep 5 && ls"),
		toolResult("c", "<tool_use_error>Blocked: sleep 5</tool_use_error>", true),
		// Self-inflicted: a recognized shell signature.
		toolUse("d", "Bash", "if [ $x == 1 ]; then ls; fi"),
		toolResult("d", "Exit code 1\n(eval):1: == not found", true),
		// External: a non-zero exit that is the program's real verdict.
		toolUse("e", "Bash", "go test ./..."), toolResult("e", "Exit code 1\nFAIL", true),
		// External: upstream weather.
		toolUse("f", "mcp__sentry__search_issues", ""),
		toolResult("f", "There was an error communicating with the Sentry API", true),
	)
	s := Summarize(fs, 3)
	if s.Total != 6 || s.SelfInflicted != 3 || s.External != 3 {
		t.Fatalf("summary = %+v", s)
	}
	if s.PerSession != 2 {
		t.Fatalf("per session = %v, want 2", s.PerSession)
	}
	if s.SelfInflicted+s.External != s.Total {
		t.Fatalf("split does not sum to the total: %+v", s)
	}
}

func TestSummarizeClassOrderAndShapes(t *testing.T) {
	fs := scan(t,
		toolUse("a", "Bash", "cd /tmp && cat x"), toolResult("a", denialText, true),
		toolUse("b", "Bash", "cat <<EOF\nx\nEOF"), toolResult("b", denialText, true),
		toolUse("c", "Bash", "cat x"), toolResult("c", denialText, true),
		toolUse("d", "Bash", "cd /var && cat y"), toolResult("d", denialText, true),
		toolUse("e", "Bash", "go build ./..."), toolResult("e", "Exit code 2\nbroken", true),
	)
	s := Summarize(fs, 2)
	// Classes print in rule order, which is the precedence, not by count.
	if len(s.ByClass) != 2 || s.ByClass[0].Class != ClassClassifierDenied ||
		s.ByClass[1].Class != ClassShellError {
		t.Fatalf("by class = %+v", s.ByClass)
	}
	if s.ByClass[0].Count != 4 || s.ByClass[0].PerSession != 2 {
		t.Fatalf("classifier row = %+v", s.ByClass[0])
	}
	want := []Count{{ShapeCDPrefix, 2}, {ShapeHeredoc, 1}, {ShapeSimple, 1}}
	if len(s.Shapes) != len(want) {
		t.Fatalf("shapes = %+v, want %+v", s.Shapes, want)
	}
	for i, c := range want {
		if s.Shapes[i] != c {
			t.Fatalf("shapes = %+v, want %+v", s.Shapes, want)
		}
	}
}

// Ties break on the key, so two runs over the same window produce the same
// ordering. Go map iteration is randomized, so without the secondary key this
// output would shuffle between runs and read as a change.
func TestSummarizeOrderingIsDeterministic(t *testing.T) {
	fs := scan(t,
		toolUse("a", "Bash", "alpha"), toolResult("a", "Exit code 1\nzeta failed", true),
		toolUse("b", "Bash", "beta"), toolResult("b", "Exit code 1\nalpha failed", true),
		toolUse("c", "Bash", "gamma"), toolResult("c", "Exit code 1\nmid failed", true),
		toolUse("d", "Bash", "delta"), toolResult("d", "Exit code 1\nmid failed", true),
	)
	first := Summarize(fs, 1)
	for i := 0; i < 20; i++ {
		got := Summarize(fs, 1)
		if len(got.TopSignatures) != len(first.TopSignatures) {
			t.Fatalf("signature count changed between runs")
		}
		for j := range got.TopSignatures {
			if got.TopSignatures[j] != first.TopSignatures[j] {
				t.Fatalf("run %d differs at %d: %+v vs %+v", i, j,
					got.TopSignatures[j], first.TopSignatures[j])
			}
		}
	}
	// Count descending first, then key ascending.
	if first.TopSignatures[0].Count != 2 ||
		!strings.Contains(first.TopSignatures[0].Signature, "mid failed") {
		t.Fatalf("top row = %+v", first.TopSignatures[0])
	}
	if !strings.Contains(first.TopSignatures[1].Signature, "alpha failed") {
		t.Fatalf("second row = %+v", first.TopSignatures[1])
	}
}

func TestSummarizeCapsSignaturesAndTruncatesExamples(t *testing.T) {
	long := "echo " + strings.Repeat("x", 400)
	var lines []string
	for i := 0; i < topSignatures+5; i++ {
		id := string(rune('a' + i))
		lines = append(lines,
			toolUse(id, "Bash", long),
			toolResult(id, "Exit code 1\nfailure kind "+strings.Repeat("k", i), true))
	}
	s := Summarize(scan(t, lines...), 1)
	if len(s.TopSignatures) != topSignatures {
		t.Fatalf("got %d signatures, want %d", len(s.TopSignatures), topSignatures)
	}
	for _, sig := range s.TopSignatures {
		if len([]rune(sig.Example)) > maxCommandChars {
			t.Fatalf("example not truncated: %d runes", len([]rune(sig.Example)))
		}
	}
}

func TestSummarizeEmptyWindow(t *testing.T) {
	s := Summarize(nil, 0)
	if s.Total != 0 || s.PerSession != 0 || len(s.ByClass) != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if got := Report(s, "2026-09-01 .. 2026-09-08  (7d)"); !strings.Contains(got, "(none)") {
		t.Fatalf("report = %q", got)
	}
}

// The JSON output is the input to the trend, so its shape is part of the
// contract: fixed keys, arrays rather than maps, and no randomized ordering.
func TestJSONShape(t *testing.T) {
	fs := scan(t,
		toolUse("a", "Bash", "cd /tmp && cat x"), toolResult("a", denialText, true),
		toolUse("b", "Bash", "go build"), toolResult("b", "Exit code 2\nbroken", true),
	)
	b, err := json.Marshal(Summarize(fs, 2))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"sessions", "total", "per_session", "self_inflicted",
		"external", "by_class", "classifier_denied_shapes", "top_signatures"} {
		if _, ok := round[k]; !ok {
			t.Fatalf("key %q missing from %s", k, b)
		}
	}
	if _, ok := round["by_class"].([]any); !ok {
		t.Fatalf("by_class is not an array: %s", b)
	}
	// Byte-identical across runs: the ordering is fixed, not map order.
	for i := 0; i < 10; i++ {
		again, _ := json.Marshal(Summarize(fs, 2))
		if string(again) != string(b) {
			t.Fatalf("JSON differs between runs:\n%s\n%s", b, again)
		}
	}
}

func TestReportTextIsAlignedAndPlain(t *testing.T) {
	fs := scan(t,
		toolUse("a", "Bash", "cd /tmp && cat x"), toolResult("a", denialText, true),
	)
	got := Report(Summarize(fs, 4), "2026-09-01 .. 2026-09-08  (7d)")
	for _, want := range []string{
		"window       : 2026-09-01 .. 2026-09-08  (7d)",
		"sessions     : 4 substantive",
		"failures     : 1 total, 0.25 per session",
		"split        : 1 self-inflicted, 0 external",
		"classifier_denied",
		"classifier_denied by command shape:",
		"example: cd /tmp && cat x",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\u2014") {
		t.Fatalf("report contains an em dash:\n%s", got)
	}
}

// Collect walks a real Paths tree, so a session whose transcript was rotated
// away must be skipped rather than failing the window.
func TestCollectSkipsMissingTranscripts(t *testing.T) {
	root := t.TempDir()
	p := store.Paths{Root: root, ClaudeHome: filepath.Join(root, "claude")}
	dir := filepath.Join(p.Transcripts(), "-tmp-project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		toolUse("a", "Bash", "cat .env"),
		toolResult("a", denialText, true),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "have-one.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sessions := []model.Session{
		{Meta: model.SessionMeta{SessionID: "have-one", StartTime: "2026-09-10T09:00:00Z", ProjectPath: "/tmp/project"}},
		{Meta: model.SessionMeta{SessionID: "gone", StartTime: "2026-09-10T10:00:00Z"}},
	}
	fs := Collect(p, sessions)
	if len(fs) != 1 || fs[0].Class != ClassClassifierDenied || fs[0].Session != "have-one" {
		t.Fatalf("collected %+v", fs)
	}
}

// The snapshot field is additive: a snapshot written before it existed must
// still load, and one written with it must round trip unchanged.
func TestSnapshotRoundTrip(t *testing.T) {
	fs := scan(t,
		toolUse("a", "Bash", "cd /tmp && cat x"), toolResult("a", denialText, true),
		toolUse("b", "Bash", "go build"), toolResult("b", "Exit code 2\nbroken", true),
	)
	snap := snapshot.Snapshot{Window: snapshot.Window{Label: "2026-09-10", Days: 7}}
	snap.Failures = SnapshotSummary(Summarize(fs, 2))

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back snapshot.Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Failures == nil {
		t.Fatalf("failures block lost in round trip: %s", b)
	}
	if back.Failures.Total != 2 || back.Failures.SelfInflicted != 1 || back.Failures.External != 1 {
		t.Fatalf("failures = %+v", back.Failures)
	}
	if back.Failures.ByClass[ClassClassifierDenied] != 1 || back.Failures.ByClass[ClassShellError] != 1 {
		t.Fatalf("by class = %+v", back.Failures.ByClass)
	}
	if back.Failures.PerSession != 1 {
		t.Fatalf("per session = %v", back.Failures.PerSession)
	}
}

func TestSnapshotWithoutFailuresStillLoads(t *testing.T) {
	var old snapshot.Snapshot
	if err := json.Unmarshal([]byte(`{"window":{"label":"2020-03-08","days":7}}`), &old); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if old.Failures != nil {
		t.Fatalf("failures = %+v, want nil", old.Failures)
	}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "failures") {
		t.Fatalf("absent block was written back: %s", b)
	}
}

func TestDecodeBlocksRejectsNonArrayAndMalformedContent(t *testing.T) {
	// Content that is not array-form, even if JSON would accept it as one
	// after leading whitespace, is not block content.
	if got := decodeBlocks(json.RawMessage(` [{"type":"text","text":"x"}]`)); got != nil {
		t.Fatalf("decodeBlocks(non-array) = %+v, want nil", got)
	}
	// A truncated array cannot be decoded at all.
	if got := decodeBlocks(json.RawMessage(`[{"type":"text"},`)); got != nil {
		t.Fatalf("decodeBlocks(malformed) = %+v, want nil", got)
	}
}

// A transcript that exists but cannot be read is skipped the same way a
// missing one is, and the sessions after it are still counted.
func TestCollectSkipsUnreadableTranscripts(t *testing.T) {
	root := t.TempDir()
	p := store.Paths{Root: root, ClaudeHome: filepath.Join(root, "claude")}
	dir := filepath.Join(p.Transcripts(), "-tmp-project")
	// A directory where the transcript file should be opens but fails to read.
	if err := os.MkdirAll(filepath.Join(dir, "broken.jsonl"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		toolUse("a", "Bash", "go build"),
		toolResult("a", "Exit code 2\nbroken", true),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "good-one.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if p.TranscriptPath("broken") == "" {
		t.Fatalf("expected a path for the unreadable transcript")
	}
	sessions := []model.Session{
		{Meta: model.SessionMeta{SessionID: "broken", StartTime: "2026-09-10T08:00:00Z"}},
		{Meta: model.SessionMeta{SessionID: "good-one", StartTime: "2026-09-11T09:00:00Z", ProjectPath: "/tmp/project"}},
	}
	fs := Collect(p, sessions)
	if len(fs) != 1 || fs[0].Session != "good-one" || fs[0].Class != ClassShellError || fs[0].Date != "2026-09-11" {
		t.Fatalf("collected %+v", fs)
	}
}

// Blank and whitespace-only lines appear between entries when a writer flushes
// a bare newline; they must be skipped without disturbing pairing.
func TestScanSkipsBlankLines(t *testing.T) {
	fs := scan(t,
		"",
		toolUse("t1", "Bash", "ls /nope"),
		"   \t  ",
		"",
		toolResult("t1", "Exit code 1\nNo such file or directory", true),
	)
	if len(fs) != 1 || fs[0].Command != "ls /nope" || fs[0].Class != ClassShellError {
		t.Fatalf("got %+v", fs)
	}
}

// One malformed element must be dropped on its own without losing its
// siblings. The bad element here is a partial decode: a type mismatch on one
// field still fills the others, so keeping it would count a phantom failure.
func TestScanSkipsMalformedContentElement(t *testing.T) {
	bad := `{"type":"user","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"Exit code 1\nphantom","text":5},` +
		`{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"Exit code 1\nreal"}` +
		`]}}`
	fs := scan(t, toolUse("t1", "Bash", "ls"), bad)
	if len(fs) != 1 || !strings.Contains(fs[0].Signature, "real") {
		t.Fatalf("got %+v", fs)
	}
}

// A tool_result with no content field but a top-level text field still
// carries its error message into classification.
func TestScanReadsTextFieldWhenContentMissing(t *testing.T) {
	b, _ := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "t1", "is_error": true, "text": denialText,
		}}},
	})
	fs := scan(t, toolUse("t1", "Bash", "cat .env"), string(b))
	if len(fs) != 1 || fs[0].Class != ClassClassifierDenied || fs[0].Detail != "Credential Leakage" {
		t.Fatalf("got %+v", fs)
	}
	if fs[0].Signature != Normalize(denialText) {
		t.Fatalf("signature = %q, want %q", fs[0].Signature, Normalize(denialText))
	}
}

// Malformed string-form content yields no text rather than the raw bytes or
// the block's text field, so a corrupt result never leaks into a signature.
func TestResultTextMalformedStringContent(t *testing.T) {
	b := block{Content: json.RawMessage(`"unterminated`), Text: "fallback"}
	if got := resultText(b); got != "" {
		t.Fatalf("resultText = %q, want empty", got)
	}
}

// Only an object-form input is decoded. Anything else, including an input
// that is not a JSON object at its first byte, yields no command.
func TestBashCommandIgnoresNonObjectInput(t *testing.T) {
	cases := []string{"", `"ls -la"`, ` {"command":"ls -la"}`}
	for _, in := range cases {
		if got := bashCommand(json.RawMessage(in)); got != "" {
			t.Fatalf("bashCommand(%q) = %q, want empty", in, got)
		}
	}
	if got := bashCommand(json.RawMessage(`{"command":"ls -la"}`)); got != "ls -la" {
		t.Fatalf("bashCommand(object) = %q, want %q", got, "ls -la")
	}
}
