// Package failures counts and classifies failed tool calls in a window.
//
// The builtin report counts tool errors as one undifferentiated number, which
// cannot tell a denied permission check from a mistyped shell command from an
// upstream 503. Those have different fixes and different owners, so a count
// that merges them cannot show whether any fix worked.
//
// A failure here is a user-turn content block of type "tool_result" carrying
// is_error, paired back to the assistant "tool_use" block that produced it by
// tool_use_id. Pairing is what makes the tool name and, for Bash, the command
// available at classification time; the error text alone is often ambiguous
// about which tool even failed.
package failures

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/transcript"
)

// maxCommandChars caps the example command quoted in a signature row. Long
// enough to recognize the shape, short enough that fifteen rows stay readable.
const maxCommandChars = 140

// topSignatures is how many normalized messages the report lists.
const topSignatures = 15

// Failure is one failed tool call, already paired with its call.
type Failure struct {
	Session string `json:"session"`
	Date    string `json:"date"`
	Project string `json:"project"`
	Class   string `json:"class"`
	Tool    string `json:"tool"`
	// Detail names the thing inside the class: the hook, the MCP server, the
	// leading shell command. Empty when the class has nothing further to say.
	Detail string `json:"detail,omitempty"`
	// ShellSignature is the recognized shell failure mode, when one matched.
	// Its presence is what makes a shell error self-inflicted rather than an
	// ordinary non-zero exit from a command that did its job.
	ShellSignature string `json:"shell_signature,omitempty"`
	Command        string `json:"command,omitempty"`
	Shape          string `json:"shape,omitempty"`
	Signature      string `json:"signature"`
	SelfInflicted  bool   `json:"self_inflicted"`
}

// entry is one transcript line. Only the fields pairing needs are decoded.
type entry struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one element of an array-form content field. tool_use and
// tool_result blocks share the type, so both sets of fields live here.
type block struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

// call is what a tool_use block contributes to the failure that follows it.
type call struct {
	name    string
	command string
}

// Collect scans every session's transcript and returns the failures in order:
// sessions in the order given, and within a session in transcript order.
//
// A session whose transcript is missing or unreadable is skipped rather than
// fatal, matching how the rest of the tool treats a rotated-away transcript:
// one absent session is not a reason to refuse a window's worth of counting.
func Collect(p store.Paths, sessions []model.Session) []Failure {
	var out []Failure
	for _, s := range sessions {
		path := p.TranscriptPath(s.Meta.SessionID)
		if path == "" {
			continue
		}
		fs, err := ScanFile(path, s.Meta.SessionID, dateOf(s.Meta.StartTime), s.Meta.ProjectPath)
		if err != nil {
			continue
		}
		out = append(out, fs...)
	}
	return out
}

