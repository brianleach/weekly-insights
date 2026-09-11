# weekly-insights

The real Claude Code `/insights` report, scoped to a time range.

```
weekly-insights insights --days 7 --open
```

Claude Code's builtin `/insights` is good, but it is cumulative over all history
and takes no arguments. If you read it every week looking for things to fix, a week
of changed behavior gets averaged against months of the old pattern and nothing
ever visibly moves. This tool runs the builtin over just the sessions inside a
window and hands you the report it writes. Same sections, same format, one week.

## How it works

`/insights` reads everything under the Claude config directory and honors
`CLAUDE_CONFIG_DIR`. So the tool builds a temporary config directory that contains
only the window:

- copies of the transcripts and per-session caches for the sessions in range
  (the builtin skips symlinked transcripts when it lists the directory, so these
  have to be real files)
- symlinks to your real settings, plugins, commands and hooks, so the child behaves
  like your normal install
- a copy of the account file, so the child is logged in as you

then runs `claude -p /insights` against it, copies the report out, and deletes the
stage. Nothing in the builtin is reimplemented; the report is whatever your installed
Claude Code produces. Everything the stage copies is written with mode 0600 inside
directories created 0700, and the whole stage is removed when the run ends.

Two useful side effects. The child computes session metadata for every transcript
it sees, and the tool harvests that back into your real cache, so sessions the
cumulative builtin never got to (it caps new analysis per run) stop being invisible.
And if you have canonical facets for the window (see below), they are seeded into
the stage, so the builtin's charts use a stable vocabulary and the run skips
extraction entirely.

## Install

Go 1.26 or newer is required (see `go.mod`).

```
go install github.com/brianleach/weekly-insights/cmd/weekly-insights@latest
```

Or `make build` for a local binary and `make dist` for cross-compiled ones. Standard
library only, no cgo.

## Authentication

A staged config directory cannot see the login stored for your real one, so the
child needs a long-lived token minted on your subscription (not API billing).
One-time setup, two commands:

```
claude setup-token        # interactive; prints a token
weekly-insights auth      # paste it; stored in the right place for your platform
```

Where it goes:

| Platform | Storage |
|---|---|
| macOS | keychain item `weekly-insights` |
| Linux | `$XDG_CONFIG_HOME/weekly-insights/token` (default `~/.config/...`), mode 0600 |
| Windows | `%AppData%\weekly-insights\token` |

`weekly-insights auth --check` says whether a token is available and where, without
printing it. `--clear` removes it. Setting `CLAUDE_CODE_OAUTH_TOKEN` in the
environment overrides storage for a single run.

The token is stored outside any repository on purpose. A `.env` in a checkout is one
careless `git add` from being committed, and the binary is run from arbitrary
directories anyway, so it would not be found. The repository's `.gitignore` excludes
`.env` and `token` files regardless, as a backstop.

On Linux you may not need a token at all: Claude Code there keeps credentials in a
file inside the config directory, and the stage carries that file along. This is
untested; if the run reports "not logged in", use the token.

The stage, which holds a copy of your account file, is removed when the run ends.

## Platform support

macOS is tested. Linux should work and has not been exercised. Windows is not
supported: the stage symlinks your shared config entries, and creating symlinks on
Windows needs Developer Mode or elevation. `make dist` builds darwin and linux only.

## Usage

```
weekly-insights insights --days 7                  # this week's report
weekly-insights insights --days 7 --open           # and open it
weekly-insights insights --days 7 --end 2026-09-04 # a past week
weekly-insights insights --days 30                 # a month
```

Reports land in `~/claude-weekly-insights/insights-<days>d-<end>.html` by default;
`--out DIR` changes that. `--keep-stage` leaves the staged directory in place for
inspection. `--include-scratch` disables the project exclusions described below.

## Trends across weeks

The builtin has no memory between runs, so it cannot tell you whether last week's
correction stuck. A second set of commands covers that:

