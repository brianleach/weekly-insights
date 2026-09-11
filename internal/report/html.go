package report

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// The HTML report deliberately borrows the builtin /insights design system:
// a stats strip, two-column bar-chart cards, and colored callout cards, with
// no data tables. It is rendered straight from the Snapshot rather than by
// converting the markdown, because bar charts and delta cards have no
// markdown equivalent and the person reading it week after week already knows
// the builtin's layout.

// WeeklyHTML renders one snapshot as a standalone page, diffed against prev
// when non-nil.
func WeeklyHTML(cur snapshot.Snapshot, prev *snapshot.Snapshot) string {
	var b strings.Builder
	title := "Weekly Insights " + cur.Window.Label
	pageOpen(&b, title)

	fmt.Fprintf(&b, "<h1>Weekly Insights</h1>\n")
	sub := fmt.Sprintf("%s to %s", longDate(cur.Window.Start), longDate(cur.Window.End))
	if prev != nil {
		sub += fmt.Sprintf(" &middot; compared with the week ending %s", longDate(prev.Window.End))
	}
	fmt.Fprintf(&b, "<div class=\"subtitle\">%s</div>\n", sub)

	nav(&b, [][2]string{
		{"#wow", "Week over week"}, {"#wanted", "What you wanted"},
		{"#used", "How you used it"}, {"#corrections", "What you corrected"},
		{"#friction", "Friction detail"},
	})

	s, v := cur.Sessions, cur.Volume
	stats(&b, []stat{
		{fmt.Sprint(s.Substantive), "sessions"},
		{fmt.Sprint(v.Messages), "messages"},
		{num(v.Hours), "hours"},
		{fmt.Sprint(v.Commits), "commits"},
		{fmt.Sprintf("+%d / -%d", v.LinesAdded, v.LinesRemoved), "lines"},
		{pct(cur.Rates[snapshot.RateFacetCoveragePct]), "facet coverage"},
	})

	// Week over week -------------------------------------------------------
	b.WriteString("<h2 id=\"wow\">Week Over Week</h2>\n")
	if prev == nil {
		b.WriteString("<div class=\"narrative\"><p>First snapshot. There is no prior week to compare against; the next run will show deltas here.</p></div>\n")
	} else {
		fmt.Fprintf(&b, "<p class=\"section-intro\">Per-session rates, so a busy week is not penalised against a quiet one. Last week had %d sessions.</p>\n",
			prev.Sessions.Substantive)
		b.WriteString("<div class=\"delta-grid\">\n")
		for _, m := range tracked {
			c, okc := cur.Rates[m.key]
			p, okp := prev.Rates[m.key]
			if !okc && !okp {
				continue
			}
			var d float64
			okd := okc && okp
			if okd {
				d = round2(c - p)
			}
			ver := verdict(d, okd, m.down)
			sign := ""
			if okd && d >= 0 {
				sign = "+"
			}
			fmt.Fprintf(&b, "<div class=\"delta-card %s\"><div class=\"delta-label\">%s</div>"+
				"<div class=\"delta-value\">%s</div>"+
				"<div class=\"delta-sub\">last week %s <span class=\"delta-change\">%s%s</span></div></div>\n",
				ver, esc(m.label), fmtOpt(c, okc, m.suffix), fmtOpt(p, okp, m.suffix), sign, fmtOpt(d, okd, m.suffix))
		}
		b.WriteString("</div>\n")
	}

	// What you wanted ------------------------------------------------------
	b.WriteString("<h2 id=\"wanted\">What You Wanted</h2>\n")
	b.WriteString("<div class=\"charts-row\">\n")
	var prevGoals, prevFriction map[string]int
	if prev != nil {
		prevGoals, prevFriction = prev.Goals, prev.Friction
	}
	chart(&b, "What you asked for", cur.Goals, prevGoals, 10, "#2563eb")
	chart(&b, "Friction", cur.Friction, prevFriction, 10, "#dc2626")
	b.WriteString("</div>\n")

	// How you used it ------------------------------------------------------
	b.WriteString("<h2 id=\"used\">How You Used It</h2>\n")
	b.WriteString("<div class=\"charts-row\">\n")
	chart(&b, "Tools", cur.Tools, nil, 8, "#7c3aed")
	chart(&b, "Projects", shortProjects(cur.Projects), nil, 8, "#16a34a")
	b.WriteString("</div>\n")
	b.WriteString("<div class=\"charts-row\">\n")
	chart(&b, "Outcomes", cur.Outcomes, nil, 5, "#0891b2")
	chart(&b, "Satisfaction signals", cur.Satisfaction, nil, 8, "#d97706")
	b.WriteString("</div>\n")

	// Corrections ------------------------------------------------------------
	fmt.Fprintf(&b, "<h2 id=\"corrections\">What You Had To Correct <span class=\"count\">%d</span></h2>\n", len(cur.UserCorrections))
	if len(cur.UserCorrections) == 0 {
		b.WriteString("<p class=\"empty\">None recorded.</p>\n")
	} else {
		b.WriteString("<p class=\"section-intro\">Verbatim, grouped by day. This is the section to go through line by line.</p>\n")
		for _, day := range byDay(len(cur.UserCorrections), func(i int) string { return cur.UserCorrections[i].Date }) {
			fmt.Fprintf(&b, "<div class=\"quote-card\"><div class=\"quote-day\">%s <span class=\"count\">%d</span></div><ul class=\"quote-list\">\n",
				longDate(day.date), len(day.idx))
			for _, i := range day.idx {
				fmt.Fprintf(&b, "<li>&ldquo;%s&rdquo;</li>\n", esc(cur.UserCorrections[i].Quote))
			}
			b.WriteString("</ul></div>\n")
		}
	}

	// Friction detail ----------------------------------------------------------
	fmt.Fprintf(&b, "<h2 id=\"friction\">Friction Detail <span class=\"count\">%d</span></h2>\n", len(cur.FrictionDetails))
	if len(cur.FrictionDetails) == 0 {
		b.WriteString("<p class=\"empty\">None recorded.</p>\n")
	} else {
		b.WriteString("<div class=\"friction-categories\">\n")
		for _, day := range byDay(len(cur.FrictionDetails), func(i int) string { return cur.FrictionDetails[i].Date }) {
			fmt.Fprintf(&b, "<div class=\"friction-category\"><div class=\"friction-title\">%s</div><ul class=\"friction-examples\">\n", longDate(day.date))
			for _, i := range day.idx {
				f := cur.FrictionDetails[i]
				fmt.Fprintf(&b, "<li><strong>%s</strong> %s</li>\n", esc(f.Project), esc(f.Detail))
			}
			b.WriteString("</ul></div>\n")
		}
		b.WriteString("</div>\n")
	}

	fmt.Fprintf(&b, "<div class=\"footnote\">%d sessions excluded as agent scratch or eval runs. Facets: %s. Hours are wall clock between first and last message and overstate sessions left open.</div>\n",
		s.ExcludedScratch, esc(sourceSummary(s.FacetSource)))
	pageClose(&b)
	return b.String()
}

