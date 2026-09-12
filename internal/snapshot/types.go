// Package snapshot turns a time window into a comparable weekly record.
//
// A snapshot is the unit of memory this tool has and the builtin lacks. Each
// run writes one; each run also reads the previous one, which is what makes
// "did last week's correction stick?" answerable.
//
// The JSON shape is deliberately stable. Snapshots written by earlier versions
// must stay readable, because a trend line is worthless if it resets whenever
// the tool changes.
package snapshot

// Snapshot is one window's aggregated record.
type Snapshot struct {
	Window              Window             `json:"window"`
	GeneratedAt         string             `json:"generated_at"`
	Sessions            Sessions           `json:"sessions"`
	Volume              Volume             `json:"volume"`
	Tools               map[string]int     `json:"tools"`
	Projects            map[string]int     `json:"projects"`
	Goals               map[string]int     `json:"goals"`
	Friction            map[string]int     `json:"friction"`
	Satisfaction        map[string]int     `json:"satisfaction"`
	Outcomes            map[string]int     `json:"outcomes"`
	Helpfulness         map[string]int     `json:"helpfulness"`
	SessionTypes        map[string]int     `json:"session_types"`
	Success             map[string]int     `json:"success"`
	ToolErrorCategories map[string]int     `json:"tool_error_categories"`
	FrictionDetails     []FrictionDetail   `json:"friction_details"`
	UserCorrections     []UserCorrection   `json:"user_corrections"`
	Rates               map[string]float64 `json:"rates"`
	// Failures is the classified tool-failure block, absent in snapshots
	// written before the failures command existed. It is a pointer so that
	// "this week had no failures" and "this week predates the counting" stay
	// distinguishable in a trend line.
	Failures *FailureSummary `json:"failures,omitempty"`
}

// FailureSummary is one window's failed tool calls, split by the fixed classes
// in internal/failures. Only the totals live here; the per-incident detail is
// reproducible from the transcripts and would bloat every snapshot.
type FailureSummary struct {
	Total      int            `json:"total"`
	ByClass    map[string]int `json:"by_class"`
	PerSession float64        `json:"per_session"`
	// SelfInflicted counts failures a local rule could have prevented, and
	// External the rest. They sum to Total.
	SelfInflicted int `json:"self_inflicted"`
	External      int `json:"external"`
}

// Window is the time range covered. Label is the end date, and together with
// Days it forms the snapshot's identity.
//
// Filenames follow that identity: a 7-day snapshot is "<label>.json" and any
// other window length is "<label>-<days>d.json", for example
// "2020-03-08-30d.json". The 7-day case keeps the bare form because snapshots
// written before windows were nameable are already on disk under it, and a
// trend line that silently drops its own history is worse than an awkward
// naming rule. Without the suffix a 7-day and a 30-day run ending on the same
// date would collide and the second would overwrite the first.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Days  int    `json:"days"`
	Label string `json:"label"`
}

// Sessions records how the population was filtered, so a reader can tell a
// quiet week from an over-aggressive exclusion rule.
type Sessions struct {
	InWindow        int `json:"in_window"`
	ExcludedScratch int `json:"excluded_scratch"`
	Substantive     int `json:"substantive"`
	WithFacets      int `json:"with_facets"`
	// FacetSource splits coverage by where the facets came from. A window
	// mixing canonical and builtin facets is not cleanly comparable to one
	// built entirely from either, and this is the field that reveals it.
	FacetSource map[string]int `json:"facet_source"`
}

// Volume holds the deterministic counters. Every field here is counted from
// transcripts rather than inferred, so these are exact.
type Volume struct {
	Messages              int     `json:"messages"`
	Hours                 float64 `json:"hours"`
	Commits               int     `json:"commits"`
	Pushes                int     `json:"pushes"`
	LinesAdded            int     `json:"lines_added"`
	LinesRemoved          int     `json:"lines_removed"`
	FilesModified         int     `json:"files_modified"`
	InputTokens           int     `json:"input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	Interruptions         int     `json:"interruptions"`
	ToolErrors            int     `json:"tool_errors"`
	DaysActive            int     `json:"days_active"`
	MedianResponseSeconds float64 `json:"median_response_seconds"`
}

// FrictionDetail is one session's free-text account of what went wrong.
type FrictionDetail struct {
	Session string `json:"session"`
	Date    string `json:"date"`
	Project string `json:"project"`
	Detail  string `json:"detail"`
}

// UserCorrection is a verbatim quote of the user correcting or challenging the
// assistant. These are the raw material for next week's follow-up check.
type UserCorrection struct {
	Session string `json:"session"`
	Date    string `json:"date"`
	Quote   string `json:"quote"`
}

// Rate keys. Rates are per-session so a busy week is not penalized against a
// quiet one, which is the comparison the builtin's absolute counts get wrong.
const (
	RateSessionsPerDay     = "sessions_per_active_day"
	RateFrictionPerSession = "friction_events_per_session"
	RateNegativePct        = "negative_satisfaction_pct"
	RateInterruptions      = "interruptions_per_session"
	RateToolErrors         = "tool_errors_per_session"
	RateAchievedPct        = "achieved_pct"
	RateFacetCoveragePct   = "facet_coverage_pct"
	RateUnverifiedClaim    = "unverified_claim_per_session"
	RateUnsanctionedAction = "unwanted_autonomous_action_per_session"
	RateIgnoredPreference  = "ignored_stated_preference_per_session"
)
