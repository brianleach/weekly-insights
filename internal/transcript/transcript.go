// Package transcript renders a raw Claude Code session .jsonl into the plain
// text an LLM reads to extract facets.
//
// The rendering is deliberately lossy and deterministic: user turns are capped,
// assistant turns capped harder, and tool calls collapse to their names. Facet
// extraction cares about the shape of a session (what was asked, what went
// wrong, how the user reacted) and not about tool payloads, which otherwise
// dominate the token budget. Over-long sessions are head+tail sampled rather
// than summarized so that two runs over the same transcript produce identical
// input.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
)

// Defaults mirror the builtin /insights extractor's budget.
const (
	DefaultUserChars      = 500
	DefaultAssistantChars = 300
	DefaultMaxChars       = 28000
)

// ElisionMarker separates the head and tail halves of a sampled session. The
// extraction prompt keys on this exact wording to know the middle is missing,
// so it must not drift.
const ElisionMarker = "\n\n[... middle of session elided for length ...]\n\n"

// Options tunes the per-turn and whole-session budgets. A zero field takes the
// corresponding default, so the zero Options is the normal configuration.
type Options struct {
	UserChars, AssistantChars, MaxChars int
}

func (o Options) withDefaults() Options {
	if o.UserChars <= 0 {
		o.UserChars = DefaultUserChars
	}
	if o.AssistantChars <= 0 {
		o.AssistantChars = DefaultAssistantChars
	}
	if o.MaxChars <= 0 {
		o.MaxChars = DefaultMaxChars
	}
	return o
}

// entry is one line of the transcript. Only the fields that affect rendering
// are decoded; message.content is held raw because it is polymorphic.
type entry struct {
	Type string `json:"type"`
	// Cwd and Timestamp are written by the CLI on every entry. Summarize reads
	// them; rendering ignores them.
	Cwd       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	// isCompactSummary has appeared on both the envelope and the message
	// depending on the writer's version, so both are read.
	IsCompactSummary bool `json:"isCompactSummary"`
	Message          struct {
		IsCompactSummary bool            `json:"isCompactSummary"`
		Content          json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one element of an array-form content field.
type block struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error"`
}

// blocks decodes array-form content. A string-form or malformed content field
// yields nothing, which is what callers that only care about blocks want.
func blocks(raw json.RawMessage) []block {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	// Decode one element at a time: a single non-object element (the format
	// does not forbid one) must not discard the rest of the array.
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil
	}
	out := make([]block, 0, len(elems))
	for _, e := range elems {
		var b block
		if err := json.Unmarshal(e, &b); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// texts pulls the renderable text out of a content field, capped, skipping
// blocks that are empty or whitespace only.
func texts(raw json.RawMessage, limit int) []string {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || strings.TrimSpace(s) == "" {
			return nil
		}
		return []string{truncate(s, limit)}
	}
	var out []string
	for _, b := range blocks(raw) {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			out = append(out, truncate(b.Text, limit))
		}
	}
	return out
}

// truncate cuts s to at most n runes. Counting runes rather than bytes keeps
// the cap from splitting a multi-byte character into mojibake, and matches the
// character-based slicing of the Python reference.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// Render reads a .jsonl transcript and returns the rendered text, or "" if the
// session produced nothing renderable.
func Render(path string, o Options) (string, error) {
	o = o.withDefaults()
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening transcript %s: %w", path, err)
	}
	defer f.Close()

	var lines []string
	r := bufio.NewReader(f)
	for {
		raw, err := r.ReadString('\n')
		if raw != "" {
			lines = append(lines, renderLine(raw, o)...)
		}
		if err != nil {
			// A transcript is appended to while the session runs, so the last
			// line is routinely partial; io.EOF just ends the file.
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("reading transcript %s: %w", path, err)
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return elide(strings.Join(lines, "\n"), o.MaxChars), nil
}

// renderLine turns one JSON line into zero or more output lines. Blank and
// unparseable lines yield nothing: the file is written concurrently by the
// running CLI and a half-written tail must not fail the whole render.
func renderLine(raw string, o Options) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var e entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nil
	}

	var out []string
	switch e.Type {
	case "user":
		if e.IsCompactSummary || e.Message.IsCompactSummary {
			// A compaction replaces the conversation so far; labeling it as a
			// user turn would read as something the user actually said.
			for _, s := range texts(e.Message.Content, o.UserChars) {
				out = append(out, "[Summary of earlier conversation]: "+s)
			}
			return out
		}
		for _, s := range texts(e.Message.Content, o.UserChars) {
			// An interruption arrives as a synthetic user turn. Flagging it on
			// its own line makes the friction countable downstream.
			if strings.Contains(s, "[Request interrupted by user") {
				out = append(out, "[User INTERRUPTED the assistant]")
			}
			out = append(out, "[User]: "+s)
		}
		// Tool results ride along on user turns. Only the failures matter.
		for _, b := range blocks(e.Message.Content) {
			if b.Type == "tool_result" && b.IsError {
				out = append(out, "[Tool ERROR]")
			}
		}
	case "assistant":
		// Order between prose and tool calls is the signal here: it shows
		// whether the assistant explained itself before acting.
		for _, b := range blocks(e.Message.Content) {
			switch {
			case b.Type == "text" && strings.TrimSpace(b.Text) != "":
				out = append(out, "[Assistant]: "+truncate(b.Text, o.AssistantChars))
			case b.Type == "tool_use" && b.Name != "":
				out = append(out, "[Tool: "+b.Name+"]")
			}
		}
	}
	return out
}