// TrendHTML renders every snapshot as one page of per-metric bar charts,
// oldest week first, so a metric's shape over time is visible at a glance.
func TrendHTML(snaps []snapshot.Snapshot) string {
	var b strings.Builder
	pageOpen(&b, "Weekly Trend")
	b.WriteString("<h1>Weekly Trend</h1>\n")
	if len(snaps) == 0 {
		b.WriteString("<p class=\"empty\">No snapshots yet.</p>\n")
		pageClose(&b)
		return b.String()
	}
	fmt.Fprintf(&b, "<div class=\"subtitle\">%d weeks, %s to %s</div>\n",
		len(snaps), longDate(snaps[0].Window.Start), longDate(snaps[len(snaps)-1].Window.End))

	total := 0
	for _, s := range snaps {
		total += s.Sessions.Substantive
	}
	stats(&b, []stat{{fmt.Sprint(len(snaps)), "weeks"}, {fmt.Sprint(total), "sessions"}})

	b.WriteString("<p class=\"section-intro\">One card per metric, one bar per week. Bars are scaled to the largest week for that metric. Lower is better for everything except the last card.</p>\n")
	b.WriteString("<div class=\"charts-row\">\n")
	weekMetric := func(label, key, color string, isPct bool) {
		fmt.Fprintf(&b, "<div class=\"chart-card\"><div class=\"chart-title\">%s</div>\n", esc(label))
		max := 0.0
		for _, s := range snaps {
			if v := s.Rates[key]; v > max {
				max = v
			}
		}
		for i, s := range snaps {
			v, ok := s.Rates[key]
			w := 0.0
			if ok && max > 0 {
				w = 100 * v / max
			}
			cls := ""
			if i == len(snaps)-1 {
				cls = " latest"
			}
			val := fmtOpt(v, ok, "")
			if isPct {
				val = fmtOpt(v, ok, "%")
			}
			fmt.Fprintf(&b, "<div class=\"bar-row%s\"><div class=\"bar-label\">%s</div><div class=\"bar-track\"><div class=\"bar-fill\" style=\"width:%.1f%%;background:%s\"></div></div><div class=\"bar-value\">%s</div></div>\n",
				cls, shortDate(s.Window.Label), w, color, val)
		}
		b.WriteString("</div>\n")
	}
	weekMetric("Sessions", "", "#64748b", false) // placeholder replaced below
	b.Reset()
	// Rebuild without the placeholder: sessions is a count, not a rate.
	pageOpen(&b, "Weekly Trend")
	b.WriteString("<h1>Weekly Trend</h1>\n")
	fmt.Fprintf(&b, "<div class=\"subtitle\">%d weeks, %s to %s</div>\n",
		len(snaps), longDate(snaps[0].Window.Start), longDate(snaps[len(snaps)-1].Window.End))
	stats(&b, []stat{{fmt.Sprint(len(snaps)), "weeks"}, {fmt.Sprint(total), "sessions"}})
	b.WriteString("<p class=\"section-intro\">One card per metric, one bar per week. Bars are scaled to the largest week for that metric. Lower is better for everything except the last card.</p>\n")
	b.WriteString("<div class=\"charts-row\">\n")
	sessionsCard(&b, snaps)
	for _, m := range tracked {
		color := "#dc2626"
		if !m.down {
			color = "#16a34a"
		}
		weekMetric(m.label, m.key, color, m.suffix == "%")
	}
	b.WriteString("</div>\n")
	b.WriteString("<div class=\"narrative\"><p>Weeks are only comparable when their facets came from the same extraction. Check <code>sessions.facet_source</code> in each snapshot before reading a jump across that boundary as a real change.</p></div>\n")
	pageClose(&b)
	return b.String()
}

