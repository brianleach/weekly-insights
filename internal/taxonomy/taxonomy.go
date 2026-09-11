// Package taxonomy pins the label vocabulary and normalizes free-form labels
// into it.
//
// Why this exists: Claude Code's builtin /insights ships a canonical display
// vocabulary but never puts it in its facet-extraction prompt, so the model
// invents a fresh label per session. Across one real corpus, "fix a bug"
// appeared as bug_fixing, bug_fix, bug_fix_implementation, ui_bug_fix and
// test_and_ci_fixes. Counts built on labels like that cannot be trended.
//
// Two mechanisms keep weeks comparable:
//   - New extractions are prompted with the fixed vocabulary below.
//   - Anything already cached in a free-form vocabulary is normalized on read.
//
// Normalization is token-based: a label is lowercased, split on "_", and
// matched against ordered rule sets. First match wins, so rule order encodes
// precedence. Run `weekly-insights aggregate --explain` to audit every collapse.
package taxonomy

import "strings"

// Goal categories. The first thirteen are the builtin's own vocabulary. The
// rest are additions for work the builtin has no label for and therefore
// reports as free-form noise.
const (
	GoalWarmup            = "warmup_minimal"
	GoalCodeReview        = "code_review"
	GoalVerifyWork        = "verify_work"
	GoalDebugInvestigate  = "debug_investigate"
	GoalFixBug            = "fix_bug"
	GoalWriteTests        = "write_tests"
	GoalCreatePRCommit    = "create_pr_commit"
	GoalDeployInfra       = "deploy_infra"
	GoalProjectManagement = "project_management"
	GoalCommunication     = "communication_drafting"
	GoalWriteDocs         = "write_docs"
	GoalAnalyzeData       = "analyze_data"
	GoalRefactorCode      = "refactor_code"
	GoalWriteScriptTool   = "write_script_tool"
	GoalConfigureSystem   = "configure_system"
	GoalImplementFeature  = "implement_feature"
	GoalUnderstandCode    = "understand_codebase"
	GoalOther             = "other"
)

// Friction types. The three leading entries are additions: generic extractors
// reliably miss them, and they are the failure modes worth tracking over time.
const (
	FrictionUnverifiedClaim    = "unverified_claim"
	FrictionUnsanctionedAction = "unwanted_autonomous_action"
	FrictionIgnoredPreference  = "ignored_stated_preference"
	FrictionMisunderstood      = "misunderstood_request"
	FrictionWrongApproach      = "wrong_approach"
	FrictionBuggyCode          = "buggy_code"
	FrictionWrongLocation      = "wrong_file_or_location"
	FrictionExcessiveChanges   = "excessive_changes"
	FrictionIncompleteWork     = "incomplete_work"
	FrictionUserRejected       = "user_rejected_action"
	FrictionUserStoppedEarly   = "user_stopped_early"
	FrictionBlocked            = "claude_got_blocked"
	FrictionToolFailed         = "tool_failed"
	FrictionExternalIssue      = "external_issue"
	FrictionContextLoss        = "context_loss"
	FrictionSlowVerbose        = "slow_or_verbose"
	FrictionUnclearExplanation = "unclear_explanation"
	FrictionUserUnclear        = "user_unclear"
	FrictionOther              = "other"
)

