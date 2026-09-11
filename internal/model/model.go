// Package model holds the on-disk shapes this tool reads and writes.
//
// SessionMeta and Facets mirror the caches Claude Code's builtin /insights
// maintains under ~/.claude/usage-data. We read those caches rather than
// re-deriving them, so a run costs nothing for sessions already analyzed.
// Facets additionally carries UserCorrections, which the builtin does not
// record and which is what makes week-over-week follow-up possible.
package model

// SessionMeta is the builtin's deterministic per-session record. Every field
// here is counted from the transcript, not inferred, so it is exact.
type SessionMeta struct {
	SessionID           string         `json:"session_id"`
	ProjectPath         string         `json:"project_path"`
	StartTime           string         `json:"start_time"`
	DurationMinutes     float64        `json:"duration_minutes"`
	UserMessageCount    int            `json:"user_message_count"`
	AssistantMsgCount   int            `json:"assistant_message_count"`
	ToolCounts          map[string]int `json:"tool_counts"`
	Languages           map[string]int `json:"languages"`
	GitCommits          int            `json:"git_commits"`
	GitPushes           int            `json:"git_pushes"`
	InputTokens         int            `json:"input_tokens"`
	OutputTokens        int            `json:"output_tokens"`
	FirstPrompt         string         `json:"first_prompt"`
	UserInterruptions   int            `json:"user_interruptions"`
	UserResponseTimes   []float64      `json:"user_response_times"`
	ToolErrors          int            `json:"tool_errors"`
	ToolErrorCategories map[string]int `json:"tool_error_categories"`
	UsesTaskAgent       bool           `json:"uses_task_agent"`
	UsesMCP             bool           `json:"uses_mcp"`
	LinesAdded          int            `json:"lines_added"`
	LinesRemoved        int            `json:"lines_removed"`
	FilesModified       int            `json:"files_modified"`
}

// Facets is the LLM's judgment about one session. Unlike SessionMeta these are
// interpretations, so they are reported as trends rather than truth.
type Facets struct {
	SessionID       string         `json:"session_id"`
	UnderlyingGoal  string         `json:"underlying_goal"`
	GoalCategories  map[string]int `json:"goal_categories"`
	Outcome         string         `json:"outcome"`
	Satisfaction    map[string]int `json:"user_satisfaction_counts"`
	Helpfulness     string         `json:"claude_helpfulness"`
	SessionType     string         `json:"session_type"`
	FrictionCounts  map[string]int `json:"friction_counts"`
	FrictionDetail  string         `json:"friction_detail"`
	UserCorrections []string       `json:"user_corrections,omitempty"`
	PrimarySuccess  string         `json:"primary_success"`
	BriefSummary    string         `json:"brief_summary"`
}

// Session pairs a meta record with its facets, if any were found.
type Session struct {
	Meta   SessionMeta
	Facets *Facets
	// FacetSource is "canonical" when the facets came from our own extraction
	// and "builtin" when they came from the builtin cache and were normalized
	// on read. Mixing the two across a week boundary makes counts incomparable,
	// so snapshots record the split.
	FacetSource string
}
