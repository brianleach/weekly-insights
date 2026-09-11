package progress

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/report"
	"github.com/brianleach/weekly-insights/internal/runner"
)

//go:embed prompt.md
var instructions string

// Instructions returns the fixed prompt, for --dry-run and for the docs.
func Instructions() string { return instructions }

// Memo is the model's structured answer.
type Memo struct {
	Verdict     string        `json:"verdict"`
	Recurring   []Recurring   `json:"recurring"`
	Resolved    []Resolved    `json:"resolved"`
	Suggestions []Suggestion2 `json:"suggestions"`
	AlreadyHave []string      `json:"already_have"`
	Numbers     string        `json:"numbers"`
	OneChange   string        `json:"one_change"`
}

type Recurring struct {
	Theme    string   `json:"theme"`
	Weeks    []string `json:"weeks"`
	Evidence string   `json:"evidence"`
	Trend    string   `json:"trend"`
}

type Resolved struct {
	Theme    string `json:"theme"`
	LastSeen string `json:"last_seen"`
	Note     string `json:"note"`
}

type Suggestion2 struct {
	Suggestion     string `json:"suggestion"`
	FirstSuggested string `json:"first_suggested"`
	Adopted        bool   `json:"adopted"`
	Effect         string `json:"effect"`
}

// Input is everything the model is shown besides the instructions.
type Input struct {
	Weeks    []Week // oldest first
	ClaudeMD string // current global CLAUDE.md, may be empty
	Trend    string // numeric trend table, may be empty
}

// maxClaudeMD bounds the CLAUDE.md excerpt. A long file is mostly project
// rules that have nothing to do with insights, and the budget is better spent
// on the weeks themselves.
const maxClaudeMD = 24000

// Render the data the model reads from stdin. Sections are labelled so the
// model can cite weeks by date.
func (in Input) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== WEEKLY REPORTS, OLDEST FIRST (%d weeks) ===\n", len(in.Weeks))
	for _, w := range in.Weeks {
		fmt.Fprintf(&b, "\n##### WEEK ENDING %s (%d days) #####\n", w.Label, w.Days)
		if len(w.Stats) > 0 {
			b.WriteString("Stats: ")
			for i, s := range w.Stats {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s", s.Value, s.Label)
			}
			b.WriteString("\n")
		}
		section(&b, "At a glance", w.Glance)
		for _, a := range w.Areas {
			fmt.Fprintf(&b, "Project area: %s. %s\n", a.Title, a.Desc)
		}
		for _, x := range w.Wins {
			fmt.Fprintf(&b, "Win: %s. %s\n", x.Title, x.Desc)
		}
		for _, f := range w.Friction {
			fmt.Fprintf(&b, "Friction: %s. %s\n", f.Title, f.Desc)
			for _, e := range f.Examples {
				fmt.Fprintf(&b, "  - %s\n", e)
			}
		}
		for _, s := range w.Suggestions {
			fmt.Fprintf(&b, "Suggested CLAUDE.md addition:\n%s\n", indent(s.Text))
			if s.Why != "" {
				fmt.Fprintf(&b, "  why: %s\n", s.Why)
			}
		}
		section(&b, "Features suggested", w.Features)
		section(&b, "Horizon ideas", w.Horizon)
	}
	if in.Trend != "" {
		b.WriteString("\n=== NUMERIC TREND (from canonical facets; per-session rates) ===\n")
		b.WriteString(in.Trend)
		b.WriteString("\n")
	}
	if in.ClaudeMD != "" {
		md := in.ClaudeMD
		if len(md) > maxClaudeMD {
			md = md[:maxClaudeMD] + "\n[... truncated ...]"
		}
		b.WriteString("\n=== CURRENT GLOBAL CLAUDE.md (what the user has actually adopted) ===\n")
		b.WriteString(md)
		b.WriteString("\n")
	}
	return b.String()
}

func section(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "  - %s\n", it)
	}
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// Options for one model run.
type Options struct {
	ClaudeBin string    // defaults to "claude"
	Token     string    // optional; resolved by package auth, never logged
	Stderr    io.Writer // child stderr passthrough; nil discards
}

// ErrExec wraps a failure of the child process itself: it could not start, or
// it exited nonzero. Whatever it printed is not an answer, so a caller must
// fail rather than fall back to it.
var ErrExec = errors.New("the progress run failed")

// ErrParse wraps a child that succeeded but whose reply was not the expected
// JSON. The raw reply is still the model's answer, so a caller may keep it.
var ErrParse = errors.New("the model's reply could not be parsed")