type rule struct {
	canonical string
	tokens    map[string]bool
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// goalRules is ordered: earlier rules win. debug_investigate precedes
// deploy_infra so "ci_debugging" reads as debugging rather than CI work.
var goalRules = []rule{
	{GoalWarmup, set("warmup")},
	{GoalCodeReview, set("review", "reviews", "reviewing")},
	{GoalVerifyWork, set("verification", "verify", "verifying", "validation", "audit", "parity")},
	{GoalDebugInvestigate, set("debug", "debugging", "investigate", "investigation",
		"investigating", "diagnosis", "diagnose", "triage", "root", "troubleshooting", "rca")},
	{GoalFixBug, set("fix", "fixes", "bug", "bugfix", "hotfix", "remediation",
		"conflict", "conflicts", "resolution")},
	{GoalWriteTests, set("test", "tests", "testing", "spec", "specs", "rspec", "coverage")},
	{GoalCreatePRCommit, set("pr", "prs", "commit", "push", "merge", "git", "branch",
		"version", "repo", "repository", "pull", "github")},
	{GoalDeployInfra, set("deploy", "deployment", "deployments", "release", "infra",
		"infrastructure", "devops", "ci", "cd", "preview", "seeding", "backfill",
		"migrations", "production", "prod")},
	{GoalProjectManagement, set("ticket", "tickets", "ticketing", "issue", "linear",
		"project", "planning", "plan", "roadmap", "backlog", "tracking", "status",
		"task", "tasks", "handoff", "delegation", "orchestration")},
	{GoalCommunication, set("communication", "drafting", "draft", "slack", "message",
		"email", "tone", "writing", "copy", "reporting", "summary")},
	{GoalWriteDocs, set("documentation", "docs", "doc", "document", "adr", "readme",
		"prd", "authoring")},
	{GoalAnalyzeData, set("data", "analytics", "metrics", "benchmark", "benchmarking",
		"evaluation", "eval", "comparison", "analysis")},
	{GoalRefactorCode, set("refactor", "refactoring", "cleanup", "simplify",
		"consolidation", "port", "migration", "optimization", "optimize", "performance")},
	{GoalWriteScriptTool, set("script", "scripting", "tool", "tooling", "cli", "skill",
		"agent", "agents", "automation")},
	{GoalConfigureSystem, set("config", "configuration", "setup", "settings", "workflow",
		"credential", "permission", "flag", "gate", "scheduling", "install",
		"environment", "env")},
	{GoalImplementFeature, set("feature", "implementation", "implement", "build",
		"scaffolding", "greenfield", "generation", "ui", "styling", "change", "changes")},
	{GoalUnderstandCode, set("explanation", "explain", "understanding", "comprehension",
		"exploration", "explore", "search", "discovery", "question", "lookup", "recall",
		"codebase", "navigation", "context", "research", "decision", "support")},
}

// frictionRules is ordered: context_loss precedes unverified_claim so
// "stale_context" reads as lost context rather than a false assertion.
var frictionRules = []rule{
	{FrictionContextLoss, set("context", "stale")},
	{FrictionUnverifiedClaim, set("unverified", "hallucinated", "incorrect", "claim",
		"claims", "assumption", "misleading", "information")},
	{FrictionUnsanctionedAction, set("unwanted", "autonomous", "scope", "creep",
		"unintended", "side", "proactive", "unnecessary")},
	{FrictionIgnoredPreference, set("ignored", "preference", "instructions", "tone",
		"style", "formatting")},
	{FrictionWrongLocation, set("file", "location", "path")},
	{FrictionBlocked, set("blocked", "permission", "permissions", "classifier",
		"denied", "auth", "authentication", "refusal", "unavailable")},
	{FrictionUserRejected, set("rejected", "reject", "rejection")},
	{FrictionUserStoppedEarly, set("stopped", "interrupted", "interruption", "abort")},
	{FrictionExcessiveChanges, set("excessive", "overengineered", "bloat")},
	{FrictionBuggyCode, set("buggy", "broken", "bug", "regression", "failing")},
	{FrictionMisunderstood, set("misunderstood", "misread", "missed", "intent",
		"misinterpreted")},
	{FrictionUnclearExplanation, set("unclear", "confusing")},
	{FrictionSlowVerbose, set("slow", "verbose", "verbosity", "noise")},
	{FrictionIncompleteWork, set("incomplete", "partial", "unfinished")},
	{FrictionExternalIssue, set("external", "merge", "conflicts", "network", "drops", "api")},
	{FrictionToolFailed, set("tool", "tooling", "error", "errors", "failure", "failed")},
	{FrictionExternalIssue, set("environment", "env", "setup")},
	{FrictionWrongApproach, set("wrong", "approach")},
}

// Enum vocabularies. Order is meaningful: report tables render in this order.
var (
	Goals     = []string{GoalCodeReview, GoalVerifyWork, GoalDebugInvestigate, GoalFixBug, GoalWriteTests, GoalCreatePRCommit, GoalDeployInfra, GoalProjectManagement, GoalCommunication, GoalWriteDocs, GoalAnalyzeData, GoalRefactorCode, GoalWriteScriptTool, GoalConfigureSystem, GoalImplementFeature, GoalUnderstandCode, GoalWarmup, GoalOther}
	Frictions = []string{FrictionUnverifiedClaim, FrictionUnsanctionedAction, FrictionIgnoredPreference, FrictionMisunderstood, FrictionWrongApproach, FrictionBuggyCode, FrictionWrongLocation, FrictionExcessiveChanges, FrictionIncompleteWork, FrictionUserRejected, FrictionUserStoppedEarly, FrictionBlocked, FrictionToolFailed, FrictionExternalIssue, FrictionContextLoss, FrictionSlowVerbose, FrictionUnclearExplanation, FrictionUserUnclear, FrictionOther}

	Satisfactions = []string{"delighted", "happy", "satisfied", "likely_satisfied", "neutral", "unsure", "dissatisfied", "frustrated"}
	Outcomes      = []string{"fully_achieved", "mostly_achieved", "partially_achieved", "not_achieved", "unclear_from_transcript"}
	Helpfulness   = []string{"essential", "very_helpful", "moderately_helpful", "slightly_helpful", "unhelpful"}
	SessionTypes  = []string{"single_task", "multi_task", "iterative_refinement", "exploration", "quick_question"}
	Successes     = []string{"none", "fast_accurate_search", "correct_code_edits", "good_explanations", "proactive_help", "multi_file_changes", "good_debugging"}

	// Negative satisfaction drives the headline dissatisfaction rate. The
	// positive levels are deliberately excluded: "continuing without complaint"
	// counts as likely_satisfied, so the positive share is not informative.
	Negative = []string{"dissatisfied", "frustrated"}
)

func tokens(label string) map[string]bool {
	l := strings.ToLower(label)
	l = strings.ReplaceAll(l, "-", "_")
	l = strings.ReplaceAll(l, " ", "_")
	return set(strings.Split(l, "_")...)
}

func apply(rules []rule, label, fallback string) string {
	tk := tokens(label)
	for _, r := range rules {
		for t := range r.tokens {
			if tk[t] {
				return r.canonical
			}
		}
	}
	return fallback
}

func in(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// NormGoal maps any goal label onto the canonical set.
func NormGoal(label string) string {
	if in(Goals, label) {
		return label
	}
	return apply(goalRules, label, GoalOther)
}

// NormFriction maps any friction label onto the canonical set.
func NormFriction(label string) string {
	if in(Frictions, label) {
		return label
	}
	return apply(frictionRules, label, FrictionOther)
}

// NormSatisfaction maps a satisfaction level onto the canonical set. Unknown
// levels become neutral rather than being dropped, so totals stay honest.
func NormSatisfaction(label string) string {
	l := strings.ToLower(label)
	if in(Satisfactions, l) {
		return l
	}
	switch l {
	case "thrilled":
		return "delighted"
	case "pleased":
		return "happy"
	}
	return "neutral"
}

// NormEnum clamps a value to an allowed set, falling back when it is unknown.
func NormEnum(label string, allowed []string, fallback string) string {
	l := strings.ToLower(label)
	if in(allowed, l) {
		return l
	}
	return fallback
}

// IsNegative reports whether a satisfaction level counts against the week.
func IsNegative(level string) bool { return in(Negative, level) }
