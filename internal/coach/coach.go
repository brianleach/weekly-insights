// Package coach turns what the tool measures into ranked, concrete changes to
// the user's Claude Code setup.
//
// Every rule here is a pure function over data the tool already has: the
// current snapshot, the snapshots before it, the window's classified failures,
// the global CLAUDE.md and the settings allow list. No rule calls a model, so
// the same inputs give the same proposals, in the same order, with the same
// IDs, which is what lets a later run check whether a proposal was applied and
// whether it stuck.
//
// The package only proposes. It never writes to the Claude config directory.
package coach

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"

	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// Targets: the file a proposal would change.
const (
	// TargetClaudeMD is the global CLAUDE.md in the Claude config directory.
	TargetClaudeMD = "claude_md"
	// TargetSettingsAllow is permissions.allow in the global settings.json.
	TargetSettingsAllow = "settings_allow"
)

// Statuses. Applied is reserved for the apply step, which this package does
// not perform.
const (
	StatusNew            = "new"
	StatusAlreadyPresent = "already_present"
	StatusApplied        = "applied"
)

// Rule names.
const (
	RuleCorrection = "R1"
	RuleFriction   = "R2"
	RuleHarness    = "R3"
	RuleDenied     = "R4"
	RuleShell      = "R5"
)

// weights scale a rule's recurrence count into a score. A correction the user
// typed themselves outranks a counted pattern of the same size, because it is
// the most direct evidence of what they want.
var weights = map[string]int{
	RuleCorrection: 3,
	RuleFriction:   2,
	RuleHarness:    2,
	RuleDenied:     1,
	RuleShell:      1,
}

// Proposal is one concrete change and the evidence behind it.
type Proposal struct {
	// ID is a hash of rule, target and normalized change, so the same finding
	// next week carries the same ID.
	ID     string `json:"id"`
	Rule   string `json:"rule"`
	Title  string `json:"title"`
	Target string `json:"target"`
	// Change is the exact block to append to CLAUDE.md, or the exact allow
	// rule string.
	Change   string   `json:"change"`
	Evidence Evidence `json:"evidence"`
	Score    int      `json:"score"`
	Risk     string   `json:"risk"`
	Status   string   `json:"status"`
}

// Evidence is what a proposal rests on.
type Evidence struct {
	// Count is the recurrence the score is built from.
	Count int `json:"count"`
	// Sessions is how many sessions the finding was seen in, and Population
	// the substantive sessions of the current window, so a reader can judge
	// the sample.
	Sessions   int         `json:"sessions"`
	Population int         `json:"population"`
	Weeks      []WeekCount `json:"weeks"`
	SessionIDs []string    `json:"session_ids,omitempty"`
	Quotes     []string    `json:"quotes,omitempty"`
}

// WeekCount is one window's measurement. Rate is set for rate rules.
type WeekCount struct {
	Window string  `json:"window"`
	Count  int     `json:"count"`
	Rate   float64 `json:"rate,omitempty"`
}

// Input is everything the rules read.
type Input struct {
	Current snapshot.Snapshot
	// Previous may hold any snapshots in any order; only those of the same
	// window length that end before Current are used.
	Previous []snapshot.Snapshot
	Failures []failures.Failure
	ClaudeMD string
	Allow    []string
}

// Result is the ranked proposals plus the reasons any rule stood down.
type Result struct {
	Proposals []Proposal `json:"proposals"`
	Notes     []string   `json:"notes,omitempty"`
}

// rule is one pure rule. It returns its proposals and any notes.
type rule func(cur snapshot.Snapshot, prev []snapshot.Snapshot, fs []failures.Failure) ([]Proposal, []string)

var rules = []rule{ruleCorrections, ruleFriction, ruleHarness}

// Propose runs every rule and returns the ranked result.
func Propose(in Input) Result {
	prev := earlier(in.Current, in.Previous)
	res := Result{Proposals: []Proposal{}}
	for _, r := range rules {
		ps, notes := r(in.Current, prev, in.Failures)
		res.Proposals = append(res.Proposals, ps...)
		res.Notes = append(res.Notes, notes...)
	}
	allow := map[string]bool{}
	for _, a := range in.Allow {
		allow[strings.TrimSpace(a)] = true
	}
	claudeMD := fold(in.ClaudeMD)
	for i := range res.Proposals {
		p := &res.Proposals[i]
		p.ID = proposalID(p.Rule, p.Target, p.Change)
		p.Score = p.Evidence.Count * weights[p.Rule]
		p.Status = StatusNew
		if present(p, claudeMD, allow) {
			p.Status = StatusAlreadyPresent
		}
	}
	rank(res.Proposals)
	return res
}

// earlier keeps the snapshots of the current window length that end before
// it, oldest first, so input order never changes the result.
func earlier(cur snapshot.Snapshot, all []snapshot.Snapshot) []snapshot.Snapshot {
	var out []snapshot.Snapshot
	for _, s := range all {
		if s.Window.Days == cur.Window.Days && s.Window.Label < cur.Window.Label {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Window.Label < out[j].Window.Label })
	return out
}

func present(p *Proposal, claudeMD string, allow map[string]bool) bool {
	if p.Target == TargetSettingsAllow {
		return allow[p.Change]
	}
	f := fold(p.Change)
	return f != "" && strings.Contains(claudeMD, f)
}

// rank orders new before already present, then by score, then by ID, so the
// order is total and deterministic.
func rank(ps []Proposal) {
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if (a.Status == StatusNew) != (b.Status == StatusNew) {
			return a.Status == StatusNew
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.ID < b.ID
	})
}

// proposalID is stable across runs and across cosmetic edits to the change.
func proposalID(rule, target, change string) string {
	h := sha256.Sum256([]byte(rule + "\x00" + target + "\x00" + fold(change)))
	return hex.EncodeToString(h[:6])
}

// fold lowercases and turns every run of non-letters and non-digits into one
// space, so "Don't  push!" and "don t push" compare equal.
func fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

// sortedKeys returns a map's keys in order.
func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
