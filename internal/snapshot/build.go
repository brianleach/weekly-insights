package snapshot

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/brianleach/weekly-insights/internal/model"
	"github.com/brianleach/weekly-insights/internal/store"
	"github.com/brianleach/weekly-insights/internal/taxonomy"
	"github.com/brianleach/weekly-insights/internal/window"
)

// dateLen is the length of a YYYY-MM-DD prefix on an ISO timestamp. Dates are
// taken from the raw start_time string rather than a parsed time so a session
// lands on the day it was recorded in, whatever offset the timestamp carries.
const dateLen = 10

// sessionPrefixLen is how much of a session UUID is quoted in the report. Eight
// characters is enough to find the transcript again without turning the
// friction table into a wall of identifiers.
const sessionPrefixLen = 8

// maxCorrectionsPerSession caps how many quotes one session can contribute, so
// a single argumentative session cannot dominate next week's follow-up list.
const maxCorrectionsPerSession = 6

// maxQuoteChars trims a quote to something that fits a report row.
const maxQuoteChars = 160

// Build aggregates a window.Result into a Snapshot, normalizing every label.
// collapses, when non-nil, records canonical -> set of raw labels seen, for --explain.
func Build(r window.Result, days int, collapses map[string]map[string]bool) Snapshot {
	s := Snapshot{
		Window: Window{
			Start: r.Start.Format(time.RFC3339),
			End:   r.End.Format(time.RFC3339),
			Days:  days,
			Label: r.End.Format("2006-01-02"),
		},
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		Sessions:            Sessions{InWindow: len(r.All), ExcludedScratch: r.ExcludedScratch, Substantive: len(r.Substantive), FacetSource: map[string]int{}},
		Tools:               map[string]int{},
		Projects:            map[string]int{},
		Goals:               map[string]int{},
		Friction:            map[string]int{},
		Satisfaction:        map[string]int{},
		Outcomes:            map[string]int{},
		Helpfulness:         map[string]int{},
		SessionTypes:        map[string]int{},
		Success:             map[string]int{},
		ToolErrorCategories: map[string]int{},
		FrictionDetails:     []FrictionDetail{},
		UserCorrections:     []UserCorrection{},
		Rates:               map[string]float64{},
	}

	var (
		hours     float64
		responses []float64
		daysSeen  = map[string]bool{}
	)

	for _, sess := range r.Substantive {
		m := sess.Meta
		s.Volume.Messages += m.UserMessageCount
		hours += m.DurationMinutes / 60.0
		s.Volume.Commits += m.GitCommits
		s.Volume.Pushes += m.GitPushes
		s.Volume.LinesAdded += m.LinesAdded
		s.Volume.LinesRemoved += m.LinesRemoved
		s.Volume.FilesModified += m.FilesModified
		s.Volume.InputTokens += m.InputTokens
		s.Volume.OutputTokens += m.OutputTokens
		s.Volume.Interruptions += m.UserInterruptions
		s.Volume.ToolErrors += m.ToolErrors
		responses = append(responses, m.UserResponseTimes...)
		if d := dateOf(m); d != "" {
			daysSeen[d] = true
		}

		project := m.ProjectPath
		if project == "" {
			project = "?"
		}
		s.Projects[project]++
		for k, n := range m.ToolCounts {
			s.Tools[k] += n
		}
		for k, n := range m.ToolErrorCategories {
			s.ToolErrorCategories[k] += n
		}

		if sess.Facets == nil {
			continue
		}
		f := sess.Facets
		s.Sessions.WithFacets++
		s.Sessions.FacetSource[sess.FacetSource]++

		for raw, n := range f.GoalCategories {
			if n <= 0 {
				continue
			}
			c := taxonomy.NormGoal(raw)
			recordCollapse(collapses, "goal:"+c, raw)
			s.Goals[c] += n
		}
		for raw, n := range f.FrictionCounts {
			if n <= 0 {
				continue
			}
			c := taxonomy.NormFriction(raw)
			recordCollapse(collapses, "friction:"+c, raw)
			s.Friction[c] += n
		}
		for raw, n := range f.Satisfaction {
			if n <= 0 {
				continue
			}
			s.Satisfaction[taxonomy.NormSatisfaction(raw)] += n
		}

		s.Outcomes[taxonomy.NormEnum(f.Outcome, taxonomy.Outcomes, "unclear_from_transcript")]++
		s.Helpfulness[taxonomy.NormEnum(f.Helpfulness, taxonomy.Helpfulness, "very_helpful")]++
		s.SessionTypes[taxonomy.NormEnum(f.SessionType, taxonomy.SessionTypes, "multi_task")]++
		// "none" is the fallback for a missing or unrecognized value, so counting
		// it would inflate the success table with sessions that reported nothing.
		if success := taxonomy.NormEnum(f.PrimarySuccess, taxonomy.Successes, "none"); success != "none" {
			s.Success[success]++
		}

		if detail := strings.TrimSpace(f.FrictionDetail); detail != "" {
			s.FrictionDetails = append(s.FrictionDetails, FrictionDetail{
				Session: shortID(m.SessionID),
				Date:    dateOf(m),
				Project: filepath.Base(orQuestion(m.ProjectPath)),
				Detail:  detail,
			})
		}
		quotes := f.UserCorrections
		if len(quotes) > maxCorrectionsPerSession {
			quotes = quotes[:maxCorrectionsPerSession]
		}
		for _, q := range quotes {
			q = strings.TrimSpace(q)
			if q == "" {
				continue
			}
			s.UserCorrections = append(s.UserCorrections, UserCorrection{
				Session: shortID(m.SessionID),
				Date:    dateOf(m),
				Quote:   truncate(q, maxQuoteChars),
			})
		}
	}

	s.Volume.Hours = round(hours, 1)
	s.Volume.DaysActive = len(daysSeen)
	s.Volume.MedianResponseSeconds = round(median(responses), 1)

	// Facet-derived counts are only defined over sessions that have facets;
	// dividing them by the substantive count would understate them whenever
	// facet coverage is partial. Meta-derived counts cover every substantive
	// session and use that wider denominator.
	subst := float64(atLeastOne(len(r.Substantive)))
	withFacets := float64(atLeastOne(s.Sessions.WithFacets))
	satTotal := float64(atLeastOne(sum(s.Satisfaction)))

	s.Rates[RateSessionsPerDay] = round(float64(len(r.Substantive))/float64(atLeastOne(len(daysSeen))), 2)
	s.Rates[RateFrictionPerSession] = round(float64(sum(s.Friction))/withFacets, 2)
	s.Rates[RateNegativePct] = round(100*float64(negativeSatisfaction(s.Satisfaction))/satTotal, 1)
	s.Rates[RateInterruptions] = round(float64(s.Volume.Interruptions)/subst, 2)
	s.Rates[RateToolErrors] = round(float64(s.Volume.ToolErrors)/subst, 2)
	s.Rates[RateAchievedPct] = round(100*float64(s.Outcomes["fully_achieved"]+s.Outcomes["mostly_achieved"])/withFacets, 1)
	s.Rates[RateFacetCoveragePct] = round(100*float64(s.Sessions.WithFacets)/subst, 1)
	s.Rates[RateUnverifiedClaim] = round(float64(s.Friction[taxonomy.FrictionUnverifiedClaim])/withFacets, 2)
	s.Rates[RateUnsanctionedAction] = round(float64(s.Friction[taxonomy.FrictionUnsanctionedAction])/withFacets, 2)
	s.Rates[RateIgnoredPreference] = round(float64(s.Friction[taxonomy.FrictionIgnoredPreference])/withFacets, 2)

	return s
}

