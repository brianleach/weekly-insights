---
name: weekly-insights
description: Generate a weekly Claude Code usage report scoped to a time window, with week-over-week deltas on friction, unverified claims, unsanctioned actions, and ignored preferences. Use when the user says "weekly insights", "how did this week go", "run my weekly review", "did last week's corrections stick", or wants a time-scoped alternative to the builtin /insights, which is cumulative and cannot show trends.
---

# Weekly insights

Drives the `weekly-insights` binary. The binary does all counting, normalizing,
and rendering; this skill supplies the one thing it deliberately does not do,
which is run a model over the transcripts to extract facets.

Install the binary first:

```bash
go install github.com/brianleach/weekly-insights/cmd/weekly-insights@latest
weekly-insights --help
```

## Procedure

### 1. Check coverage

```bash
weekly-insights select --days 7
```

Reports sessions in the window, how many were excluded as scratch, how many are
substantive, and how many still need canonical facets.

Sessions counted as needing extraction include ones that already have builtin
facets, because those use free-form labels and cannot be trended.

### 2. Extract facets

```bash
WORK=$(mktemp -d)
weekly-insights prepare --days 7 --out "$WORK"
ls "$WORK"
```

Fan out subagents over the prepared transcripts. Give each 4 to 6 sessions, and
launch them in a single message so they run concurrently. Mandate:

> Run `weekly-insights prompt` and follow it exactly. For each transcript file
> listed below, read it and write the resulting JSON object to
> `~/.claude/usage-data/weekly-facets/<session_id>.json`. Use only the
> vocabularies in that prompt, never invent a label. One file in, one file out.
> Do not modify anything else. Report only the count written.

Delegated volume work like this should run on the cheaper coding model, not the
session's model.

### 3. Validate

```bash
weekly-insights validate --fix
```

Catches invented labels, bad enums, malformed JSON. Re-extract anything that
comes back unparseable.

### 4. Snapshot and report

```bash
weekly-insights aggregate --days 7
weekly-insights report
weekly-insights report --trend
```

`aggregate --explain` prints how every free-form label collapsed, so the
normalizer stays auditable.

### 5. Write the analysis

The binary produces numbers; you write the judgment. Ground every claim in the
snapshot and keep it short. Lead with the answer.

Cover, in this order:

1. **Did last week's corrections stick?** Read `user_corrections` from the prior
   snapshot, then check whether the same friction type recurred this week. This
   is the single most useful output. Name the correction and say plainly whether
   it recurred.
2. **What moved.** Only changes larger than normal week-to-week noise. At roughly
   30 sessions a week, a shift of one or two events is not a trend: say so rather
   than narrating it. If the two windows differ in `sessions.facet_source`, say
   up front that the comparison is measurement, not behavior.
3. **One thing to change.** A single concrete adjustment, ideally one line in
   CLAUDE.md, not a list of five.

Do not restate tables the report already printed. Do not pad with observations
the user did not ask for.

## Backfilling earlier weeks

Snapshots are built from cached data, so prior weeks can be reconstructed:

```bash
weekly-insights aggregate --days 7 --end 2026-09-04
weekly-insights report --current 2026-09-04
```

Coverage for older windows is whatever the builtin happened to cache, normalized.
Run step 2 with `--end` to raise it.

## Caveats

See the "Reading the output" section of the project README. The short version:
rates not counts, mind the facet source when comparing, hours are wall clock and
weak, satisfaction skews positive, and only the deterministic counters are exact.