// elide head+tail samples an over-long body. The two ends carry the ask and the
// outcome, which is what facet extraction needs; the middle is mostly tool
// churn. Cutting on rune boundaries keeps the halves valid UTF-8.
func elide(body string, max int) string {
	if max <= 0 {
		return body
	}
	runes := []rune(body)
	if len(runes) <= max {
		return body
	}
	half := max / 2
	return string(runes[:half]) + ElisionMarker + string(runes[len(runes)-half:])
}

// WriteFor renders one session to dir/<sessionID>.txt with a header. A session
// that renders empty is skipped rather than written as a header-only file.
func WriteFor(p store.Paths, sessionID, projectPath, start, dir string, o Options) error {
	path := p.TranscriptPath(sessionID)
	if path == "" {
		return fmt.Errorf("locating transcript for session %s: %w", sessionID, os.ErrNotExist)
	}
	body, err := Render(path, o)
	if err != nil {
		return fmt.Errorf("rendering session %s: %w", sessionID, err)
	}
	if body == "" {
		return nil
	}
	// 0700/0600 throughout: a rendered transcript carries the user's prompts and
	// project paths, so nothing here is group- or world-readable.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating transcript directory %s: %w", dir, err)
	}
	header := fmt.Sprintf("Session: %s\nDate: %s\nProject: %s\n\n", sessionID, start, projectPath)
	out := filepath.Join(dir, sessionID+".txt")
	// WriteFile keeps the mode of a file that already exists, so a destination
	// left over from an earlier, looser run is tightened before it is rewritten.
	if err := os.Chmod(out, 0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tightening %s: %w", out, err)
	}
	if err := os.WriteFile(out, []byte(header+body), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	return nil
}

// Summarize derives the minimal session metadata selection needs directly from
// a transcript. It exists because the builtin's session-meta cache is only
// written when /insights runs, and is capped per run: a session with a
// transcript but no cached record would otherwise be invisible to every report.
//
// Only the fields that selection reads are filled in. The counters the builtin
// derives from tool payloads (tokens, diffs, tool errors) are left zero rather
// than half-guessed, so a derived record is never mistaken for a complete one.
func Summarize(path string) (model.SessionMeta, error) {
	m := model.SessionMeta{
		SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	f, err := os.Open(path)
	if err != nil {
		return model.SessionMeta{}, fmt.Errorf("opening transcript %s: %w", path, err)
	}
	defer f.Close()

	var first, last time.Time
	// Same reader as Render: transcript lines routinely exceed bufio.Scanner's
	// 64KB token limit, so ReadString is the only safe line source here.
	r := bufio.NewReader(f)
	for {
		raw, rerr := r.ReadString('\n')
		if raw != "" {
			summarizeLine(raw, &m, &first, &last)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return model.SessionMeta{}, fmt.Errorf("reading transcript %s: %w", path, rerr)
		}
	}
	if !first.IsZero() && !last.IsZero() {
		m.DurationMinutes = last.Sub(first).Minutes()
	}
	return m, nil
}

// summarizeLine folds one JSON line into the summary. Blank and unparseable
// lines are ignored for the same reason renderLine ignores them: the file is
// appended to while the session runs, so its tail is routinely half-written.
func summarizeLine(raw string, m *model.SessionMeta, first, last *time.Time) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	var e entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return
	}
	if m.ProjectPath == "" && e.Cwd != "" {
		m.ProjectPath = e.Cwd
	}
	if ts, ok := parseTime(e.Timestamp); ok {
		// Entries are usually in order but nothing guarantees it, so the window
		// bounds are taken as a min and a max rather than as the first and last
		// lines of the file.
		if first.IsZero() || ts.Before(*first) {
			*first = ts
			m.StartTime = e.Timestamp // kept exactly as written
		}
		if last.IsZero() || ts.After(*last) {
			*last = ts
		}
	}
	switch e.Type {
	case "user":
		// A compaction is not something the user said, matching how Render
		// labels it, so it does not count as a user message.
		if e.IsCompactSummary || e.Message.IsCompactSummary {
			return
		}
		// Same emptiness rule Render uses to decide whether to emit a [User]
		// line; the cap is irrelevant here, only whether any text survives.
		if len(texts(e.Message.Content, 1)) > 0 {
			m.UserMessageCount++
		}
	case "assistant":
		m.AssistantMsgCount++
	}
}

// parseTime reads an entry timestamp. The CLI writes RFC3339 with a zone; the
// Nano layout accepts both the fractional and non-fractional forms.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
