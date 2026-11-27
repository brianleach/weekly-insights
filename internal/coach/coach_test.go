package coach

import (
	"reflect"
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// snap is a synthetic snapshot ending on label with n substantive sessions.
func snap(label string, n int) snapshot.Snapshot {
	return snapshot.Snapshot{
		Window:   snapshot.Window{Label: label, Days: 7},
		Sessions: snapshot.Sessions{Substantive: n, FacetSource: map[string]int{"canonical": n}},
		Rates:    map[string]float64{},
	}
}

func corr(session, quote string) snapshot.UserCorrection {
	return snapshot.UserCorrection{Session: session, Date: "2026-01-08", Quote: quote}
}

func harness(session, detail string) failures.Failure {
	return failures.Failure{Session: session, Class: failures.ClassHarnessRule, Detail: detail, Tool: "Edit"}
}

func rulesOf(r Result) []string {
	var out []string
	for _, p := range r.Proposals {
		out = append(out, p.Rule+":"+p.Title)
	}
	return out
}

func TestRuleCorrections(t *testing.T) {
	tests := []struct {
		name     string
		now      []snapshot.UserCorrection
		before   []snapshot.UserCorrection
		wantFire bool
		count    int
	}{
		{"one session only", []snapshot.UserCorrection{corr("s1", "Run the tests first"), corr("s1", "run the tests first!")}, nil, false, 0},
		{"two sessions this window", []snapshot.UserCorrection{corr("s1", "Run the tests first."), corr("s2", "run  the TESTS first")}, nil, true, 2},
		{"this and the previous window", []snapshot.UserCorrection{corr("s3", "Run the tests first")}, []snapshot.UserCorrection{corr("s0", "run the tests, first")}, true, 2},
		{"only the previous window", nil, []snapshot.UserCorrection{corr("s0", "run the tests first"), corr("s9", "run the tests first")}, false, 0},
		{"different words", []snapshot.UserCorrection{corr("s1", "run the tests first"), corr("s2", "run the linter first")}, nil, false, 0},
		{"punctuation only", []snapshot.UserCorrection{corr("s1", "?!"), corr("s2", "...")}, nil, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, prev := snap("2026-01-10", 12), snap("2026-01-03", 12)
			cur.UserCorrections, prev.UserCorrections = tt.now, tt.before
			r := Propose(Input{Current: cur, Previous: []snapshot.Snapshot{prev}})
			if got := len(r.Proposals) == 1; got != tt.wantFire {
				t.Fatalf("fired = %v, want %v: %v", got, tt.wantFire, rulesOf(r))
			}
			if !tt.wantFire {
				return
			}
			p := r.Proposals[0]
			if p.Rule != RuleCorrection || p.Target != TargetClaudeMD || p.Evidence.Count != tt.count {
				t.Errorf("proposal = %+v", p)
			}
			if !strings.Contains(strings.ToLower(p.Change), "run the tests") || p.Score != tt.count*3 {
				t.Errorf("change %q score %d", p.Change, p.Score)
			}
			if p.Evidence.Population != 12 || len(p.Evidence.Quotes) == 0 {
				t.Errorf("evidence = %+v", p.Evidence)
			}
		})
	}
}

func TestRuleCorrectionsPicksEarliestQuoteAndTruncates(t *testing.T) {
	cur := snap("2026-01-10", 12)
	long := strings.Repeat("x", 300)
	cur.UserCorrections = []snapshot.UserCorrection{
		{Session: "b", Date: "2026-01-09", Quote: long},
		{Session: "a", Date: "2026-01-09", Quote: long + " "},
		{Session: "c", Date: "2026-01-05", Quote: " " + long},
	}
	r := Propose(Input{Current: cur})
	if len(r.Proposals) != 1 {
		t.Fatalf("got %v", rulesOf(r))
	}
	if n := strings.Count(r.Proposals[0].Change, "x"); n != maxQuoteRunes {
		t.Errorf("quote has %d runes, want %d", n, maxQuoteRunes)
	}
	if !reflect.DeepEqual(r.Proposals[0].Evidence.SessionIDs, []string{"a", "b", "c"}) {
		t.Errorf("sessions %v", r.Proposals[0].Evidence.SessionIDs)
	}
}

// rateWindows builds three windows with the given unverified-claim rates.
func rateWindows(n int, rates ...float64) (snapshot.Snapshot, []snapshot.Snapshot) {
	labels := []string{"2025-12-27", "2026-01-03", "2026-01-10"}
	var ws []snapshot.Snapshot
	for i, v := range rates {
		s := snap(labels[i], n)
		s.Rates[snapshot.RateUnverifiedClaim] = v
		ws = append(ws, s)
	}
	return ws[len(ws)-1], ws[:len(ws)-1]
}

