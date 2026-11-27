package report

import (
	"fmt"
	"strconv"
	"strings"
)

// The trend page draws its charts as hand-written inline SVG rather than with
// a charting library or a script: the page has to open from a local file with
// no network, and the same snapshots must always produce the same bytes so a
// golden test can pin the markup. Colors go through CSS variables with
// fallbacks, so the page stylesheet decides them and the SVG stays readable on
// its own.

// point is one week's value; ok is false when the week has no value for the
// metric, which draws as a gap rather than a zero.
type point struct {
	v  float64
	ok bool
}

// series is one line on a chart.
type series struct {
	name   string
	points []point
}

// mark is a vertical annotation at a week index.
type mark struct {
	index int
	label string
}

// chartOpts describes everything about a chart except its data.
type chartOpts struct {
	title  string
	weeks  []string // x-axis labels, one per week, plain text
	suffix string   // "%" for percentages, as fmtOpt takes it
	marks  []mark
}

const (
	svgW  = 600
	svgH  = 180
	padL  = 56
	padR  = 20
	padT  = 22
	padB  = 28
	plotW = svgW - padL - padR
	plotH = svgH - padT - padB
)

// seriesColors are the fallbacks for --wi-series-N.
var seriesColors = []string{"#2563eb", "#d97706", "#7c3aed", "#16a34a"}

// lineChart renders one chart. The first series supplies the latest point and
// the previous-4-week mean; further series are drawn alongside it.
func lineChart(ss []series, o chartOpts) string {
	n := len(o.weeks)
	mean, okMean := prevMean(ss)
	lo, hi, okRange := valueRange(ss)
	if okMean {
		lo, hi = min(lo, mean), max(hi, mean)
	}
	flat := !okRange || hi-lo < epsilon
	x := func(i int) float64 {
		if n <= 1 {
			return padL + plotW/2.0
		}
		return padL + plotW*float64(i)/float64(n-1)
	}
	y := func(v float64) float64 {
		if flat {
			return padT + plotH/2.0
		}
		return padT + plotH*(hi-v)/(hi-lo)
	}
	val := func(v float64) string { return esc(fmtOpt(v, true, o.suffix)) }

	var b strings.Builder
	fmt.Fprintf(&b, "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 %d %d\" role=\"img\" class=\"line-chart\">\n", svgW, svgH)
	fmt.Fprintf(&b, "<title>%s</title>\n", esc(o.title))
	fmt.Fprintf(&b, "<desc>%s</desc>\n", esc(describe(ss, o, mean, okMean)))

	// Axes, with the value range on the left and the first and last week below.
	fmt.Fprintf(&b, "<path d=\"M%s %sV%sH%s\" style=\"fill:none;stroke:var(--wi-axis,#cbd5e1)\"/>\n",
		f1(padL), f1(padT), f1(padT+plotH), f1(padL+plotW))
	if okRange {
		if flat {
			ylabel(&b, y(lo), val(lo))
		} else {
			ylabel(&b, y(hi), val(hi))
			ylabel(&b, y(lo), val(lo))
		}
	}
	if n > 0 {
		anchor := "start"
		if n == 1 {
			anchor = "middle"
		}
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"%d\" text-anchor=\"%s\" style=\"%s\">%s</text>\n", f1(x(0)), svgH-8, anchor, textStyle, esc(o.weeks[0]))
		if n > 1 {
			fmt.Fprintf(&b, "<text x=\"%s\" y=\"%d\" text-anchor=\"end\" style=\"%s\">%s</text>\n", f1(x(n-1)), svgH-8, textStyle, esc(o.weeks[n-1]))
		}
	}

	if okMean {
		my := y(mean)
		fmt.Fprintf(&b, "<path d=\"M%s %sH%s\" stroke-dasharray=\"4 4\" style=\"fill:none;stroke:var(--wi-baseline,#94a3b8)\"/>\n",
			f1(padL), f1(my), f1(padL+plotW))
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"%s\" text-anchor=\"end\" style=\"%s\">4-wk mean %s</text>\n", f1(padL+plotW), f1(my-4), textStyle, val(mean))
	}

	// Annotations: a dashed vertical line, labels stacked when several share a week.
	stack := map[int]int{}
	for _, m := range o.marks {
		if m.index < 0 || m.index >= n {
			continue
		}
		mx := x(m.index)
		fmt.Fprintf(&b, "<path d=\"M%s %sV%s\" stroke-dasharray=\"2 3\" style=\"fill:none;stroke:var(--wi-mark,#dc2626)\"/>\n",
			f1(mx), f1(padT), f1(padT+plotH))
		tx, anchor := mx+4, "start"
		if mx > padL+plotW/2.0 {
			tx, anchor = mx-4, "end"
		}
		fmt.Fprintf(&b, "<text x=\"%s\" y=\"%d\" text-anchor=\"%s\" style=\"font-size:11px;fill:var(--wi-mark,#dc2626)\">%s</text>\n",
			f1(tx), padT-8+12*stack[m.index], anchor, esc(m.label))
		stack[m.index]++
	}

	for si, s := range ss {
		color := fmt.Sprintf("var(--wi-series-%d,%s)", si+1, seriesColors[si%len(seriesColors)])
		// The legend sits on the x-axis row, between the first and last week.
		if len(ss) > 1 {
			lx := padL + plotW/2.0 + 150*(float64(si)-float64(len(ss)-1)/2)
			fmt.Fprintf(&b, "<text x=\"%s\" y=\"%d\" text-anchor=\"middle\" style=\"font-size:11px;fill:%s\">%s</text>\n", f1(lx), svgH-8, color, esc(s.name))
		}
		// Consecutive present values form one polyline; a missing week ends it.
		var run []string
		flush := func() {
			if len(run) > 1 {
				fmt.Fprintf(&b, "<polyline points=\"%s\" style=\"fill:none;stroke:%s;stroke-width:2\"/>\n", strings.Join(run, " "), color)
			}
			run = run[:0]
		}
		for i := 0; i < n; i++ {
			if i >= len(s.points) || !s.points[i].ok {
				flush()
				continue
			}
			run = append(run, f1(x(i))+","+f1(y(s.points[i].v)))
		}
		flush()
		for i := 0; i < n && i < len(s.points); i++ {
			p := s.points[i]
			if !p.ok {
				continue
			}
			r, extra := "3", ""
			if i == n-1 {
				r, extra = "5", ";stroke:var(--wi-bg,#ffffff);stroke-width:2"
			}
			fmt.Fprintf(&b, "<circle cx=\"%s\" cy=\"%s\" r=\"%s\" style=\"fill:%s%s\"><title>%s: %s</title></circle>\n",
				f1(x(i)), f1(y(p.v)), r, color, extra, esc(o.weeks[i]), val(p.v))
		}
	}
	b.WriteString("</svg>")
	return b.String()
}

