// Package report renders snapshots as markdown.
//
// The week-over-week table is the reason this package exists. The builtin
// report is cumulative, so it can never answer whether last week's correction
// stuck; every number here is either a delta or a per-session rate, so a busy
// week is not automatically judged worse than a quiet one.
//
// This is a faithful port of the Python reference (scripts/report.py). Where
// the two differ, the difference is noted at the site.
package report

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// metric is one row of the week-over-week table. Direction records which way is
// an improvement, which is the only thing that lets a raw delta become a
// verdict: a falling friction rate is good, a falling achievement rate is not.
type metric struct {
	key    string
	label  string
	down   bool // true when lower is better
	suffix string
}

// tracked is ordered; the table renders in this order.
var tracked = []metric{
	{snapshot.RateFrictionPerSession, "Friction events / session", true, ""},
	{snapshot.RateNegativePct, "Dissatisfied or frustrated", true, "%"},
	{snapshot.RateUnverifiedClaim, "Unverified claims / session", true, ""},
	{snapshot.RateUnsanctionedAction, "Unsanctioned actions / session", true, ""},
	{snapshot.RateIgnoredPreference, "Ignored preferences / session", true, ""},
	{snapshot.RateInterruptions, "Your interruptions / session", true, ""},
	{snapshot.RateToolErrors, "Tool errors / session", true, ""},
	{snapshot.RateAchievedPct, "Fully or mostly achieved", false, "%"},
}

// epsilon absorbs float noise so a rate that did not actually move reads as
// "flat" rather than as a microscopic regression.
const epsilon = 1e-9

// Weekly renders one snapshot as markdown, diffed against prev when non-nil.
func Weekly(cur snapshot.Snapshot, prev *snapshot.Snapshot) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s); b.WriteByte('\n') }

	// The em dash here is a literal separator in the header, not prose.
	line(fmt.Sprintf("# Weekly insights — %s to %s", dateOnly(cur.Window.Start), dateOnly(cur.Window.End)))
	line("")

	s, v := cur.Sessions, cur.Volume
	line(fmt.Sprintf("**%d substantive sessions** across %d active days · %d messages · %sh · %d commits · +%d/-%d lines",
		s.Substantive, v.DaysActive, v.Messages, num(v.Hours), v.Commits, v.LinesAdded, v.LinesRemoved))
	line("")

	// Coverage is reported alongside the counts so a quiet week is not confused
	// with a week whose facets simply were not extracted.
	line(fmt.Sprintf("Facet coverage %s (%d/%d). %d scratch/eval sessions excluded.",
		ratePct(cur, snapshot.RateFacetCoveragePct), s.WithFacets, s.Substantive, s.ExcludedScratch))
	line("")

	line("## Week over week")
	line("")
	if prev == nil {
		line("_First snapshot. No prior week to compare against; next run will show deltas._")
	} else {
		line(fmt.Sprintf("Comparing against **%s** (%d sessions).", prev.Window.Label, prev.Sessions.Substantive))
		line("")
		line("| Metric | This week | Last week | Change | |")
		line("|---|---:|---:|---:|---|")
		for _, m := range tracked {
			c, okc := cur.Rates[m.key]
			p, okp := prev.Rates[m.key]
			// A metric absent from both snapshots predates the tool tracking it;
			// rendering "n/a vs n/a" would be noise.
			if !okc && !okp {
				continue
			}
			var d float64
			okd := okc && okp
			if okd {
				d = round2(c - p)
			}
			sign := "+"
			if !okd || d < 0 {
				sign = ""
			}
			line(fmt.Sprintf("| %s | %s | %s | %s%s | %s |",
				m.label, fmtOpt(c, okc, m.suffix), fmtOpt(p, okp, m.suffix),
				sign, fmtOpt(d, okd, m.suffix), verdict(d, okd, m.down)))
		}
	}
	line("")

	line("## Where the time went")
	line("")
	line("| Goal | Count |")
	line("|---|---:|")
	for _, kv := range topN(cur.Goals, 8) {
		note := ""
		if prev != nil {
			if pv, ok := prev.Goals[kv.key]; ok {
				delta := kv.count - pv
				s := ""
				if delta >= 0 {
					s = "+"
				}
				note = fmt.Sprintf(" (%s%d)", s, delta)
			}
		}
		line(fmt.Sprintf("| %s | %d%s |", kv.key, kv.count, note))
	}
	line("")

	line("## Friction")
	line("")
	if len(cur.Friction) == 0 {
		line("_None recorded._")
	} else {
		line("| Type | Count | Last week |")
		line("|---|---:|---:|")
		for _, kv := range topN(cur.Friction, 10) {
			last := "-"
			if prev != nil {
				if pv, ok := prev.Friction[kv.key]; ok {
					last = fmt.Sprintf("%d", pv)
				}
			}
			line(fmt.Sprintf("| %s | %d | %s |", kv.key, kv.count, last))
		}
	}
	line("")

	if len(cur.UserCorrections) > 0 {
		line("### What you had to correct")
		line("")
		for _, c := range cur.UserCorrections[:min(15, len(cur.UserCorrections))] {
			line(fmt.Sprintf("- `%s` \"%s\"", c.Date, c.Quote))
		}
		line("")
	}

	if len(cur.FrictionDetails) > 0 {
		line("### Friction detail")
		line("")
		for _, f := range cur.FrictionDetails[:min(12, len(cur.FrictionDetails))] {
			// The em dash here is a literal separator, matching the reference.
			line(fmt.Sprintf("- **%s %s** — %s", f.Date, f.Project, f.Detail))
		}
		line("")
	}

	line("## Tools")
	line("")
	total := 0
	for _, n := range cur.Tools {
		total += n
	}
	if total < 1 {
		total = 1
	}
	for _, kv := range topN(cur.Tools, 8) {
		line(fmt.Sprintf("- %s: %d (%d%%)", kv.key, kv.count, 100*kv.count/total))
	}
	line("")
	line("## Projects")
	line("")
	for _, kv := range topN(cur.Projects, 8) {
		line(fmt.Sprintf("- %s: %d", kv.key, kv.count))
	}
	line("")

	return b.String()
}

