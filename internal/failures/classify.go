package failures

import (
	"regexp"
	"strings"
)

// The classes. Each failure gets exactly one, and the list is the rule order:
// the first rule that matches wins, so inserting a class changes how existing
// failures collapse and is a breaking change to the trend line.
const (
	// ClassClassifierDenied is the permission classifier refusing the call.
	ClassClassifierDenied = "classifier_denied"
	// ClassHookBlocked is a local PreToolUse hook refusing the call.
	ClassHookBlocked = "hook_blocked"
	// ClassHarnessRule is a harness guardrail: sleep chains, edit before read,
	// a command the harness could not verify stays inside a worktree.
	ClassHarnessRule = "harness_rule"
	// ClassAPIFlake is upstream weather: 5xx, rate limits, overload.
	ClassAPIFlake = "api_flake"
	// ClassMCPError is an error from an MCP server's own tool.
	ClassMCPError = "mcp_error"
	// ClassShellError is a Bash call that exited non-zero for its own reasons.
	ClassShellError = "shell_error"
	// ClassOther is the residue. A healthy week empties it.
	ClassOther = "other"
)

// Classes is the rule order, which is also the order the report prints.
var Classes = []string{
	ClassClassifierDenied,
	ClassHookBlocked,
	ClassHarnessRule,
	ClassAPIFlake,
	ClassMCPError,
	ClassShellError,
	ClassOther,
}

// Command shapes. A shape is computed for Bash calls only; it is what says
// whether a denied command had an allowlistable form.
const (
	ShapeHeredoc   = "heredoc"
	ShapeCDPrefix  = "cd_prefixed"
	ShapeMultiline = "multiline"
	ShapeCompound  = "compound"
	ShapeSimple    = "simple"
)

// harnessMarkers are the harness guardrails worth naming. They only count
// inside a <tool_use_error>, which is how the harness marks its own refusals;
// the same words appearing in a program's output are that program's business.
var harnessMarkers = []struct{ needle, detail string }{
	{"sleep", "sleep_chain"},
	{"Monitor", "sleep_chain"},
	{"too complex to verify", "worktree_verify"},
	{"has not been read yet", "edit_before_read"},
	{"File has not been read", "edit_before_read"},
	{"run_in_background", "run_in_background"},
}

// apiFlakePatterns are upstream failures no local change can prevent. The bare
// 5xx form is bounded by non-digits so it does not fire on a longer number.
var apiFlakePatterns = regexp.MustCompile(
	`(^|[^0-9])5[0-9]{2}([^0-9]|$)|529|rate limit|Overloaded|No server is currently available|temporarily unavailable|communicating with the Sentry API`)

// hookNamePattern picks a hook out of a block message: either a script path or
// a bare script name.
var hookNamePattern = regexp.MustCompile(`[A-Za-z0-9_./-]*[A-Za-z0-9_-]+\.(sh|py|js|ts)`)

// shellSignatures are the shell failure modes that are the caller's fault
// rather than the command's verdict. Everything else is a program reporting a
// real result through its exit code.
var shellSignatures = []struct{ needle, name string }{
	{"(eval):", "zsh_word_splitting"},
	{"== not found", "zsh_equals"},
	{"parse error near", "parse_error"},
	{"command not found", "command_not_found"},
	{"No such file or directory", "no_such_file"},
	{"Permission denied", "permission_denied"},
}

// cdPrefix matches a leading `cd <dir> &&`, the shape that defeats an allow
// rule written against the command that follows it.
var cdPrefix = regexp.MustCompile(`^\s*cd\s+\S+\s*&&\s*`)

// Classify applies the ordered rule set to one failed call and returns its
// class plus the detail that names the thing inside the class.
//
// Order matters and is the taxonomy: a classifier denial mentioning a 500 is a
// denial, and a hook that blocked an MCP call is a hook block.
func Classify(tool, command, text string) (class, detail string) {
	switch {
	case strings.Contains(text, "auto mode classifier"):
		return ClassClassifierDenied, denialReason(text)
	case strings.Contains(text, "PreToolUse:") || strings.Contains(text, "hook error"):
		return ClassHookBlocked, hookName(text)
	case strings.Contains(text, "<tool_use_error>"):
		if d, ok := harnessMarker(text); ok {
			return ClassHarnessRule, d
		}
	}
	if apiFlakePatterns.MatchString(text) {
		return ClassAPIFlake, ""
	}
	if strings.HasPrefix(tool, "mcp__") {
		return ClassMCPError, mcpServer(tool)
	}
	if isBash(tool) {
		return ClassShellError, LeadingCommand(command)
	}
	return ClassOther, ""
}

