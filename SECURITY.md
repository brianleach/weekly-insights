# Security

## What the tool touches

- **Session transcripts** under `~/.claude/projects/`. These contain your prompts,
  file paths, and project names. `insights` copies the window's transcripts into a
  temporary staging directory for the duration of one run and deletes it afterwards.
  `prepare` writes rendered transcripts to a directory you name; treat that
  directory as sensitive and delete it when extraction is done.
- **Your account file** (`~/.claude.json`) and, where present, `.credentials.json`.
  `insights` copies these into the staging directory so the child process is logged
  in as you, then deletes the stage. Nothing is written back to the originals.
- **A long-lived token** from `claude setup-token`, if you store one with
  `weekly-insights auth`. It goes to the macOS keychain, or to a file in the platform
  user config directory created with mode 0600. It is read at run time, placed in the
  child's environment, and never logged or written elsewhere. `auth --clear` removes it.

## Network

The `insights` and `progress` commands run your installed Claude Code (`claude -p`),
which calls the model API exactly as an interactive `/insights` would, on your own
subscription. Nothing else in this tool makes a network request, and nothing is
uploaded by the tool itself.

## What is never committed

The token lives outside any repository by design, and `.gitignore` excludes `.env`
and token files as a backstop. Test fixtures are synthetic; no real transcripts,
facets, or snapshots belong in this repository.

## Reporting

To report a vulnerability, open a private security advisory on the repository rather
than a public issue.