func sessionsCard(b *strings.Builder, snaps []snapshot.Snapshot) {
	b.WriteString("<div class=\"chart-card\"><div class=\"chart-title\">Sessions</div>\n")
	max := 0
	for _, s := range snaps {
		if s.Sessions.Substantive > max {
			max = s.Sessions.Substantive
		}
	}
	for i, s := range snaps {
		w := 0.0
		if max > 0 {
			w = 100 * float64(s.Sessions.Substantive) / float64(max)
		}
		cls := ""
		if i == len(snaps)-1 {
			cls = " latest"
		}
		fmt.Fprintf(b, "<div class=\"bar-row%s\"><div class=\"bar-label\">%s</div><div class=\"bar-track\"><div class=\"bar-fill\" style=\"width:%.1f%%;background:#2563eb\"></div></div><div class=\"bar-value\">%d</div></div>\n",
			cls, shortDate(s.Window.Label), w, s.Sessions.Substantive)
	}
	b.WriteString("</div>\n")
}

// --- building blocks -------------------------------------------------------

type stat struct{ value, label string }

func stats(b *strings.Builder, items []stat) {
	b.WriteString("<div class=\"stats-row\">\n")
	for _, it := range items {
		fmt.Fprintf(b, "<div class=\"stat\"><div class=\"stat-value\">%s</div><div class=\"stat-label\">%s</div></div>\n", it.value, esc(it.label))
	}
	b.WriteString("</div>\n")
}

func nav(b *strings.Builder, links [][2]string) {
	b.WriteString("<div class=\"nav-toc\">")
	for _, l := range links {
		fmt.Fprintf(b, "<a href=\"%s\">%s</a>", l[0], esc(l[1]))
	}
	b.WriteString("</div>\n")
}

