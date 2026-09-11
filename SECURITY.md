# Security

## What the tool touches

- **Session transcripts** under `~/.claude/projects/`. These contain your prompts,
  file paths, and project names. `insights` copies the window's transcripts into a
  temporary staging directory for the duration of one run and deletes it afterwards.
  `prepare` writes rendered transcripts to a directory you name; treat that
  directory as sensitive and delete it when extraction is done.
- **Your global `~/.claude/CLAUDE.md`.** `progress` reads it and sends it to the
  model as part of the prompt (see Network below).
- **Your account file** (`~/.claude.json`) and, where present, `.credentials.json`.
  `insights` copies these into the staging directory so the child process is logged
  in as you, then deletes the stage. Nothing is written back to the originals.
- **A long-lived token** from `claude setup-token`, if you store one with
  `weekly-insights auth`. It goes to the macOS keychain, or to a file in the platform
  user config directory created with mode 0600. It is read at run time, placed in the
  child's environment, and never logged or written elsewhere. `auth --clear` removes it.

## File permissions

Sensitive outputs are written with mode 0600 inside directories created 0700:
staged transcripts and caches, transcripts rendered by `prepare`, the HTML reports,
the snapshots, and the progress memo.

## Network

Two commands make network calls, and both do it by running your installed Claude
Code (`claude -p`) on your own subscription. Nothing else in this tool makes a
network request, and the tool itself uploads nothing anywhere.

`insights` runs the builtin `/insights` against the staged config directory, so the
child sends what a normal `/insights` sends: the session transcripts in the window
(prompts, assistant replies, file paths, project names) and their cached metadata.

`progress` sends, in one prompt:

- the extracted text of the last N weekly reports: at-a-glance summaries, project
  areas, wins, friction categories and their verbatim examples, suggested
  `CLAUDE.md` additions, suggested features, and horizon items
- **your current global `~/.claude/CLAUDE.md`, in full, truncated only at 24000
  characters.** If that file holds anything you do not want sent to the model, do
  not run `progress`.
- the numeric trend table, when snapshots exist

## What is never committed

The token lives outside any repository by design, and `.gitignore` excludes `.env`
and token files as a backstop. Test fixtures are synthetic; no real transcripts,
facets, or snapshots belong in this repository.

## Reporting

To report a vulnerability, open a private security advisory on the repository rather
than a public issue.