// defaultDays is the window length that keeps the unsuffixed filename. See
// the Window doc comment for why.
const defaultDays = 7

// FileName is the on-disk name for a snapshot of the given window. A zero or
// negative day count is treated as the default so a hand-built Snapshot with
// no Days set still lands on the legacy name rather than "label-0d.json".
func FileName(label string, days int) string {
	if days <= 0 || days == defaultDays {
		return label + ".json"
	}
	return fmt.Sprintf("%s-%dd.json", label, days)
}

// Save writes a snapshot under its label and window length.
func Save(p store.Paths, s Snapshot) (string, error) {
	path := filepath.Join(p.Snapshots(), FileName(s.Window.Label, s.Window.Days))
	if err := store.WriteJSON(path, s); err != nil {
		return "", fmt.Errorf("saving snapshot %s: %w", s.Window.Label, err)
	}
	return path, nil
}

// LoadWindow reads the snapshot for one label and window length.
func LoadWindow(p store.Paths, label string, days int) (Snapshot, error) {
	return readSnapshot(filepath.Join(p.Snapshots(), FileName(label, days)))
}

// Load reads one snapshot by label, preferring the 7-day form and falling
// back to whichever other window length is on disk for that date. Callers that
// know the window they want should use LoadWindow; this exists so a label
// typed on the command line still resolves when only a 30-day snapshot was
// ever saved for it.
func Load(p store.Paths, label string) (Snapshot, error) {
	path := filepath.Join(p.Snapshots(), FileName(label, defaultDays))
	if _, err := os.Stat(path); err != nil {
		matches, gerr := filepath.Glob(filepath.Join(p.Snapshots(), label+"-*d.json"))
		if gerr == nil && len(matches) > 0 {
			sort.Strings(matches)
			path = matches[0]
		}
	}
	return readSnapshot(path)
}

