package coach

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// R1 thresholds. A correction counts as recurring when the same normalized
// quote appears in MinCorrectionSessions sessions of this window, or in this
// window and the one before it.
const (
	MinCorrectionSessions = 2
	// maxQuoteRunes keeps a proposed CLAUDE.md line to one readable line.
	maxQuoteRunes = 200
)

// R2 thresholds. A friction rate fires when it is at least RateFloor per
// session and has not gone down for RisingWindows consecutive windows. It
// stands down when any compared window has fewer than MinRateSessions
// substantive sessions, or when the facet source changed between them.
const (
	RateFloor       = 0.1
	RisingWindows   = 2
	MinRateSessions = 10
)

// R3 threshold: the same harness guardrail tripped this many times.
const MinHarnessRepeats = 3

// frictionRates are the rates R2 watches, each with the fixed instruction it
// proposes.
var frictionRates = []struct{ key, title, change string }{
	{snapshot.RateUnverifiedClaim, "Verify before reporting done",
		"- Before reporting a task done, state how you verified it: the command you ran and what it showed. If you could not verify it, say so."},
	{snapshot.RateUnsanctionedAction, "Ask before acting beyond the request",
		"- Do not take actions the user did not ask for, such as commits, pushes, deletions, installs or config changes. Propose them and wait for a yes."},
	{snapshot.RateIgnoredPreference, "Keep stated preferences",
		"- When the user states a preference, follow it for the rest of the session instead of going back to your default."},
}

// harnessFixes maps a harness guardrail (failures.Failure.Detail) to the fixed
// instruction that prevents it. A guardrail missing here proposes nothing.
var harnessFixes = map[string]struct{ title, change string }{
	"edit_before_read": {"Read before editing",
		"- Read a file before editing or overwriting it."},
	"sleep_chain": {"No sleep chains",
		"- Do not wait with sleep loops or chained sleeps. Run long commands in the background and check on them."},
	"worktree_verify": {"Keep commands verifiable",
		"- Keep each shell call to one plain command, with no cd prefix or chaining, so the harness can verify it."},
	"run_in_background": {"Use background runs as the harness asks",
		"- Start long-running commands with run_in_background and do not block on them in the foreground."},
}

// ruleCorrections is R1, the recurring correction.
func ruleCorrections(cur snapshot.Snapshot, prev []snapshot.Snapshot, _ []failures.Failure) ([]Proposal, []string) {
	type group struct {
		now, before map[string]bool
		quotes      map[string]bool
		first       snapshot.UserCorrection
	}
	groups := map[string]*group{}
	get := func(key string) *group {
		g := groups[key]
		if g == nil {
			g = &group{now: map[string]bool{}, before: map[string]bool{}, quotes: map[string]bool{}}
			groups[key] = g
		}
		return g
	}
	for _, c := range cur.UserCorrections {
		key := fold(c.Quote)
		if key == "" {
			continue
		}
		g := get(key)
		g.now[c.Session] = true
		g.quotes[oneLine(c.Quote)] = true
		if g.first.Quote == "" || earlierCorrection(c, g.first) {
			g.first = c
		}
	}
	var last snapshot.Snapshot
	if len(prev) > 0 {
		last = prev[len(prev)-1]
		for _, c := range last.UserCorrections {
			if g := groups[fold(c.Quote)]; g != nil {
				g.before[c.Session] = true
				g.quotes[oneLine(c.Quote)] = true
			}
		}
	}
	var out []Proposal
	for _, key := range sortedKeys(groups) {
		g := groups[key]
		if len(g.now) < MinCorrectionSessions && len(g.before) == 0 {
			continue
		}
		sessions := map[string]bool{}
		for s := range g.now {
			sessions[s] = true
		}
		for s := range g.before {
			sessions[s] = true
		}
		weeks := []WeekCount{{Window: cur.Window.Label, Count: len(g.now)}}
		if len(g.before) > 0 {
			weeks = append([]WeekCount{{Window: last.Window.Label, Count: len(g.before)}}, weeks...)
		}
		out = append(out, Proposal{
			Rule:   RuleCorrection,
			Title:  "Recurring correction",
			Target: TargetClaudeMD,
			Change: fmt.Sprintf("- Recurring correction, follow it without being reminded: %q", truncateRunes(oneLine(g.first.Quote), maxQuoteRunes)),
			Risk:   "low",
			Evidence: Evidence{
				Count: len(sessions), Sessions: len(sessions), Population: cur.Sessions.Substantive,
				Weeks: weeks, SessionIDs: sortedKeys(sessions), Quotes: sortedKeys(g.quotes),
			},
		})
	}
	return out, nil
}

