package failures

import (
	"fmt"
	"strings"

	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// Report renders a summary as the plain text the command prints. Columns are
// padded to a fixed width so the counts line up when read down the page.
func Report(s Summary, window string) string {
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	line("window       : %s", window)
	line("sessions     : %d substantive", s.Sessions)
	line("failures     : %d total, %s per session", s.Total, num(s.PerSession))
	line("split        : %d self-inflicted, %d external", s.SelfInflicted, s.External)
	line("")

	line("by class:")
	if len(s.ByClass) == 0 {
		line("   (none)")
	}
	for _, c := range s.ByClass {
		line("   %-20s %5d   %5s /session", c.Class, c.Count, num(c.PerSession))
	}

	if len(s.Shapes) > 0 {
		line("")
		line("classifier_denied by command shape:")
		for _, sh := range s.Shapes {
			line("   %-20s %5d", sh.Key, sh.Count)
		}
	}

	if len(s.TopSignatures) > 0 {
		line("")
		line("top signatures:")
		for _, sig := range s.TopSignatures {
			line("   %5d  %-20s %s", sig.Count, sig.Class, sig.Signature)
			if sig.Example != "" {
				line("          example: %s", sig.Example)
			}
		}
	}
	return b.String()
}

// SnapshotSummary is the subset of a summary that a snapshot carries, so a
// trend line can cite failures without storing every incident. The field is
// additive and a pointer: snapshots written before this existed stay readable
// and simply report no failure block.
func SnapshotSummary(s Summary) *snapshot.FailureSummary {
	byClass := map[string]int{}
	for _, c := range s.ByClass {
		byClass[c.Class] = c.Count
	}
	return &snapshot.FailureSummary{
		Total:         s.Total,
		ByClass:       byClass,
		PerSession:    s.PerSession,
		SelfInflicted: s.SelfInflicted,
		External:      s.External,
	}
}

// num prints a rate with two decimals, matching how the snapshot rounds it.
func num(v float64) string { return fmt.Sprintf("%.2f", v) }
