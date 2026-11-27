package coach

import (
	"fmt"
	"html"
	"strings"
)

// Summary is one line of evidence: how often, in how many of how many
// sessions, and per window.
func (e Evidence) Summary() string {
	var weeks []string
	for _, w := range e.Weeks {
		if w.Rate != 0 {
			weeks = append(weeks, fmt.Sprintf("%s: %.2f/session", w.Window, w.Rate))
		} else {
			weeks = append(weeks, fmt.Sprintf("%s: %d", w.Window, w.Count))
		}
	}
	return fmt.Sprintf("rests on %d of %d sessions; %s", e.Sessions, e.Population, strings.Join(weeks, ", "))
}

// Text is the ranked list as the coach command prints it.
func Text(r Result, header string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %d proposal(s)\n", header, len(r.Proposals))
	for i, p := range r.Proposals {
		fmt.Fprintf(&b, "\n%d. [%s] %s %s  (score %d, risk %s, %s)\n", i+1, p.ID, p.Rule, p.Title, p.Score, p.Risk, p.Status)
		fmt.Fprintf(&b, "   target:   %s\n   evidence: %s\n", p.Target, p.Evidence.Summary())
		if len(p.Evidence.SessionIDs) > 0 {
			fmt.Fprintf(&b, "   sessions: %s\n", strings.Join(p.Evidence.SessionIDs, ", "))
		}
		for _, q := range p.Evidence.Quotes {
			fmt.Fprintf(&b, "   quote:    %q\n", q)
		}
		fmt.Fprintf(&b, "   change:   %s\n", p.Change)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\nnote: %s\n", n)
	}
	return b.String()
}

// HTML is the coach page of the week bundle.
func HTML(r Result, title string) string {
	var b strings.Builder
	t := html.EscapeString(title)
	fmt.Fprintf(&b, `<!doctype html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:52rem;margin:2rem auto;padding:0 1rem}
pre{background:#f2f4f7;padding:.5rem;white-space:pre-wrap}.muted{color:#667085}</style>
</head><body><h1>%s</h1>
`, t, t)
	if len(r.Proposals) == 0 {
		b.WriteString("<p class=\"muted\">No proposals this window.</p>\n")
	}
	for i, p := range r.Proposals {
		fmt.Fprintf(&b, "<h2>%d. %s</h2><p class=\"muted\">%s &middot; %s &middot; target %s &middot; score %d &middot; risk %s &middot; %s</p>\n",
			i+1, html.EscapeString(p.Title), p.ID, p.Rule, p.Target, p.Score, html.EscapeString(p.Risk), p.Status)
		fmt.Fprintf(&b, "<p>%s</p><pre>%s</pre>\n", html.EscapeString(p.Evidence.Summary()), html.EscapeString(p.Change))
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "<p class=\"muted\">note: %s</p>\n", html.EscapeString(n))
	}
	b.WriteString("</body></html>\n")
	return b.String()
}
