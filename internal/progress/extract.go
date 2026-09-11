// Package progress judges progression across weekly builtin reports.
//
// The builtin /insights has no memory: every run writes every section from
// scratch, so a one-week window reads like a one-week-old user. Rather than
// alter the builtin, this package reads the raw reports it already wrote,
// extracts the sections a progression judgment needs, and asks the model one
// fixed set of questions across weeks. The raw reports are never modified.
package progress

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Week is the extractable content of one raw builtin report.
type Week struct {
	Label       string // end date, YYYY-MM-DD
	Days        int
	Path        string
	Stats       []Stat
	Glance      []string
	Areas       []Titled
	Wins        []Titled
	Friction    []Friction
	Suggestions []Suggestion
	Features    []string
	Horizon     []string
}

type Stat struct{ Value, Label string }
type Titled struct{ Title, Desc string }
type Friction struct {
	Title, Desc string
	Examples    []string
}
type Suggestion struct{ Text, Why string }

var (
	reName    = regexp.MustCompile(`^insights-(\d+)d-(\d{4}-\d{2}-\d{2})\.html$`)
	reStat    = regexp.MustCompile(`(?s)<div class="stat-value">(.*?)</div>\s*<div class="stat-label">(.*?)</div>`)
	reGlance  = regexp.MustCompile(`(?s)<div class="glance-section">(.*?)</div>`)
	reArea    = regexp.MustCompile(`(?s)<div class="area-name">(.*?)</div>.*?<div class="area-desc">(.*?)</div>`)
	reWin     = regexp.MustCompile(`(?s)<div class="big-win-title">(.*?)</div>\s*<div class="big-win-desc">(.*?)</div>`)
	reFrict   = regexp.MustCompile(`(?s)<div class="friction-category">\s*<div class="friction-title">(.*?)</div>\s*<div class="friction-desc">(.*?)</div>\s*<ul class="friction-examples">(.*?)</ul>`)
	reLI      = regexp.MustCompile(`(?s)<li>(.*?)</li>`)
	reCmd     = regexp.MustCompile(`(?s)<code class="cmd-code">(.*?)</code>`)
	reWhy     = regexp.MustCompile(`(?s)<div class="cmd-why">(.*?)</div>`)
	reFeature = regexp.MustCompile(`(?s)<div class="feature-title">(.*?)</div>`)
	reHorizon = regexp.MustCompile(`(?s)<div class="horizon-title">(.*?)</div>`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reSpace   = regexp.MustCompile(`\s+`)
)

// Extract parses one raw report. It works from the builtin's class names, so a
// change to the builtin's template degrades to empty sections rather than an
// error; the caller decides whether that is acceptable.
func Extract(path string) (Week, error) {
	w := Week{Path: path}
	m := reName.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return w, fmt.Errorf("%s is not a windowed insights report (want insights-<N>d-<date>.html)", filepath.Base(path))
	}
	fmt.Sscanf(m[1], "%d", &w.Days)
	w.Label = m[2]

	b, err := os.ReadFile(path)
	if err != nil {
		return w, fmt.Errorf("reading %s: %w", path, err)
	}
	s := string(b)

	for _, x := range reStat.FindAllStringSubmatch(s, -1) {
		w.Stats = append(w.Stats, Stat{text(x[1]), text(x[2])})
	}
	for _, x := range reGlance.FindAllStringSubmatch(s, -1) {
		w.Glance = append(w.Glance, text(x[1]))
	}
	for _, x := range reArea.FindAllStringSubmatch(s, -1) {
		w.Areas = append(w.Areas, Titled{text(x[1]), text(x[2])})
	}
	for _, x := range reWin.FindAllStringSubmatch(s, -1) {
		w.Wins = append(w.Wins, Titled{text(x[1]), text(x[2])})
	}
	for _, x := range reFrict.FindAllStringSubmatch(s, -1) {
		f := Friction{Title: text(x[1]), Desc: text(x[2])}
		for _, li := range reLI.FindAllStringSubmatch(x[3], -1) {
			f.Examples = append(f.Examples, text(li[1]))
		}
		w.Friction = append(w.Friction, f)
	}
	cmds := reCmd.FindAllStringSubmatch(s, -1)
	whys := reWhy.FindAllStringSubmatch(s, -1)
	for i, c := range cmds {
		sg := Suggestion{Text: textKeepLines(c[1])}
		if i < len(whys) {
			sg.Why = text(whys[i][1])
		}
		w.Suggestions = append(w.Suggestions, sg)
	}
	for _, x := range reFeature.FindAllStringSubmatch(s, -1) {
		w.Features = append(w.Features, text(x[1]))
	}
	for _, x := range reHorizon.FindAllStringSubmatch(s, -1) {
		w.Horizon = append(w.Horizon, text(x[1]))
	}
	return w, nil
}

// Discover returns the windowed reports in dir, oldest first.
func Discover(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "insights-*d-*.html"))
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var out []string
	for _, m := range matches {
		if reName.MatchString(filepath.Base(m)) {
			out = append(out, m)
		}
	}
	// Names embed the end date, so sorting by the date portion orders weeks.
	sort.Slice(out, func(i, j int) bool {
		return reName.FindStringSubmatch(filepath.Base(out[i]))[2] < reName.FindStringSubmatch(filepath.Base(out[j]))[2]
	})
	return out, nil
}

func text(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// textKeepLines is for suggested CLAUDE.md blocks, where line breaks carry
// the structure of the bullet list being proposed.
func textKeepLines(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}
