You are reviewing several consecutive weekly Claude Code insights reports for one
user, plus their current global CLAUDE.md and, when present, a numeric trend table.
Each weekly report was generated independently with no knowledge of the others, so
each one describes its week as if it were the whole history. Your job is the part
none of them could do: judge progression across the weeks.

Ground every claim in the material provided. Quote or closely paraphrase the
reports; do not invent examples. Where the weeks disagree, say so. Treat the
CLAUDE.md as evidence of what the user actually adopted, not as intent.

Sample sizes are small (roughly 30 to 40 sessions a week). A theme appearing in one
week only is not a trend. A theme appearing in three or more weeks is. Numbers that
move by one or two events are noise; say so rather than narrating them.

Respond with ONLY a valid JSON object, no prose before or after, no code fence:

{
  "verdict": "2 to 4 sentences. Is the user progressing, flat, or regressing, and on what evidence. Lead with the answer.",
  "recurring": [
    {
      "theme": "short name for a friction theme that appears in more than one week",
      "weeks": ["YYYY-MM-DD", "..."],
      "evidence": "one or two concrete examples drawn from the reports, with the week",
      "trend": "worse | same | better"
    }
  ],
  "resolved": [
    {
      "theme": "a friction theme present in earlier weeks and absent in the latest",
      "last_seen": "YYYY-MM-DD",
      "note": "why it plausibly stopped, or 'unclear' if the material does not say"
    }
  ],
  "suggestions": [
    {
      "suggestion": "a CLAUDE.md addition or practice one of the reports recommended, summarized in one line",
      "first_suggested": "YYYY-MM-DD",
      "adopted": true,
      "effect": "if adopted: did the matching friction drop in later weeks? if not adopted: did the problem persist? one sentence"
    }
  ],
  "already_have": [
    "features or practices the reports keep recommending that the CLAUDE.md or the reports themselves show are already in place"
  ],
  "numbers": "one short paragraph interpreting the numeric trend table if one was provided, naming only changes larger than noise; empty string if none",
  "one_change": "the single most valuable concrete change for next week, one or two sentences"
}

Keep every string plain text. Do not use em dashes.