func TestRuleFriction(t *testing.T) {
	tests := []struct {
		name     string
		sessions int
		rates    []float64
		wantFire bool
	}{
		{"rising above floor", 10, []float64{0.2, 0.3, 0.4}, true},
		{"flat at floor", 10, []float64{0.1, 0.1, 0.1}, true},
		{"just under floor now", 10, []float64{0.2, 0.3, 0.09}, false},
		{"previous under floor", 10, []float64{0.05, 0.09, 0.3}, false},
		{"down this window", 10, []float64{0.2, 0.4, 0.3}, false},
		{"down last window", 10, []float64{0.4, 0.3, 0.5}, false},
		{"small sample", 9, []float64{0.2, 0.3, 0.4}, false},
		{"one previous window", 10, []float64{0.3, 0.4}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, prev := rateWindows(tt.sessions, tt.rates...)
			r := Propose(Input{Current: cur, Previous: prev})
			if got := len(r.Proposals) == 1; got != tt.wantFire {
				t.Fatalf("fired = %v, want %v: %v %v", got, tt.wantFire, rulesOf(r), r.Notes)
			}
			if tt.wantFire {
				p := r.Proposals[0]
				if p.Rule != RuleFriction || len(p.Evidence.Weeks) != 3 || p.Evidence.Sessions != tt.sessions {
					t.Errorf("proposal = %+v", p)
				}
			}
		})
	}
}

func TestRuleFrictionSmallSampleNote(t *testing.T) {
	cur, prev := rateWindows(10, 0.2, 0.3, 0.4)
	prev[0].Sessions.Substantive = 9
	r := Propose(Input{Current: cur, Previous: prev})
	if len(r.Proposals) != 0 || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "2025-12-27 has 9") {
		t.Errorf("proposals %v notes %v", rulesOf(r), r.Notes)
	}
}

func TestRuleFrictionFacetSourceChange(t *testing.T) {
	cur, prev := rateWindows(10, 0.2, 0.3, 0.4)
	cur.Sessions.FacetSource = map[string]int{"canonical": 6, "builtin": 4}
	r := Propose(Input{Current: cur, Previous: prev})
	if len(r.Proposals) != 0 || len(r.Notes) != 1 ||
		!strings.Contains(r.Notes[0], "facet source changed") || !strings.Contains(r.Notes[0], "builtin+canonical") {
		t.Errorf("proposals %v notes %v", rulesOf(r), r.Notes)
	}
	cur.Sessions.FacetSource = nil
	if r := Propose(Input{Current: cur, Previous: prev}); !strings.Contains(strings.Join(r.Notes, ""), "(none)") {
		t.Errorf("notes %v", r.Notes)
	}
}

