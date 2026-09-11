# Security

This tool reads local files and makes no network calls. It holds no credentials
and sends nothing anywhere.

It does read Claude Code session transcripts, which contain your prompts, file
paths, and project names. Rendered transcripts written by `prepare` contain that
same content in plain text, so treat the output directory as sensitive and delete
it when extraction is done.

To report a vulnerability, open a private security advisory on the repository
rather than a public issue.