const textStyle = "font-size:11px;fill:var(--wi-text,#64748b)"

func ylabel(b *strings.Builder, y float64, label string) {
	fmt.Fprintf(b, "<text x=\"%d\" y=\"%s\" text-anchor=\"end\" style=\"%s\">%s</text>\n", padL-6, f1(y+4), textStyle, label)
}

// f1 formats a coordinate with one fixed decimal, so output never depends on
// shortest-representation float printing.
func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

func valueRange(ss []series) (lo, hi float64, ok bool) {
	for _, s := range ss {
		for _, p := range s.points {
			if !p.ok {
				continue
			}
			if !ok {
				lo, hi, ok = p.v, p.v, true
				continue
			}
			lo, hi = min(lo, p.v), max(hi, p.v)
		}
	}
	return lo, hi, ok
}

// prevMean is the mean of the first series' present values over the four weeks
// before the latest one: the baseline the latest week is read against.
func prevMean(ss []series) (float64, bool) {
	if len(ss) == 0 {
		return 0, false
	}
	pts := ss[0].points
	sum, k := 0.0, 0
	for i := max(0, len(pts)-5); i < len(pts)-1; i++ {
		if pts[i].ok {
			sum += pts[i].v
			k++
		}
	}
	if k == 0 {
		return 0, false
	}
	return sum / float64(k), true
}

// describe is the chart's text alternative.
func describe(ss []series, o chartOpts, mean float64, okMean bool) string {
	n := len(o.weeks)
	if len(ss) == 0 || n == 0 || len(ss[0].points) < n || !ss[0].points[n-1].ok {
		return "No value for the latest week."
	}
	d := fmt.Sprintf("Latest week %s: %s.", o.weeks[n-1], fmtOpt(ss[0].points[n-1].v, true, o.suffix))
	if okMean {
		d += fmt.Sprintf(" Previous 4-week mean %s.", fmtOpt(mean, true, o.suffix))
	}
	return d
}