// chart renders a horizontal bar chart card. When prev is non-nil each row
// carries last week's count as a muted delta so a shift is visible without a
// second chart.
func chart(b *strings.Builder, title string, counts, prev map[string]int, n int, color string) {
	fmt.Fprintf(b, "<div class=\"chart-card\"><div class=\"chart-title\">%s</div>\n", esc(title))
	rows := topN(counts, n)
	if len(rows) == 0 {
		b.WriteString("<div class=\"empty\">None recorded.</div></div>\n")
		return
	}
	max := rows[0].count
	for _, r := range rows {
		w := 100 * float64(r.count) / float64(max)
		delta := ""
		if prev != nil {
			if p, ok := prev[r.key]; ok {
				d := r.count - p
				switch {
				case d > 0:
					delta = fmt.Sprintf(" <span class=\"bar-delta up\">+%d</span>", d)
				case d < 0:
					delta = fmt.Sprintf(" <span class=\"bar-delta down\">%d</span>", d)
				}
			} else {
				delta = " <span class=\"bar-delta new\">new</span>"
			}
		}
		fmt.Fprintf(b, "<div class=\"bar-row\"><div class=\"bar-label\" title=\"%s\">%s</div><div class=\"bar-track\"><div class=\"bar-fill\" style=\"width:%.1f%%;background:%s\"></div></div><div class=\"bar-value\">%d%s</div></div>\n",
			esc(r.key), esc(humanize(r.key)), w, color, r.count, delta)
	}
	b.WriteString("</div>\n")
}