// ScanFile reads one transcript and returns its failed tool calls.
func ScanFile(path, sessionID, date, project string) ([]Failure, error) {
	calls := map[string]call{}
	var out []Failure

	err := transcript.ForEachLine(path, func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		var e entry
		// The file is appended to while the session runs, so its tail is
		// routinely half-written; an unparseable line is skipped, not fatal.
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return
		}
		for _, b := range decodeBlocks(e.Message.Content) {
			switch {
			case e.Type == "assistant" && b.Type == "tool_use" && b.ID != "":
				calls[b.ID] = call{name: b.Name, command: bashCommand(b.Input)}
			case e.Type == "user" && b.Type == "tool_result" && b.IsError:
				c := calls[b.ToolUseID]
				out = append(out, newFailure(sessionID, date, project, c, resultText(b)))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// newFailure applies the classification rules to one paired result.
func newFailure(sessionID, date, project string, c call, text string) Failure {
	f := Failure{
		Session: shortID(sessionID),
		Date:    date,
		Project: project,
		Tool:    c.name,
		Command: truncate(strings.TrimSpace(c.command), maxCommandChars),
	}
	if isBash(c.name) {
		f.Shape = Shape(c.command)
	}
	f.Class, f.Detail = Classify(c.name, c.command, text)
	if f.Class == ClassShellError {
		f.ShellSignature = shellSignature(text)
	}
	f.Signature = Normalize(text)
	f.SelfInflicted = selfInflicted(f)
	return f
}

// selfInflicted splits the failures this machine could have prevented from the
// ones it could only have retried. A classifier denial counts as self-inflicted
// only when the command had a shape that defeats an allow rule: a denial of a
// plain command is the classifier's call, not the caller's mistake.
func selfInflicted(f Failure) bool {
	switch f.Class {
	case ClassHarnessRule:
		return true
	case ClassShellError:
		return f.ShellSignature != ""
	case ClassClassifierDenied:
		return f.Shape != "" && f.Shape != ShapeSimple
	}
	return false
}

// decodeBlocks decodes array-form content, tolerating a single bad element.
func decodeBlocks(raw json.RawMessage) []block {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
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

// resultText flattens a tool_result's content, which the CLI writes either as
// a bare string or as an array of text blocks depending on the tool.
func resultText(b block) string {
	if len(b.Content) == 0 {
		return b.Text
	}
	if b.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(b.Content, &s); err == nil {
			return s
		}
		return ""
	}
	var parts []string
	for _, sub := range decodeBlocks(b.Content) {
		if strings.TrimSpace(sub.Text) != "" {
			parts = append(parts, sub.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// bashCommand pulls the command out of a tool_use input. Every other tool's
// input is left alone: the command is the only payload field classification
// reads, and decoding the rest would pull file contents into memory.
func bashCommand(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	return in.Command
}

// Summary is the aggregate over a window's failures.
//
// Every collection is a slice rather than a map so that the JSON output has one
// fixed order: a map would render in Go's randomized iteration order and two
// runs over the same window would produce diffable-looking but identical data.
type Summary struct {
	Sessions      int              `json:"sessions"`
	Total         int              `json:"total"`
	PerSession    float64          `json:"per_session"`
	SelfInflicted int              `json:"self_inflicted"`
	External      int              `json:"external"`
	ByClass       []ClassCount     `json:"by_class"`
	Shapes        []Count          `json:"classifier_denied_shapes"`
	TopSignatures []SignatureCount `json:"top_signatures"`
}

// ClassCount is one class's total and its per-session rate.
type ClassCount struct {
	Class      string  `json:"class"`
	Count      int     `json:"count"`
	PerSession float64 `json:"per_session"`
}

// Count is a plain key/count row.
type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// SignatureCount is one normalized message with an example of what produced it.
type SignatureCount struct {
	Class     string `json:"class"`
	Signature string `json:"signature"`
	Count     int    `json:"count"`
	Example   string `json:"example,omitempty"`
}

// Summarize aggregates failures over a population of sessions. sessions is the
// substantive count, not the count of sessions that happened to fail, so the
// per-session rates compare across weeks of different sizes.
func Summarize(fs []Failure, sessions int) Summary {
	s := Summary{Sessions: sessions, Total: len(fs)}
	den := float64(sessions)
	if den < 1 {
		den = 1
	}
	s.PerSession = round2(float64(len(fs)) / den)

	byClass := map[string]int{}
	shapes := map[string]int{}
	sigCount := map[string]int{}
	sigClass := map[string]string{}
	sigText := map[string]string{}
	sigExample := map[string]string{}

	for _, f := range fs {
		byClass[f.Class]++
		if f.SelfInflicted {
			s.SelfInflicted++
		}
		if f.Class == ClassClassifierDenied && f.Shape != "" {
			shapes[f.Shape]++
		}
		key := f.Class + "\x00" + f.Signature
		sigCount[key]++
		sigClass[key] = f.Class
		sigText[key] = f.Signature
		// First example wins, so the quoted command does not change when a
		// later session happens to hit the same signature.
		if sigExample[key] == "" && f.Command != "" {
			sigExample[key] = f.Command
		}
	}
	s.External = s.Total - s.SelfInflicted

	// Classes render in the fixed rule order, not by count: the order is the
	// precedence, and reading it as a ranking would misstate the taxonomy.
	for _, c := range Classes {
		n := byClass[c]
		if n == 0 {
			continue
		}
		s.ByClass = append(s.ByClass, ClassCount{Class: c, Count: n, PerSession: round2(float64(n) / den)})
	}
	s.Shapes = sortedCounts(shapes)
	for _, kv := range sortedCounts(sigCount) {
		s.TopSignatures = append(s.TopSignatures, SignatureCount{
			Class:     sigClass[kv.Key],
			Signature: sigText[kv.Key],
			Count:     kv.Count,
			Example:   sigExample[kv.Key],
		})
	}
	if len(s.TopSignatures) > topSignatures {
		s.TopSignatures = s.TopSignatures[:topSignatures]
	}
	return s
}

// sortedCounts orders by count descending and key ascending. The secondary key
// is what makes the output deterministic across runs.
func sortedCounts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		out = append(out, Count{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func dateOf(start string) string {
	const dateLen = 10
	if len(start) < dateLen {
		return start
	}
	return start[:dateLen]
}

func shortID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// truncate cuts on rune boundaries so a multi-byte character is never split
// into invalid UTF-8 on its way into the JSON output.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