func isBash(tool string) bool { return tool == "Bash" }

// harnessMarker reports which guardrail a tool_use_error tripped.
func harnessMarker(text string) (string, bool) {
	for _, m := range harnessMarkers {
		if strings.Contains(text, m.needle) {
			return m.detail, true
		}
	}
	return "", false
}

// denialReason pulls the bracketed reason the classifier quotes, e.g.
// "Reason: [Credential Leakage]". It is the only part of that message that
// varies, so without it every denial collapses to one signature.
func denialReason(text string) string {
	i := strings.Index(text, "Reason: [")
	if i < 0 {
		return ""
	}
	rest := text[i+len("Reason: ["):]
	j := strings.Index(rest, "]")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// hookName names the hook that blocked the call, when the message carries a
// script path or name.
func hookName(text string) string {
	m := hookNamePattern.FindString(text)
	if m == "" {
		return ""
	}
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// mcpServer is the server segment of an MCP tool name, which is the unit an
// upstream report would be filed against.
func mcpServer(tool string) string {
	parts := strings.Split(strings.TrimPrefix(tool, "mcp__"), "__")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// shellSignature reports the recognized shell failure mode, or "".
func shellSignature(text string) string {
	for _, s := range shellSignatures {
		if strings.Contains(text, s.needle) {
			return s.name
		}
	}
	return ""
}

// LeadingCommand is the command word an allow rule would have to match: the
// first token of the first segment, with a leading `cd <dir> &&` stripped
// because that prefix is exactly what hides the real command from the rule.
func LeadingCommand(command string) string {
	c := cdPrefix.ReplaceAllString(command, "")
	c = strings.TrimSpace(c)
	if c == "" {
		return ""
	}
	// Cut at the first separator: only the first segment runs unconditionally.
	if i := strings.IndexAny(c, "&;|\n"); i >= 0 {
		c = c[:i]
	}
	fields := strings.Fields(c)
	for _, f := range fields {
		// A leading VAR=value is an environment assignment, not the command.
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "-") {
			continue
		}
		return f
	}
	return ""
}

// Shape classifies a Bash command by the form that decides whether an allow
// rule can match it. The specific shapes are tested before the general ones,
// since a `cd X && git status` is compound too and naming it compound would
// lose the fact that is actually wrong with it.
func Shape(command string) string {
	c := strings.TrimSpace(command)
	if c == "" {
		return ""
	}
	switch {
	case strings.Contains(c, "<<"):
		return ShapeHeredoc
	case cdPrefix.MatchString(c):
		return ShapeCDPrefix
	case strings.Contains(c, "\n"):
		return ShapeMultiline
	case strings.ContainsAny(c, "&;|"):
		return ShapeCompound
	}
	return ShapeSimple
}

// Normalization patterns, applied in this order. URLs go first because a URL
// contains a path, and digits go last because every earlier pattern would
// otherwise be matching against placeholders.
var (
	urlPattern    = regexp.MustCompile(`https?://\S+`)
	pathPattern   = regexp.MustCompile(`(/[A-Za-z0-9._~@+-]+){2,}/?`)
	hexPattern    = regexp.MustCompile(`\b[0-9a-f]{7,}\b`)
	digitPattern  = regexp.MustCompile(`[0-9]+`)
	spacePattern  = regexp.MustCompile(`\s+`)
	maxSignature  = 100
	sigReplacings = []struct {
		re   *regexp.Regexp
		with string
	}{
		{urlPattern, "<url>"},
		{pathPattern, "<path>"},
		{hexPattern, "<hash>"},
		{digitPattern, "N"},
	}
)

// Normalize turns one error message into a signature that groups with the
// other instances of the same failure. Everything that varies per occurrence
// (URLs, paths, hashes, numbers, line breaks) is collapsed, which is what makes
// a top-signature table a list of problems rather than a list of incidents.
func Normalize(text string) string {
	s := text
	s = strings.ReplaceAll(s, "<tool_use_error>", " ")
	s = strings.ReplaceAll(s, "</tool_use_error>", " ")
	for _, r := range sigReplacings {
		s = r.re.ReplaceAllString(s, r.with)
	}
	s = spacePattern.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return truncate(s, maxSignature)
}