func humanize(label string) string {
	l := strings.ReplaceAll(label, "_", " ")
	if l == "" {
		return l
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// shortProjects keeps the last two path segments so the bar labels fit.
func shortProjects(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		parts := strings.Split(strings.TrimRight(k, "/"), "/")
		if len(parts) > 2 {
			parts = parts[len(parts)-2:]
		}
		out[strings.Join(parts, "/")] += v
	}
	return out
}

func sourceSummary(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for _, k := range []string{"canonical", "builtin"} {
		if n, ok := m[k]; ok {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	return strings.Join(parts, ", ")
}

func esc(s string) string { return html.EscapeString(s) }

func longDate(s string) string {
	t, err := time.Parse("2006-01-02", dateOnly(s))
	if err != nil {
		return esc(s)
	}
	return t.Format("Jan 2, 2006")
}

func shortDate(s string) string {
	t, err := time.Parse("2006-01-02", dateOnly(s))
	if err != nil {
		return esc(s)
	}
	return t.Format("Jan 2")
}

func pageOpen(b *strings.Builder, title string) {
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	fmt.Fprintf(b, "<title>%s</title>\n<style>\n%s</style>\n</head>\n<body>\n<div class=\"container\">\n", esc(title), css)
}

func pageClose(b *strings.Builder) {
	b.WriteString("</div>\n</body>\n</html>\n")
}

// css mirrors the builtin /insights stylesheet (light theme, Inter, 800px
// column, slate palette) with additions for the delta cards, quote cards and
// bar deltas this report has and the builtin does not.
const css = `
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif; background: #f8fafc; color: #334155; line-height: 1.65; padding: 48px 24px; }
.container { max-width: 800px; margin: 0 auto; }
h1 { font-size: 32px; font-weight: 700; color: #0f172a; margin-bottom: 8px; }
h2 { font-size: 20px; font-weight: 600; color: #0f172a; margin-top: 48px; margin-bottom: 16px; }
h2 .count { font-size: 13px; font-weight: 600; color: #64748b; background: #f1f5f9; padding: 2px 8px; border-radius: 4px; vertical-align: middle; margin-left: 6px; }
.subtitle { color: #64748b; font-size: 15px; margin-bottom: 32px; }
.nav-toc { display: flex; flex-wrap: wrap; gap: 8px; margin: 24px 0 32px 0; padding: 16px; background: white; border-radius: 8px; border: 1px solid #e2e8f0; }
.nav-toc a { font-size: 12px; color: #64748b; text-decoration: none; padding: 6px 12px; border-radius: 6px; background: #f1f5f9; }
.nav-toc a:hover { background: #e2e8f0; color: #334155; }
.stats-row { display: flex; gap: 24px; margin-bottom: 40px; padding: 20px 0; border-top: 1px solid #e2e8f0; border-bottom: 1px solid #e2e8f0; flex-wrap: wrap; }
.stat { text-align: center; }
.stat-value { font-size: 24px; font-weight: 700; color: #0f172a; white-space: nowrap; }
.stat-label { font-size: 11px; color: #64748b; text-transform: uppercase; }
.section-intro { font-size: 14px; color: #64748b; margin-bottom: 16px; }
.narrative { background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 20px; margin-bottom: 24px; }
.narrative p { font-size: 14px; color: #475569; line-height: 1.7; }
.delta-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 12px; margin-bottom: 24px; }
.delta-card { background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px; border-left-width: 4px; }
.delta-card.better { border-left-color: #16a34a; }
.delta-card.worse { border-left-color: #dc2626; }
.delta-card.flat { border-left-color: #cbd5e1; }
.delta-label { font-size: 12px; color: #64748b; margin-bottom: 4px; }
.delta-value { font-size: 24px; font-weight: 700; color: #0f172a; line-height: 1.2; }
.delta-sub { font-size: 12px; color: #64748b; margin-top: 4px; }
.delta-change { font-weight: 600; }
.better .delta-change { color: #16a34a; }
.worse .delta-change { color: #dc2626; }
.flat .delta-change { color: #94a3b8; }
.charts-row { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; margin: 24px 0; }
.chart-card { background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 16px; }
.chart-title { font-size: 12px; font-weight: 600; color: #64748b; text-transform: uppercase; margin-bottom: 12px; }
.bar-row { display: flex; align-items: center; margin-bottom: 6px; }
.bar-row.latest .bar-label, .bar-row.latest .bar-value { color: #0f172a; font-weight: 600; }
.bar-label { width: 130px; font-size: 11px; color: #475569; flex-shrink: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bar-track { flex: 1; height: 6px; background: #f1f5f9; border-radius: 3px; margin: 0 8px; }
.bar-fill { height: 100%; border-radius: 3px; }
.bar-value { min-width: 28px; font-size: 11px; font-weight: 500; color: #64748b; text-align: right; white-space: nowrap; }
.bar-delta { font-weight: 400; margin-left: 2px; }
.bar-delta.up { color: #dc2626; }
.bar-delta.down { color: #16a34a; }
.bar-delta.new { color: #94a3b8; }
.quote-card { background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 16px; margin-bottom: 12px; }
.quote-day { font-weight: 600; font-size: 14px; color: #0f172a; margin-bottom: 10px; }
.quote-day .count { font-size: 12px; font-weight: 600; color: #64748b; background: #f1f5f9; padding: 1px 7px; border-radius: 4px; margin-left: 6px; }
.quote-list { margin: 0 0 0 18px; font-size: 14px; color: #334155; }
.quote-list li { margin-bottom: 6px; line-height: 1.55; }
.friction-categories { display: flex; flex-direction: column; gap: 16px; margin-bottom: 24px; }
.friction-category { background: #fef2f2; border: 1px solid #fca5a5; border-radius: 8px; padding: 16px; }
.friction-title { font-weight: 600; font-size: 15px; color: #991b1b; margin-bottom: 8px; }
.friction-examples { margin: 0 0 0 20px; font-size: 13px; color: #334155; }
.friction-examples li { margin-bottom: 6px; line-height: 1.55; }
.friction-examples strong { color: #7f1d1d; }
.empty { color: #94a3b8; font-size: 13px; }
.footnote { margin-top: 40px; font-size: 12px; color: #94a3b8; border-top: 1px solid #e2e8f0; padding-top: 16px; }
code { font-family: monospace; font-size: 12px; background: #f1f5f9; padding: 1px 5px; border-radius: 3px; }
@media (max-width: 640px) { .charts-row { grid-template-columns: 1fr; } .stats-row { justify-content: center; } body { padding: 24px 16px; } }
`