func readSnapshot(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reading snapshot %s: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parsing snapshot %s: %w", path, err)
	}
	return s, nil
}

// List returns every saved snapshot, oldest first, so callers can take the
// last one as "previous" without re-sorting. Snapshots of different window
// lengths ending on the same date sort next to each other, shortest first.
func List(p store.Paths) ([]Snapshot, error) {
	// One glob covers both filename forms, "<label>.json" and
	// "<label>-<days>d.json"; the window length is read back from the file
	// rather than parsed out of the name.
	matches, err := filepath.Glob(filepath.Join(p.Snapshots(), "*.json"))
	if err != nil {
		return nil, fmt.Errorf("globbing snapshots: %w", err)
	}
	out := make([]Snapshot, 0, len(matches))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var s Snapshot
		// A half-written or hand-edited file should not break the trend line.
		if err := json.Unmarshal(b, &s); err != nil || s.Window.Label == "" {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Window.Label != out[j].Window.Label {
			return out[i].Window.Label < out[j].Window.Label
		}
		return out[i].Window.Days < out[j].Window.Days
	})
	return out, nil
}

// recordCollapse notes that raw collapsed onto canonical. The key is prefixed
// with its kind because goals and frictions share label names: both have an
// "other" bucket, and merging them would make --explain lie about where a
// label came from.
func recordCollapse(collapses map[string]map[string]bool, canonical, raw string) {
	if collapses == nil {
		return
	}
	if collapses[canonical] == nil {
		collapses[canonical] = map[string]bool{}
	}
	collapses[canonical][raw] = true
}

func dateOf(m model.SessionMeta) string {
	if len(m.StartTime) < dateLen {
		return m.StartTime
	}
	return m.StartTime[:dateLen]
}

func shortID(id string) string {
	if len(id) <= sessionPrefixLen {
		return id
	}
	return id[:sessionPrefixLen]
}

// truncate cuts on rune boundaries. A byte slice would split a multi-byte
// character and put invalid UTF-8 into the snapshot JSON.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func orQuestion(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func sum(m map[string]int) int {
	t := 0
	for _, v := range m {
		t += v
	}
	return t
}

func negativeSatisfaction(m map[string]int) int {
	t := 0
	for k, v := range m {
		if taxonomy.IsNegative(k) {
			t += v
		}
	}
	return t
}

// atLeastOne keeps every rate denominator away from zero. An empty window
// should report zeroes, not NaN or a panic.
func atLeastOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

func round(v float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(v*f) / f
}
