package coach

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/brianleach/weekly-insights/internal/failures"
	"github.com/brianleach/weekly-insights/internal/snapshot"
)

// R4 and R5 thresholds: the same denied command, or the same self-inflicted
// shell signature, this many times in the window.
const (
	MinDeniedRepeats = 3
	MinShellRepeats  = 3
)

// readOnly is the fixed list of commands R4 may propose an allow rule for:
// each only reads. find is left off on purpose: an allow rule is a prefix
// match, so Bash(find:*) would also allow find -delete.
var readOnly = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "rg": true,
	"git status": true, "git log": true, "git diff": true, "git show": true,
	"go test": true, "go vet": true,
}

// riskyWords never get an allow rule, whatever the leading command.
var riskyWords = []string{"rm", "mv", "chmod", "chown", "curl", "wget", "ssh", "sudo"}

var (
	pipeToShell = regexp.MustCompile(`\|\s*(sudo\s+)?(sh|bash|zsh|dash|ksh)\b`)
	gitRisky    = regexp.MustCompile(`\bgit\s+(push|reset)\b`)
)

// shellFixes maps a self-inflicted shell signature to its fixed instruction.
var shellFixes = map[string]struct{ title, change string }{
	"zsh_word_splitting": {"Quote for zsh",
		"- The shell is zsh, which does not split unquoted variables into words. Quote expansions and use arrays, or run the command through bash -c."},
	"zsh_equals": {"Quote = in zsh",
		"- In zsh a word starting with = is expanded, so quote it, and use = rather than == in test expressions."},
	"parse_error": {"Keep shell commands simple",
		"- Keep shell commands simple: no nested quoting or multi-line constructs inline. Write a script file for anything complex."},
	"command_not_found": {"Check a command exists first",
		"- Before relying on a command, check it exists with command -v, and use the tools the project documents."},
	"no_such_file": {"Check paths first",
		"- Check that a path exists before reading or running it, and prefer absolute paths."},
	"permission_denied": {"Stop on permission denied",
		"- When a command fails with Permission denied, report it instead of retrying or working around it."},
}

// ruleDenied is R4, the classifier-denied command that keeps coming back.
func ruleDenied(cur snapshot.Snapshot, _ []snapshot.Snapshot, fs []failures.Failure) ([]Proposal, []string) {
	var out []Proposal
	for _, g := range groupFailures(fs, func(f failures.Failure) string {
		if f.Class != failures.ClassClassifierDenied || f.Tool != "Bash" {
			return ""
		}
		return commandKey(f.Command)
	}) {
		if g.count < MinDeniedRepeats {
			continue
		}
		p := Proposal{Rule: RuleDenied, Target: TargetClaudeMD, Evidence: g.evidence(cur)}
		word, simple := "", true
		for _, f := range g.items {
			if w := riskyWord(f.Command); w != "" && word == "" {
				word = w
			}
			simple = simple && f.Shape == failures.ShapeSimple
		}
		switch {
		case word != "":
			p.Title = "Avoid a risky denied command"
			p.Change = fmt.Sprintf("- The permission classifier keeps denying `%s` commands, which use %s. Use a safer form or ask the user first.", g.key, word)
			p.Risk = "high: uses " + word + ", no allow rule proposed"
		case !readOnly[g.key]:
			p.Title = "Rethink a denied command"
			p.Change = fmt.Sprintf("- The permission classifier keeps denying `%s` commands. Use a read-only alternative or ask the user first.", g.key)
			p.Risk = "medium: not on the read-only list, no allow rule proposed"
		case !simple:
			p.Title = "Run a read-only command plainly"
			p.Change = fmt.Sprintf("- Run `%s` as one plain command, with no cd prefix, chaining or heredoc, so an allow rule can match it.", g.key)
			p.Risk = "low"
		default:
			p.Title = "Allow a read-only command"
			p.Target, p.Change, p.Risk = TargetSettingsAllow, "Bash("+g.key+":*)", "low"
		}
		out = append(out, p)
	}
	return out, nil
}

// ruleShell is R5, the self-inflicted shell signature that keeps coming back.
func ruleShell(cur snapshot.Snapshot, _ []snapshot.Snapshot, fs []failures.Failure) ([]Proposal, []string) {
	var out []Proposal
	for _, g := range groupFailures(fs, func(f failures.Failure) string {
		if f.Class != failures.ClassShellError {
			return ""
		}
		return f.ShellSignature
	}) {
		fix, ok := shellFixes[g.key]
		if !ok || g.count < MinShellRepeats {
			continue
		}
		out = append(out, Proposal{
			Rule: RuleShell, Title: fix.title, Target: TargetClaudeMD, Change: fix.change, Risk: "low",
			Evidence: g.evidence(cur),
		})
	}
	return out, nil
}

// commandKey is the command an allow rule would name: the leading command,
// plus the subcommand for git and go.
func commandKey(command string) string {
	lead := failures.LeadingCommand(command)
	if lead != "git" && lead != "go" {
		return lead
	}
	fields := strings.Fields(command)
	for i, f := range fields {
		if f != lead {
			continue
		}
		for _, n := range fields[i+1:] {
			n = strings.TrimRight(n, "&;|")
			if n == "" || strings.ContainsAny(n, "&;|") {
				break
			}
			if !strings.HasPrefix(n, "-") {
				return lead + " " + n
			}
		}
		break
	}
	return lead
}

// riskyWord names the first reason a command must never get an allow rule,
// or "".
func riskyWord(command string) string {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
	})
	for _, f := range fields {
		base := f[strings.LastIndex(f, "/")+1:]
		for _, w := range riskyWords {
			if base == w {
				return w
			}
		}
	}
	switch {
	case strings.Contains(command, ">"):
		return "a redirect"
	case pipeToShell.MatchString(command):
		return "a pipe to a shell"
	case gitRisky.MatchString(command):
		return "git " + gitRisky.FindStringSubmatch(command)[1]
	case strings.Contains(command, "--force"):
		return "--force"
	}
	return ""
}