// ruleFriction is R2, the friction rate that is not going down.
func ruleFriction(cur snapshot.Snapshot, prev []snapshot.Snapshot, _ []failures.Failure) ([]Proposal, []string) {
	if len(prev) < RisingWindows {
		return nil, nil
	}
	ws := append(append([]snapshot.Snapshot{}, prev[len(prev)-RisingWindows:]...), cur)
	for _, w := range ws {
		if w.Sessions.Substantive < MinRateSessions {
			return nil, []string{fmt.Sprintf("R2 stood down: window %s has %d substantive sessions, fewer than %d",
				w.Window.Label, w.Sessions.Substantive, MinRateSessions)}
		}
	}
	for i := 1; i < len(ws); i++ {
		a, b := sources(ws[i-1]), sources(ws[i])
		if a != b {
			return nil, []string{fmt.Sprintf("R2 stood down: facet source changed between %s (%s) and %s (%s)",
				ws[i-1].Window.Label, a, ws[i].Window.Label, b)}
		}
	}
	var out []Proposal
	for _, fr := range frictionRates {
		fires := true
		var weeks []WeekCount
		for i, w := range ws {
			v := w.Rates[fr.key]
			weeks = append(weeks, WeekCount{Window: w.Window.Label, Count: w.Sessions.Substantive, Rate: v})
			if i > 0 && (v < RateFloor || v < ws[i-1].Rates[fr.key]) {
				fires = false
			}
		}
		if !fires {
			continue
		}
		out = append(out, Proposal{
			Rule: RuleFriction, Title: fr.title, Target: TargetClaudeMD, Change: fr.change, Risk: "low",
			Evidence: Evidence{
				Count: RisingWindows, Sessions: cur.Sessions.Substantive,
				Population: cur.Sessions.Substantive, Weeks: weeks,
			},
		})
	}
	return out, nil
}

// ruleHarness is R3, the harness guardrail that keeps tripping.
func ruleHarness(cur snapshot.Snapshot, _ []snapshot.Snapshot, fs []failures.Failure) ([]Proposal, []string) {
	var out []Proposal
	for _, g := range groupFailures(fs, func(f failures.Failure) string {
		if f.Class != failures.ClassHarnessRule {
			return ""
		}
		return f.Detail
	}) {
		fix, ok := harnessFixes[g.key]
		if !ok || g.count < MinHarnessRepeats {
			continue
		}
		out = append(out, Proposal{
			Rule: RuleHarness, Title: fix.title, Target: TargetClaudeMD, Change: fix.change, Risk: "low",
			Evidence: g.evidence(cur),
		})
	}
	return out, nil
}

// failureGroup is the failures sharing one key.
type failureGroup struct {
	key      string
	count    int
	sessions map[string]bool
}

func (g failureGroup) evidence(cur snapshot.Snapshot) Evidence {
	return Evidence{
		Count: g.count, Sessions: len(g.sessions), Population: cur.Sessions.Substantive,
		Weeks:      []WeekCount{{Window: cur.Window.Label, Count: g.count}},
		SessionIDs: sortedKeys(g.sessions),
	}
}

// groupFailures groups by key, skipping an empty key, in key order.
func groupFailures(fs []failures.Failure, key func(failures.Failure) string) []failureGroup {
	m := map[string]*failureGroup{}
	for _, f := range fs {
		k := key(f)
		if k == "" {
			continue
		}
		g := m[k]
		if g == nil {
			g = &failureGroup{key: k, sessions: map[string]bool{}}
			m[k] = g
		}
		g.count++
		g.sessions[f.Session] = true
	}
	out := make([]failureGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// sources names a window's facet sources, for comparing windows.
func sources(s snapshot.Snapshot) string {
	set := map[string]bool{}
	for k, n := range s.Sessions.FacetSource {
		if n > 0 {
			set[k] = true
		}
	}
	if len(set) == 0 {
		return "none"
	}
	return strings.Join(sortedKeys(set), "+")
}

func earlierCorrection(a, b snapshot.UserCorrection) bool {
	if a.Date != b.Date {
		return a.Date < b.Date
	}
	if a.Session != b.Session {
		return a.Session < b.Session
	}
	return a.Quote < b.Quote
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
