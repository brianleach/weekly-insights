# weekly-insights

Time-windowed usage insights for [Claude Code](https://claude.com/claude-code), with week-over-week trends.

Claude Code ships a builtin `/insights` command that analyzes your sessions. It is
useful, but it is cumulative over all history and takes no arguments, so a week of
changed behavior gets averaged against months of the old pattern. If you are trying
to actually improve how you work, that is the one thing you need to see.

This tool scopes to a window, pins a fixed label vocabulary so counts are comparable
across weeks, and diffs each run against the previous one.

## What it does differently

| | builtin `/insights` | weekly-insights |
|---|---|---|
| Time range | all history, not configurable | `--days N`, any window |
| Labels | free-form, model invents per session | fixed vocabulary, enforced |
| Memory | none, each run stands alone | snapshots on disk, diffed each run |
| Scratch sessions | counted as your work | filtered by config |
| Comparison | absolute counts | per-session rates |

The label problem is worth spelling out, because it is what makes trends possible.
The builtin ships a canonical vocabulary internally but never puts it in its
extraction prompt, so the model invents a label per session. On one real corpus,
"fix a bug" appeared as `bug_fixing`, `bug_fix`, `bug_fix_implementation`,
`ui_bug_fix`, and `test_and_ci_fixes`. You cannot chart that. This tool pins the
vocabulary in the prompt and normalizes anything already cached in a free-form one.

## Install

```
go install github.com/brianleach/weekly-insights/cmd/weekly-insights@latest
```

Or build from source:

```
make build      # ./weekly-insights
make dist       # cross-compiled binaries in dist/
```

No dependencies beyond the Go standard library, and no cgo, so a single static
binary works on every supported platform.

## Usage

```
weekly-insights select    --days 7        # what is in the window, and facet coverage
weekly-insights prepare   --days 7 --out DIR
weekly-insights aggregate --days 7        # build and save a snapshot
weekly-insights report                    # newest snapshot vs the previous one
weekly-insights report --trend            # every snapshot as one table
weekly-insights validate --fix            # enforce the vocabulary on extracted facets
weekly-insights prompt                    # print the extraction prompt
```

A full pass looks like this:

```
weekly-insights select --days 7                        # see what needs extraction
weekly-insights prepare --days 7 --out /tmp/work       # render transcripts to text
#   ... run the extraction prompt over /tmp/work/*.txt with a model of your choice,
#   ... writing one JSON object per session to ~/.claude/usage-data/weekly-facets/
weekly-insights validate --fix
weekly-insights aggregate --days 7
weekly-insights report
```

The extraction step is deliberately left to you. This tool does not call any model
API, hold any credential, or make any network request. `prepare` renders transcripts
to plain text and `prompt` prints the instructions; how you run the model over them
is your choice. Doing it through Claude Code itself is the path of least resistance,
and a ready-made skill for that lives in `skill/`.

Earlier windows can be reconstructed from cached data at any time:

```
weekly-insights aggregate --days 7 --end 2026-09-04
weekly-insights report --current 2026-09-04
```

## What it reads

Everything is local. Nothing is uploaded, and the tool makes no network calls at all.

| Path | Owner | Used for |
|---|---|---|
| `~/.claude/usage-data/session-meta/` | Claude Code | exact per-session counters |
| `~/.claude/usage-data/facets/` | Claude Code | cached model judgments, normalized on read |
| `~/.claude/projects/*/*.jsonl` | Claude Code | raw transcripts, read by `prepare` |
| `~/.claude/usage-data/weekly-facets/` | this tool | canonical-vocabulary facets |
| `~/.claude/usage-data/weekly/` | this tool | snapshots |

It never writes to the directories Claude Code owns, so the builtin `/insights`
keeps working exactly as before.

## Configuration

`--config` points at a JSON file:

```json
{
  "exclude_project_globs": [
    "/private/tmp/*",
    "/tmp/*",
    "*/scratchpad/*",
    "*/.claude/worktrees/*"
  ]
}
```

Sessions whose project path matches are dropped before aggregation. Agent
scratchpads and eval-harness runs are not work you did, and they can badly
outnumber real sessions: in one real week they were 163 of 200.

## Reading the output

Some care is needed to avoid reading noise as signal.

- **Rates, not counts.** Every headline metric is per session, so a busy week is not
  automatically a worse week.
- **Mind the facet source.** A window built from normalized builtin facets is not
  cleanly comparable to one built from canonical extraction, because the two prompts
  detect different things. Snapshots record the split in `sessions.facet_source`, and
  the trend table warns when you cross that boundary.
- **Small samples.** At roughly 30 sessions a week, a shift of one or two events is
  not a trend.
- **Hours are weak.** Session duration is wall clock between the first and last
  message, so a session left open overnight inflates it. Session and message counts
  are the reliable volume signals. The builtin has the same distortion.
- **Satisfaction skews positive.** Continuing without complaint counts as
  `likely_satisfied`, so watch the dissatisfied and frustrated rate rather than the
  positive share.
- **Facets are judgments.** The deterministic fields (tool counts, commits, lines,
  interruptions, tool errors) come from Claude Code's own cache and are exact.
  Everything derived from facets is a model's reading of a transcript.

## The vocabulary

Goal categories and friction types are fixed. `weekly-insights prompt` prints the
full list. Three friction types are additions that generic extractors reliably miss:

- `unverified_claim` — asserting something was done, deployed, or verified without
  proof, and being challenged on it.
- `unwanted_autonomous_action` — taking a consequential action nobody asked for.
- `ignored_stated_preference` — drifting back to a habit you already corrected.

Facets also record `user_corrections`, verbatim. That is what makes the most useful
question answerable: did last week's correction actually stick?

`weekly-insights aggregate --explain` prints how every free-form label collapsed, so
the normalizer stays auditable.

## License

MIT. See [LICENSE](LICENSE).
