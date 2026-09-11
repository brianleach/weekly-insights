# Facet extraction prompt

Analyze one Claude Code session transcript and extract structured facets.

## Critical guidelines

1. **goal_categories** — count ONLY what the USER explicitly asked for.
   - Do NOT count Claude's autonomous codebase exploration.
   - Do NOT count work Claude decided to do on its own.
   - Only count when the user says "can you...", "please...", "I need...", "let's...".

2. **user_satisfaction_counts** — base ONLY on explicit user signals.
   - "yay!", "great!", "perfect!" → `happy`
   - "thanks", "looks good", "that works" → `satisfied`
   - "ok, now let's..." (continuing without complaint) → `likely_satisfied`
   - "that's not right", "try again", "do better" → `dissatisfied`
   - "this is broken", "wtf are you doing", "I give up" → `frustrated`

3. **friction_counts** — be specific about what went wrong, and pay particular
   attention to these three, which generic extractors routinely miss:
   - `unverified_claim` — Claude asserted something was done, deployed, verified,
     or in a given state without proof, and the user challenged it
     ("did you actually do this?"), or it was later found wrong.
   - `unwanted_autonomous_action` — Claude took a consequential action nobody
     asked for: opened a PR, launched a browser, pushed, edited out-of-scope files.
   - `ignored_stated_preference` — Claude drifted back to a habit the user had
     already corrected (formatting, verbosity, must-fix-only, link style).

4. If the session is very short or just a cache warmup, use `warmup_minimal`
   as the only goal category.

## Fixed vocabularies — USE THESE EXACT STRINGS, never invent new labels

This is the whole point of the exercise: labels must be identical across weeks
so counts can be trended. If nothing fits, use `other`.

**goal_categories** (keys; values are counts):
`code_review`, `verify_work`, `debug_investigate`, `fix_bug`, `write_tests`,
`create_pr_commit`, `deploy_infra`, `project_management`,
`communication_drafting`, `write_docs`, `analyze_data`, `refactor_code`,
`write_script_tool`, `configure_system`, `implement_feature`,
`understand_codebase`, `warmup_minimal`, `other`

**friction_counts** (keys; values are counts):
`unverified_claim`, `unwanted_autonomous_action`, `ignored_stated_preference`,
`misunderstood_request`, `wrong_approach`, `buggy_code`,
`wrong_file_or_location`, `excessive_changes`, `incomplete_work`,
`user_rejected_action`, `user_stopped_early`, `claude_got_blocked`,
`tool_failed`, `external_issue`, `context_loss`, `slow_or_verbose`,
`unclear_explanation`, `user_unclear`, `other`

**user_satisfaction_counts** (keys; values are counts):
`delighted`, `happy`, `satisfied`, `likely_satisfied`, `neutral`, `unsure`,
`dissatisfied`, `frustrated`

## Output

Respond with ONLY a valid JSON object, no prose and no code fence:

```
{
  "session_id": "<the id from the transcript header>",
  "underlying_goal": "What the user fundamentally wanted to achieve",
  "goal_categories": {"category": count},
  "outcome": "fully_achieved|mostly_achieved|partially_achieved|not_achieved|unclear_from_transcript",
  "user_satisfaction_counts": {"level": count},
  "claude_helpfulness": "unhelpful|slightly_helpful|moderately_helpful|very_helpful|essential",
  "session_type": "single_task|multi_task|iterative_refinement|exploration|quick_question",
  "friction_counts": {"friction_type": count},
  "friction_detail": "One sentence describing the friction, quoting the user where possible, or empty",
  "user_corrections": ["short verbatim quotes of the user correcting, rejecting, or challenging Claude"],
  "primary_success": "none|fast_accurate_search|correct_code_edits|good_explanations|proactive_help|multi_file_changes|good_debugging",
  "brief_summary": "One sentence: what the user wanted and whether they got it"
}
```

`user_corrections` is an addition to the builtin schema. Keep each quote under
120 characters and include only things the user actually typed. It is what makes
the week-over-week "did this correction stop recurring?" check possible.