// Run asks the user's own Claude Code for the memo. It runs against the real
// config directory with the default model, so the judgment comes from the
// same place the weekly reports did.
//
// The returned error is ErrExec-wrapped when the process failed and
// ErrParse-wrapped when it succeeded but the reply did not parse; only the
// second case leaves a raw reply worth keeping.
func Run(in Input, o Options) (Memo, string, error) {
	bin := o.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	// The instructions travel as the prompt and the data on stdin, which
	// Claude Code appends as context; that keeps the prompt clear of argv
	// length limits and makes --dry-run output the exact same bytes.
	cmd := exec.Command(bin, "-p", instructions, "--output-format", "text")
	cmd.Dir = os.TempDir()
	env := runner.BaseEnv()
	// BaseEnv strips any inherited token on purpose, so an authenticated run
	// needs the resolved one put back explicitly. It is never printed.
	if o.Token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+o.Token)
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader(in.Text())
	var out bytes.Buffer
	cmd.Stdout = &out
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	}
	runErr := cmd.Run()
	// A child that cannot authenticate says so on stdout and may still exit
	// zero, so this is checked the same way the insights runner checks it.
	if strings.Contains(out.String(), "Not logged in") {
		return Memo{}, "", runner.ErrNotLoggedIn
	}
	if runErr != nil {
		return Memo{}, "", fmt.Errorf("%w: running %s -p: %v\n%s",
			ErrExec, bin, runErr, strings.TrimSpace(out.String()))
	}
	m, err := Parse(out.String())
	if err != nil {
		return Memo{}, out.String(), fmt.Errorf("%w: %v", ErrParse, err)
	}
	return m, out.String(), nil
}

var reObject = regexp.MustCompile(`(?s)\{.*\}`)

// Parse pulls the JSON object out of a reply that may carry stray prose or a
// code fence around it, the same tolerance the builtin applies to its own
// facet replies.
func Parse(reply string) (Memo, error) {
	raw := reObject.FindString(reply)
	if raw == "" {
		return Memo{}, errors.New("reply contained no JSON object")
	}
	var m Memo
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Memo{}, fmt.Errorf("parsing memo JSON: %w", err)
	}
	return m, nil
}