func TestRuleHarness(t *testing.T) {
	tests := []struct {
		name     string
		fs       []failures.Failure
		wantFire bool
	}{
		{"two is under", []failures.Failure{harness("a", "edit_before_read"), harness("b", "edit_before_read")}, false},
		{"three fires", []failures.Failure{harness("a", "edit_before_read"), harness("a", "edit_before_read"), harness("b", "edit_before_read")}, true},
		{"unknown guardrail", []failures.Failure{harness("a", "new_rule"), harness("a", "new_rule"), harness("a", "new_rule")}, false},
		{"empty detail", []failures.Failure{harness("a", ""), harness("a", ""), harness("a", "")}, false},
		{"other class", []failures.Failure{{Class: failures.ClassOther, Detail: "sleep_chain"}, {Class: failures.ClassOther, Detail: "sleep_chain"}, {Class: failures.ClassOther, Detail: "sleep_chain"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Propose(Input{Current: snap("2026-01-10", 4), Failures: tt.fs})
			if got := len(r.Proposals) == 1; got != tt.wantFire {
				t.Fatalf("fired = %v, want %v: %v", got, tt.wantFire, rulesOf(r))
			}
			if tt.wantFire {
				e := r.Proposals[0].Evidence
				if e.Count != 3 || e.Sessions != 2 || e.Population != 4 || r.Proposals[0].Score != 6 {
					t.Errorf("proposal %+v", r.Proposals[0])
				}
			}
		})
	}
	for detail := range harnessFixes {
		fs := []failures.Failure{harness("a", detail), harness("a", detail), harness("a", detail)}
		if r := Propose(Input{Current: snap("2026-01-10", 4), Failures: fs}); len(r.Proposals) != 1 {
			t.Errorf("%s did not fire", detail)
		}
	}
}

// fixture fires one proposal of each of R1, R2 and R3.
func fixture() Input {
	cur, prev := rateWindows(12, 0.2, 0.3, 0.4)
	cur.UserCorrections = []snapshot.UserCorrection{corr("s1", "Use tabs"), corr("s2", "use tabs.")}
	var fs []failures.Failure
	for _, s := range []string{"a", "b", "c", "d"} {
		fs = append(fs, harness(s, "sleep_chain"))
	}
	return Input{Current: cur, Previous: prev, Failures: fs}
}

func TestStableIDsAndOrder(t *testing.T) {
	in := fixture()
	a := Propose(in)
	if len(a.Proposals) != 3 {
		t.Fatalf("got %v", rulesOf(a))
	}
	// Reverse every input slice: same proposals, same IDs, same order.
	rev := fixture()
	rev.Previous = []snapshot.Snapshot{rev.Previous[1], rev.Previous[0]}
	c := rev.Current.UserCorrections
	rev.Current.UserCorrections = []snapshot.UserCorrection{c[1], c[0]}
	for i, j := 0, len(rev.Failures)-1; i < j; i, j = i+1, j-1 {
		rev.Failures[i], rev.Failures[j] = rev.Failures[j], rev.Failures[i]
	}
	if b := Propose(rev); !reflect.DeepEqual(a, b) {
		t.Errorf("reordered input changed the result:\n%+v\n%+v", a, b)
	}
	if b := Propose(in); !reflect.DeepEqual(a, b) {
		t.Errorf("second run differs")
	}
	for i := 1; i < len(a.Proposals); i++ {
		if a.Proposals[i-1].Score < a.Proposals[i].Score {
			t.Errorf("not ranked by score: %v", rulesOf(a))
		}
	}
	// Same finding, cosmetically different change: same ID.
	if proposalID("R1", TargetClaudeMD, "- Use tabs.") != proposalID("R1", TargetClaudeMD, "- use  TABS") {
		t.Errorf("ID not stable across cosmetic changes")
	}
	if proposalID("R1", TargetClaudeMD, "x") == proposalID("R3", TargetClaudeMD, "x") {
		t.Errorf("ID ignores the rule")
	}
}

func TestTiesBreakByID(t *testing.T) {
	ps := []Proposal{{ID: "b", Score: 2, Status: StatusNew}, {ID: "a", Score: 2, Status: StatusNew}, {ID: "c", Score: 9, Status: StatusAlreadyPresent}}
	rank(ps)
	if ps[0].ID != "a" || ps[1].ID != "b" || ps[2].ID != "c" {
		t.Errorf("order %v", ps)
	}
}

func TestAlreadyPresent(t *testing.T) {
	in := fixture()
	in.ClaudeMD = "# Mine\n\n- do NOT wait with sleep loops, or chained sleeps; run long commands in the background and check on them\n"
	r := Propose(in)
	last := r.Proposals[len(r.Proposals)-1]
	if last.Rule != RuleHarness || last.Status != StatusAlreadyPresent {
		t.Errorf("last = %+v", last)
	}
	for _, p := range r.Proposals[:len(r.Proposals)-1] {
		if p.Status != StatusNew {
			t.Errorf("%s status %s", p.Rule, p.Status)
		}
	}
	allow := Proposal{Target: TargetSettingsAllow, Change: "Bash(ls:*)"}
	if !present(&allow, "", map[string]bool{"Bash(ls:*)": true}) || present(&allow, "bash ls", nil) {
		t.Errorf("allow-list detection wrong")
	}
	if present(&Proposal{Target: TargetClaudeMD, Change: "..."}, "anything", nil) {
		t.Errorf("empty change counted as present")
	}
}

func TestPreviousFilteredByWindowLength(t *testing.T) {
	cur, prev := rateWindows(10, 0.2, 0.3, 0.4)
	prev[0].Window.Days = 30
	later := snap("2026-02-01", 10)
	r := Propose(Input{Current: cur, Previous: append(prev, later)})
	if len(r.Proposals) != 0 {
		t.Errorf("used a snapshot of another window length or a later one: %v", rulesOf(r))
	}
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Don't  push!":  "don t push",
		"  ...  ":       "",
		"Ünïcode, OK?":  "ünïcode ok",
		"a\n\tb--c 1.2": "a b c 1 2",
	} {
		if got := fold(in); got != want {
			t.Errorf("fold(%q) = %q, want %q", in, got, want)
		}
	}
}
