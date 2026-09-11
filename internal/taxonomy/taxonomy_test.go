package taxonomy

import "testing"

// The expectations below were captured from a real corpus of ~180 cached
// facet files, so they pin the collapses that actually occur in the wild
// rather than hypothetical ones.
func TestNormGoal(t *testing.T) {
	cases := map[string]string{
		// the same intent, five ways
		"bug_fixing":             GoalFixBug,
		"bug_fix":                GoalFixBug,
		"bug_fix_implementation": GoalFixBug,
		"ui_bug_fix":             GoalFixBug,
		"test_and_ci_fixes":      GoalFixBug,

		"code_review_response_drafting": GoalCodeReview,
		"code_review_or_pr_workflow":    GoalCodeReview,
		"review_fix_application":        GoalCodeReview,

		// debugging must win over CI, or ci_debugging reads as infra work
		"ci_debugging":           GoalDebugInvestigate,
		"codebase_investigation": GoalDebugInvestigate,

		"pr_creation":         GoalCreatePRCommit,
		"version_control_ops": GoalCreatePRCommit,
		"git_operations":      GoalCreatePRCommit,

		"verification_of_claims": GoalVerifyWork,
		"scope_verification":     GoalVerifyWork,
		"testing_validation":     GoalVerifyWork,

		"performance_optimization":  GoalRefactorCode,
		"tooling_skill_improvement": GoalWriteScriptTool,
		"question_answering":        GoalUnderstandCode,
		"status_reporting":          GoalProjectManagement,

		// already canonical, must pass through untouched
		"warmup_minimal": GoalWarmup,
		"code_review":    GoalCodeReview,

		// nothing sensible to map onto
		"":       GoalOther,
		"zzzzzz": GoalOther,
	}
	for in, want := range cases {
		if got := NormGoal(in); got != want {
			t.Errorf("NormGoal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormFriction(t *testing.T) {
	cases := map[string]string{
		"incorrect_status_claim":            FrictionUnverifiedClaim,
		"unverified_claims":                 FrictionUnverifiedClaim,
		"hallucinated_information":          FrictionUnverifiedClaim,
		"scope_creep":                       FrictionUnsanctionedAction,
		"unintended_side_effect":            FrictionUnsanctionedAction,
		"ignored_instructions":              FrictionIgnoredPreference,
		"wrong_tone_or_style":               FrictionIgnoredPreference,
		"blocked_by_permissions_classifier": FrictionBlocked,
		"tool_auth_failure":                 FrictionBlocked,
		"environment_setup":                 FrictionExternalIssue,
		"api_error":                         FrictionExternalIssue,
		"slow_response":                     FrictionSlowVerbose,
		"incomplete_response":               FrictionIncompleteWork,

		// context must win over the "incorrect/stale" family
		"stale_context":                FrictionContextLoss,
		"stale_or_incorrect_analysis":  FrictionContextLoss,
		"incomplete_context_gathering": FrictionContextLoss,

		"wrong_approach": FrictionWrongApproach,
		"buggy_code":     FrictionBuggyCode,
		"zzzzzz":         FrictionOther,
	}
	for in, want := range cases {
		if got := NormFriction(in); got != want {
			t.Errorf("NormFriction(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every canonical label must be a fixed point, or repeated normalization
// would quietly drift counts between runs.
func TestCanonicalLabelsAreStable(t *testing.T) {
	for _, g := range Goals {
		if got := NormGoal(g); got != g {
			t.Errorf("goal %q is not stable: normalized to %q", g, got)
		}
	}
	for _, f := range Frictions {
		if got := NormFriction(f); got != f {
			t.Errorf("friction %q is not stable: normalized to %q", f, got)
		}
	}
	for _, s := range Satisfactions {
		if got := NormSatisfaction(s); got != s {
			t.Errorf("satisfaction %q is not stable: normalized to %q", s, got)
		}
	}
}

func TestNormSatisfaction(t *testing.T) {
	if got := NormSatisfaction("Frustrated"); got != "frustrated" {
		t.Errorf("case folding failed: %q", got)
	}
	if got := NormSatisfaction("ecstatic"); got != "neutral" {
		t.Errorf("unknown level should fall back to neutral, got %q", got)
	}
	if !IsNegative("dissatisfied") || !IsNegative("frustrated") {
		t.Error("dissatisfied and frustrated must count as negative")
	}
	if IsNegative("likely_satisfied") {
		t.Error("likely_satisfied must not count as negative")
	}
}