// Render writes the memo as a standalone page in the weekly report's style.
// When the memo failed to parse, raw is shown verbatim so nothing is lost.
func Render(m Memo, raw string, in Input) string {
	var b strings.Builder
	first, last := "", ""
	if len(in.Weeks) > 0 {
		first, last = in.Weeks[0].Label, in.Weeks[len(in.Weeks)-1].Label
	}
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>Progress through %s</title>\n<style>\n%s%s</style>\n</head>\n<body>\n<div class=\"container\">\n",
		esc(last), report.CSS(), extraCSS)
	b.WriteString("<h1>Progress</h1>\n")
	fmt.Fprintf(&b, "<div class=\"subtitle\">%d weekly reports, %s to %s &middot; generated %s</div>\n",
		len(in.Weeks), longDate(first), longDate(last), time.Now().Format("Jan 2, 2006"))

	b.WriteString("<div class=\"week-chips\">")
	for _, w := range in.Weeks {
		fmt.Fprintf(&b, "<a class=\"chip\" href=\"%s\">%s</a>", esc(filepathBase(w.Path)), esc(shortDate(w.Label)))
	}
	b.WriteString("</div>\n")

	if m.Verdict == "" && raw != "" {
		b.WriteString("<h2>Reply</h2>\n<p class=\"section-intro\">The model's reply could not be parsed as the expected JSON, so it is shown as written.</p>\n")
		fmt.Fprintf(&b, "<pre class=\"raw\">%s</pre>\n", esc(raw))
		b.WriteString("</div>\n</body>\n</html>\n")
		return b.String()
	}

	fmt.Fprintf(&b, "<div class=\"at-a-glance\"><div class=\"glance-title\">Verdict</div><div class=\"glance-section\">%s</div></div>\n", esc(m.Verdict))

	b.WriteString("<h2>Recurring Friction</h2>\n")
	if len(m.Recurring) == 0 {
		b.WriteString("<p class=\"empty\">Nothing recurred across weeks.</p>\n")
	} else {
		b.WriteString("<div class=\"friction-categories\">\n")
		for _, r := range m.Recurring {
			fmt.Fprintf(&b, "<div class=\"friction-category\"><div class=\"friction-title\">%s <span class=\"trend %s\">%s</span></div>",
				esc(r.Theme), esc(r.Trend), esc(r.Trend))
			b.WriteString("<div class=\"weeks\">")
			for _, w := range r.Weeks {
				fmt.Fprintf(&b, "<span class=\"chip small\">%s</span>", esc(shortDate(w)))
			}
			b.WriteString("</div>")
			fmt.Fprintf(&b, "<div class=\"friction-desc\">%s</div></div>\n", esc(r.Evidence))
		}
		b.WriteString("</div>\n")
	}

	b.WriteString("<h2>Resolved</h2>\n")
	if len(m.Resolved) == 0 {
		b.WriteString("<p class=\"empty\">No earlier theme has disappeared yet.</p>\n")
	} else {
		b.WriteString("<div class=\"big-wins\">\n")
		for _, r := range m.Resolved {
			fmt.Fprintf(&b, "<div class=\"big-win\"><div class=\"big-win-title\">%s <span class=\"muted\">last seen %s</span></div><div class=\"big-win-desc\">%s</div></div>\n",
				esc(r.Theme), esc(shortDate(r.LastSeen)), esc(r.Note))
		}
		b.WriteString("</div>\n")
	}

	b.WriteString("<h2>Suggestions and Whether You Took Them</h2>\n")
	if len(m.Suggestions) == 0 {
		b.WriteString("<p class=\"empty\">No suggestions were tracked.</p>\n")
	} else {
		b.WriteString("<div class=\"claude-md-section\">\n")
		for _, s := range m.Suggestions {
			mark, cls := "not adopted", "no"
			if s.Adopted {
				mark, cls = "adopted", "yes"
			}
			fmt.Fprintf(&b, "<div class=\"claude-md-item\"><div class=\"sug\"><span class=\"adopt %s\">%s</span> <strong>%s</strong> <span class=\"muted\">first suggested %s</span><div class=\"cmd-why\">%s</div></div></div>\n",
				cls, mark, esc(s.Suggestion), esc(shortDate(s.FirstSuggested)), esc(s.Effect))
		}
		b.WriteString("</div>\n")
	}

	if len(m.AlreadyHave) > 0 {
		b.WriteString("<h2>Still Being Suggested, Already In Place</h2>\n<ul class=\"plain\">\n")
		for _, a := range m.AlreadyHave {
			fmt.Fprintf(&b, "<li>%s</li>\n", esc(a))
		}
		b.WriteString("</ul>\n")
	}

	if strings.TrimSpace(m.Numbers) != "" {
		fmt.Fprintf(&b, "<h2>Numbers</h2>\n<div class=\"narrative\"><p>%s</p></div>\n", esc(m.Numbers))
	}

	fmt.Fprintf(&b, "<h2>One Change For Next Week</h2>\n<div class=\"key-insight\">%s</div>\n", esc(m.OneChange))

	b.WriteString("<div class=\"footnote\">Built from the raw weekly insights reports listed above, which are unchanged. The judgment is a model's reading of those reports and of the current global CLAUDE.md.</div>\n")
	b.WriteString("</div>\n</body>\n</html>\n")
	return b.String()
}

const extraCSS = `
.week-chips { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 24px; }
.chip { font-size: 12px; color: #475569; background: #f1f5f9; border: 1px solid #e2e8f0; padding: 4px 10px; border-radius: 6px; text-decoration: none; }
.chip.small { font-size: 11px; padding: 1px 7px; }
.weeks { display: flex; gap: 6px; flex-wrap: wrap; margin: 6px 0 8px; }
.trend { font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 4px; vertical-align: middle; margin-left: 6px; text-transform: uppercase; }
.trend.worse { background: #fee2e2; color: #991b1b; }
.trend.same { background: #f1f5f9; color: #475569; }
.trend.better { background: #dcfce7; color: #166534; }
.adopt { font-size: 11px; font-weight: 600; padding: 1px 7px; border-radius: 4px; text-transform: uppercase; margin-right: 6px; }
.adopt.yes { background: #dcfce7; color: #166534; }
.adopt.no { background: #fee2e2; color: #991b1b; }
.sug strong { color: #0f172a; }
.muted { color: #64748b; font-size: 12px; font-weight: 400; }
ul.plain { margin: 0 0 16px 20px; font-size: 14px; color: #334155; }
pre.raw { background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 16px; font-size: 12px; white-space: pre-wrap; }
.key-insight { font-size: 15px; }
`

func esc(s string) string { return html.EscapeString(s) }

func longDate(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return esc(s)
	}
	return t.Format("Jan 2, 2006")
}

func shortDate(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return esc(s)
	}
	return t.Format("Jan 2")
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
