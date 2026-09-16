# weekly-insights

The real Claude Code `/insights` report, scoped to a time range.

```
weekly-insights --days 7 --open
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
directories created 0700, and the whole stage is removed when the run ends,
including when the run is interrupted with Ctrl-C. A stage older than a day, left
behind by a run that was killed outright, is removed on the next run.

Two useful side effects. The child computes session metadata for every transcript
it sees, and the tool harvests that back into your real cache, so sessions the
cumulative builtin never got to (it caps new analysis per run) stop being invisible.
And if you have canonical facets for the window (see below), they are seeded into
the stage, so the builtin's charts use a stable vocabulary and the run skips
extraction entirely.

## Install

You need [Claude Code](https://claude.com/claude-code) installed and logged in; the
tool runs your own `claude` binary and does nothing without it.

**With Go 1.26 or newer:**

```
go install github.com/brianleach/weekly-insights/cmd/weekly-insights@latest
```

That puts the binary in `$(go env GOPATH)/bin`, usually `~/go/bin`. If
`weekly-insights version` says "command not found", add that directory to your
`PATH` or symlink the binary somewhere already on it, for example
`ln -s ~/go/bin/weekly-insights ~/.local/bin/`.

**Without Go:** download the binary for your platform from the
[releases page](https://github.com/brianleach/weekly-insights/releases), verify it
against the `SHA256SUMS` file published with it, and put it on your `PATH`. Builds
are provided for macOS and Linux on arm64 and amd64.

**From a checkout:** `make build` produces `./weekly-insights`; `make install` puts
it in `$(go env GOPATH)/bin`; `make dist` cross-compiles into `dist/`. Standard
library only, no cgo.

Then, once:

```
claude setup-token        # interactive; prints a token
weekly-insights auth      # paste it (see Authentication for why and where it goes)
weekly-insights --days 7 --open
```

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

On macOS the token is handed to `security` on standard input rather than as a
command-line argument, so it is not visible to anything that can list processes
while it is being stored.

The stage, which holds a copy of your account file, is removed when the run ends,
including on Ctrl-C.

## Platform support

macOS is tested. Linux is untested, including the credentials-file path described
under Authentication. Windows is not supported: the stage symlinks your shared config
entries, and creating symlinks on Windows needs Developer Mode or elevation.
`make dist` builds darwin and linux only.

## Usage

```
weekly-insights --days 7                  # this week's report
weekly-insights --days 7 --open           # and open it
weekly-insights --days 7 --end 2026-09-04 # a past week
weekly-insights --days 30                 # a month
```

Reports land in `~/claude-weekly-insights/insights-<days>d-<end>.html` by default.
The report is the default action; `weekly-insights run` and `weekly-insights insights`
are the same thing spelled out. Flags:

- `--days N` window length (default 7); `--end YYYY-MM-DD` window end (default now)
- `--out DIR` report directory (default `~/claude-weekly-insights`)
- `--open` open the report when done
- `--keep-stage` leave the staged config directory in place for inspection
- `--include-scratch` disable the project exclusions described below
- `--config FILE` exclusion config; `--claude PATH` Claude Code binary (default `claude`)

## Trends across weeks

The builtin has no memory between runs, so it cannot tell you whether last week's
correction stuck. Two separate paths cover that, and it is worth being clear about
which is which.

`progress` reads the raw weekly reports plus your global `CLAUDE.md` and writes a
narrative memo. It needs nothing but two or more `insights` reports on disk.

The numeric trend is a second, optional path: `select`, `prepare`, an extraction step
you run, `validate`, `aggregate`, `report`. It re-extracts each session with a pinned
vocabulary and saves a snapshot per week. When snapshots exist, `progress` includes
the resulting trend table in its prompt; when they do not, the memo is prose only.

### Path 1: the progress memo

```
weekly-insights progress                  # memo across the last 4 weekly reports
weekly-insights progress --weeks 6 --open
weekly-insights progress --dry-run        # print the exact prompt and data, no model call
```

Flags for `progress`:

- `--weeks N` how many of the most recent weekly reports to compare (default 4, minimum 2)
- `--reports DIR` where the `insights-<N>d-<date>.html` files live (default `~/claude-weekly-insights`);
  the memo is written there as `progress-<N>d-<end>.html`
- `--open` open the memo when done
- `--dry-run` print the prompt and data instead of calling the model
- `--root DIR` usage-data directory to read snapshots from; `--claude PATH` Claude Code binary

`progress` calls the model; see the network section below for exactly what it sends.

### Path 2: the numeric trend

This path exists because of a defect in how the builtin labels sessions. It ships a
canonical vocabulary internally but never puts it in its extraction prompt, so the
model invents a label per session: on one real corpus "fix a bug" appeared as
`bug_fixing`, `bug_fix`, `bug_fix_implementation`, `ui_bug_fix` and
`test_and_ci_fixes`. You cannot chart that. `prompt` pins the vocabulary, `validate`
enforces it, and `aggregate` normalizes anything already cached in a free-form one.

The first time through:

1. `weekly-insights select --days 7` shows what is in the window and how many
   sessions still need canonical facets (`--worklist` prints them as JSON lines,
   `--limit N` caps that).
2. `weekly-insights prepare --days 7 --out DIR` renders those transcripts to text
   (`--out` is required; `--all` renders every substantive session, `--limit N` caps).
   The files are 0600 in a 0700 directory; treat them as sensitive and delete them
   when you are done.
3. Run the extraction. Either use the skill in `skill/` from Claude Code, which fans
   the transcripts out to subagents, or run `weekly-insights prompt` yourself and
   feed each transcript to a model by hand. Every result must be written as
   `~/.claude/usage-data/weekly-facets/<session_id>.json`, which is where `aggregate`
   looks.
4. `weekly-insights validate --fix` enforces the vocabulary on those files
   (`--dir DIR` checks another directory). It exits non-zero if anything is left
   unresolved.
5. `weekly-insights aggregate --days 7` saves the snapshot (`--explain` shows how
   free-form labels collapsed, `--no-save` and `--quiet` for dry runs).
6. `weekly-insights report` renders it.

Flags for `report`:

- `--current LABEL` snapshot to report on (default newest); `--previous LABEL` snapshot
  to compare against (default the one before)
- `--trend` every snapshot as one table
- `--html` render a standalone page; with `--out DIR`, write one page per snapshot plus
  `trend.html` into that directory
- `--root DIR` usage-data directory

### Failed tool calls

`failures` classifies every failed tool call in the window. It pairs each errored
`tool_result` back to the `tool_use` that produced it, so the tool name and, for
Bash, the command are known at classification time.

```
weekly-insights failures --days 7
weekly-insights failures --days 28 --end 2026-09-11 --json
```

Flags for `failures`: `--days N`, `--end YYYY-MM-DD`, `--json`, `--config FILE`,
`--root DIR`, `--include-scratch`.

The classes are a fixed, ordered set and the first match wins:
`classifier_denied` (the permission classifier refused it), `hook_blocked` (a local
PreToolUse hook did), `harness_rule` (a harness guardrail such as a sleep chain or
an edit before a read), `api_flake` (5xx, rate limits, overload), `mcp_error` (by
server), `shell_error` (by leading command, with known shell signatures named), and
`other`. The report prints counts and per-session rates per class, the command
shapes behind the classifier denials, the top 15 normalized signatures with one
example command each, and the self-inflicted versus external split. `aggregate`
stores the same totals in the snapshot, so `report --trend` can chart them.

Snapshots record per-session rates (so a busy week is not penalised), the three
friction types that generic extractors miss (`unverified_claim`,
`unwanted_autonomous_action`, `ignored_stated_preference`), and every correction the
user typed, verbatim.

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
0600 inside directories created 0700. Each one is written to a fresh owner-only
file and renamed into place, so replacing a file that was left behind with looser
permissions never exposes the new contents, and a name that someone else turned
into a symlink is replaced rather than written through.

### Network and what leaves your machine

Two commands make network calls, both by running your installed Claude Code
(`claude -p`) on your own subscription. Every other command is local only.

**The default report** (`weekly-insights --days N`, also spelled `run`) runs the
builtin `/insights` against the staged config directory.
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
- **Self-inflicted versus external** is a classification by fixed rules over the
  error text, not a judgment: a rule change moves the line, so compare weeks built
  by the same version.

## License

MIT. See [LICENSE](LICENSE).
