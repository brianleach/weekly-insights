# Contributing

## Building

```
make build
make test
make lint
```

Go 1.26 or newer. There are no third-party dependencies and none should be added
without a strong reason: a single static binary with no supply chain is a feature
for a tool that reads local usage data.

## Testing with real data

Do not. Every test fixture must be synthetic and constructed in a temp directory.
This repository must never contain session transcripts, facets, or snapshots from
a real user, because those hold prompts, file paths, and project names.

## Changing the taxonomy

`internal/taxonomy` is the one place where a careless change breaks historical
comparability. Adding a canonical label is fine. Renaming or removing one silently
reinterprets every snapshot already on disk, so treat it as a breaking change.

Rule order encodes precedence and the first match wins, so inserting a rule can
change how existing labels collapse. `TestCanonicalLabelsAreStable` guards the
fixed-point property; add a case to `TestNormGoal` or `TestNormFriction` for any
collapse you intend.

## Snapshot compatibility

`internal/snapshot.Snapshot` is a persisted format. Adding a field is safe. Changing
or removing one invalidates trend lines people have been accumulating, which is the
entire value of the tool. Prefer adding.
