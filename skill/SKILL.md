---
name: weekly-insights
description: Run the real Claude Code /insights report over a time window (last N days) instead of all history, and optionally track week-over-week trends. Use when the user says "weekly insights", "insights for the last week", "run insights for the last N days", "how did this week go", or "did last week's corrections stick".
---

# Weekly insights

Drives the `weekly-insights` binary. The main job is one command:

```bash
weekly-insights --days 7 --open
```

That stages a config directory holding only the window's sessions, runs the user's
own `claude -p /insights` against it, and copies the resulting report to
`~/claude-weekly-insights/insights-7d-<end>.html`. It is the builtin report, same
sections and format, restricted to the window. Do not summarize it back to the user
unless asked: they read it themselves.

Install if missing:

```bash
go install github.com/brianleach/weekly-insights/cmd/weekly-insights@latest
```

## Running the report

```bash
weekly-insights --days 7 --open              # this week
weekly-insights --days 7 --end 2026-09-04    # a past week
weekly-insights --days 30                    # a month
```

The run takes a few minutes; it is a real `/insights` run, just over fewer sessions.
Print the path it returns.

If it exits with "not logged in", the one-time token setup has not been done.
`claude setup-token` is an interactive login and `weekly-insights auth` takes a
pasted secret, so ask the user to run both themselves in a terminal:

```bash
claude setup-token        # prints a token
weekly-insights auth      # paste it; stored per platform, outside any repo
weekly-insights auth --check
```

Never handle the token value yourself, and never write it into a repo checkout.

## Trends across weeks (optional)

The builtin cannot compare weeks. When the user wants that, build canonical facets
for the window and snapshot it. Facets also get seeded into the next `insights` run,
which makes the builtin's charts use a stable vocabulary.

### 1. Check coverage

```bash
weekly-insights select --days 7
```

### 2. Extract facets

```bash
WORK=$(mktemp -d)
weekly-insights prepare --days 7 --out "$WORK"
```

Fan out subagents over the prepared transcripts, 4 to 6 sessions each, launched in a
single message. Mandate:

> Run `weekly-insights prompt` and follow it exactly. For each transcript file
> listed below, read it and write the resulting JSON object to
> `~/.claude/usage-data/weekly-facets/<session_id>.json`. Use only the
> vocabularies in that prompt, never invent a label. One file in, one file out.
> Do not modify anything else. Report only the count written.

Delegated volume work like this runs on the cheaper coding model, not the
session's model.

### 3. Validate, snapshot, report

```bash
weekly-insights validate --fix
weekly-insights aggregate --days 7
weekly-insights report --trend
weekly-insights report --html --out ~/claude-weekly-insights
```

### 4. Judgment

Only when asked. Ground every claim in the snapshot, lead with the answer, and cover:
did last week's corrections recur; what moved beyond noise (at ~30 sessions a week,
one or two events is not a trend; if `sessions.facet_source` differs between the
two windows say the comparison is measurement, not behavior); one concrete change.
Do not restate what the report already shows.

## Caveats

Hours are wall clock and weak. Satisfaction skews positive. Only `session-meta`
counters are exact; everything from facets is a model's reading of a transcript.