// Trend renders every snapshot as one table, oldest first.
func Trend(snaps []snapshot.Snapshot) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s); b.WriteByte('\n') }

	line("# Weekly trend")
	line("")
	line("| Week | Sessions | Coverage | Friction/sess | Negative % | Unverified/sess | Unsanctioned/sess | Achieved % |")
	line("|---|---|---|---|---|---|---|---|")
	for _, s := range snaps {
		line(fmt.Sprintf("| %s | %d | %s | %s | %s | %s | %s | %s |",
			s.Window.Label,
			s.Sessions.Substantive,
			ratePct(s, snapshot.RateFacetCoveragePct),
			rate(s, snapshot.RateFrictionPerSession),
			rateBarePct(s, snapshot.RateNegativePct),
			rate(s, snapshot.RateUnverifiedClaim),
			rate(s, snapshot.RateUnsanctionedAction),
			rateBarePct(s, snapshot.RateAchievedPct),
		))
	}
	line("")
	// Mixing facet sources is the one way this table lies, so the caveat ships
	// with the table rather than living in the docs.
	line("Rows built from normalized builtin facets are comparable to each other; " +
		"rows built from canonical extraction are comparable to each other. Check " +
		"`facet_source` in each snapshot before reading a jump across that boundary " +
		"as a real change.")

	return b.String()
}

type kv struct {
	key   string
	count int
}

// topN sorts by count descending, then key ascending. The secondary key is what
// makes output stable: Go map iteration order is randomized, so ties would
// otherwise shuffle between runs and show up as spurious diffs.
func topN(m map[string]int, n int) []kv {
	out := make([]kv, 0, len(m))
	for k, c := range m {
		out = append(out, kv{k, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].key < out[j].key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func rate(s snapshot.Snapshot, key string) string {
	v, ok := s.Rates[key]
	return fmtOpt(v, ok, "")
}

// ratePct renders a rate that is already a percentage, including the sign.
func ratePct(s snapshot.Snapshot, key string) string {
	v, ok := s.Rates[key]
	return fmtOpt(v, ok, "%")
}

// rateBarePct renders a percentage without the sign, for trend columns whose
// header already carries it. The fixed decimal is kept so the column aligns.
func rateBarePct(s snapshot.Snapshot, key string) string {
	v, ok := s.Rates[key]
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", v)
}

func fmtOpt(v float64, ok bool, suffix string) string {
	if !ok {
		return "n/a"
	}
	if suffix == "%" {
		return pct(v)
	}
	return num(v) + suffix
}

// pct formats a percentage with a fixed single decimal. Percentages are read
// down a column across weeks, and dropping the decimal on whole values makes
// 100 and 99.6 sit at different widths, which reads as a bigger gap than it is.
func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

// num formats a number the way the reference does: whole values print without
// decimals, and precision drops from two places to one at ten, where the extra
// digit stops carrying information.
func num(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return fmt.Sprintf("%g", v)
	}
	if math.Abs(v) < 10 {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func verdict(d float64, ok bool, down bool) string {
	if !ok || math.Abs(d) < epsilon {
		return "flat"
	}
	good := d > 0
	if down {
		good = d < 0
	}
	if good {
		return "better"
	}
	return "worse"
}

// dateOnly trims a timestamp to its date, matching the reference's [:10] slice.
func dateOnly(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