```
weekly-insights select    --days 7        # what is in the window, facet coverage
weekly-insights prepare   --days 7 --out DIR
weekly-insights aggregate --days 7        # save a snapshot
weekly-insights progress  --weeks 4       # judge progression across recent reports
weekly-insights report                    # newest snapshot vs the previous one
weekly-insights report --trend            # every snapshot, one bar per week
weekly-insights report --html --out DIR   # the same as browsable pages
weekly-insights validate --fix            # enforce the vocabulary on extracted facets
weekly-insights prompt                    # print the extraction prompt
```

This path exists because of a defect in how the builtin labels sessions. It ships a
canonical vocabulary internally but never puts it in its extraction prompt, so the
model invents a label per session: on one real corpus "fix a bug" appeared as
`bug_fixing`, `bug_fix`, `bug_fix_implementation`, `ui_bug_fix` and
`test_and_ci_fixes`. You cannot chart that. `prompt` pins the vocabulary, `validate`
enforces it, and `aggregate` normalizes anything already cached in a free-form one.

`progress` is the one command here that calls the model; see the network section
below for exactly what it sends.

The extraction step itself is left to you: `prepare` renders transcripts to text
(0600 files in a 0700 directory; treat it as sensitive and delete it when you are
done) and `prompt` prints the instructions. Running it through Claude Code is the easy route,
and `skill/` holds a ready-made skill that fans it out to subagents. Snapshots record
per-session rates (so a busy week is not penalised), the three friction types that
generic extractors miss (`unverified_claim`, `unwanted_autonomous_action`,
`ignored_stated_preference`), and every correction the user typed, verbatim.

## What it reads and writes

| Path | Owner | Used for |
|---|---|---|
| `~/.claude/projects/*/*.jsonl` | Claude Code | transcripts, copied into the stage |
| `~/.claude/usage-data/session-meta/` | Claude Code | per-session counters; harvested back, never overwritten |
| `~/.claude/usage-data/facets/` | Claude Code | cached judgments; harvested back, never overwritten |
| `~/.claude.json` | Claude Code | account file, copied into the stage |
| `~/.claude/CLAUDE.md` | you | your global instructions; read by `progress` and **sent to the model** |
| `~/.claude/usage-data/weekly-facets/` | this tool | canonical-vocabulary facets |
| `~/.claude/usage-data/weekly/` | this tool | snapshots |
| `~/claude-weekly-insights/` | this tool | weekly reports and the `progress` memo |

Sensitive outputs are written with restrictive permissions: staged transcripts,
prepared transcripts, reports, snapshots and the progress memo are files of mode
0600 inside directories created 0700.

### Network and what leaves your machine

Two commands make network calls, both by running your installed Claude Code
(`claude -p`) on your own subscription. Every other command is local only.

**`insights`** runs the builtin `/insights` against the staged config directory.
The child sends whatever the builtin sends for a normal `/insights` run: the
session transcripts in the window (your prompts, assistant replies, file paths and
project names) plus their cached metadata.

**`progress`** sends, on stdin, in one prompt:

- the extracted text of the last N weekly reports: at-a-glance summaries, project
  areas, wins, friction categories and their verbatim examples, suggested
  `CLAUDE.md` additions, suggested features, and horizon items
- **your current global `~/.claude/CLAUDE.md`, in full, truncated only at 24000
  characters.** If that file holds anything you do not want sent to the model, do
  not run `progress`.
- the numeric trend table, when snapshots exist

Nothing else in the tool makes a network request, and the tool itself uploads
nothing anywhere.

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

Sessions whose project path matches are dropped from the window. Agent scratchpads
and eval-harness runs are not work you did, and they can outnumber real sessions
badly: in one real week they were 163 of 200. The builtin counts them.

## Reading the trend output

- **Rates, not counts.** Headline metrics are per session.
- **Mind the facet source.** Snapshots built from normalized builtin facets are not
  cleanly comparable to ones built from canonical extraction; `sessions.facet_source`
  records which, and the trend page warns at the boundary.
- **Small samples.** At roughly 30 sessions a week, a shift of one or two events is
  not a trend.
- **Hours are weak.** Duration is wall clock between first and last message. Session
  and message counts are the reliable volume signals. The builtin has the same
  distortion.
- **Facets are judgments.** Only the counters from `session-meta` are exact.

## License

MIT. See [LICENSE](LICENSE).
