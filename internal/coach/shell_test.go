package coach

import (
	"strings"
	"testing"

	"github.com/brianleach/weekly-insights/internal/failures"
)

// denied is n classifier denials of command, each in its own session.
func denied(n int, command string) []failures.Failure {
	var out []failures.Failure
	for i := 0; i < n; i++ {
		out = append(out, failures.Failure{Session: string(rune('a' + i)), Class: failures.ClassClassifierDenied,
			Tool: "Bash", Command: command, Shape: failures.Shape(command)})
	}
	return out
}

func only(t *testing.T, fs []failures.Failure) *Proposal {
	t.Helper()
	r := Propose(Input{Current: snap("2026-01-10", 5), Failures: fs})
	if len(r.Proposals) > 1 {
		t.Fatalf("got %v", rulesOf(r))
	}
	if len(r.Proposals) == 0 {
		return nil
	}
	return &r.Proposals[0]
}

func TestRuleDenied(t *testing.T) {
	tests := []struct {
		name, command string
		n             int
		target        string // "" means no proposal
		change, risk  string
	}{
		{"two is under", "git status", 2, "", "", ""},
		{"read-only simple", "git status", 3, TargetSettingsAllow, "Bash(git status:*)", "low"},
		{"read-only with flags", "ls -la /tmp", 3, TargetSettingsAllow, "Bash(ls:*)", "low"},
		{"go subcommand", "go test ./...", 3, TargetSettingsAllow, "Bash(go test:*)", "low"},
		{"read-only behind cd", "cd /work && git log", 3, TargetClaudeMD, "Run `git log` as one plain command", "low"},
		{"not read-only", "make deploy", 3, TargetClaudeMD, "keeps denying `make` commands. Use a read-only", "medium"},
		{"find is never allowed", "find . -name x", 3, TargetClaudeMD, "keeps denying `find`", "medium"},
		{"git subcommand off the list", "git commit -m x", 3, TargetClaudeMD, "`git commit`", "medium"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := only(t, denied(tt.n, tt.command))
			if tt.target == "" {
				if p != nil {
					t.Fatalf("fired: %+v", p)
				}
				return
			}
			if p == nil || p.Rule != RuleDenied || p.Target != tt.target ||
				!strings.Contains(p.Change, tt.change) || !strings.HasPrefix(p.Risk, tt.risk) {
				t.Fatalf("proposal = %+v", p)
			}
			if p.Evidence.Count != tt.n || p.Evidence.Sessions != tt.n {
				t.Errorf("evidence %+v", p.Evidence)
			}
		})
	}
	// Denials of other tools, and other classes, never count.
	fs := denied(3, "git status")
	for i := range fs {
		fs[i].Tool = "Read"
	}
	if p := only(t, fs); p != nil {
		t.Errorf("non-Bash denial fired: %+v", p)
	}
}

// TestRuleDeniedRiskyWords proves no allow rule is ever proposed for a shape
// carrying a risky word, even when its leading command is read-only.
func TestRuleDeniedRiskyWords(t *testing.T) {
	tests := map[string]string{
		"rm":             "ls && rm -rf build",
		"mv":             "ls; mv a b",
		"chmod":          "cat x | chmod 777 y",
		"chown":          "ls && /usr/bin/chown me f",
		"curl":           "cat urls | curl -K -",
		"wget":           "ls && wget http://example.invalid",
		"ssh":            "git status && ssh host",
		"sudo":           "sudo ls /root",
		"a redirect":     "cat a > b",
		"a pipe to sh":   "cat script | sh",
		"a pipe to bash": "cat script |bash",
		"git push":       "git status && git push origin main",
		"git reset":      "git log; git reset --hard",
		"--force":        "git status --force",
	}
	for word, command := range tests {
		t.Run(word, func(t *testing.T) {
			p := only(t, denied(3, command))
			if p == nil {
				t.Fatalf("no proposal for %q", command)
			}
			if p.Target == TargetSettingsAllow || strings.Contains(p.Change, "Bash(") {
				t.Fatalf("allow rule proposed for %q: %+v", command, p)
			}
			if !strings.HasPrefix(p.Risk, "high: uses") {
				t.Errorf("risk %q", p.Risk)
			}
		})
	}
	// One risky instance in the group is enough to withhold the allow rule.
	fs := append(denied(2, "cat a"), denied(1, "cat a > b")...)
	if p := only(t, fs); p == nil || p.Target == TargetSettingsAllow {
		t.Errorf("allow rule despite a redirect: %+v", p)
	}
	if w := riskyWord("ls -la"); w != "" {
		t.Errorf("riskyWord(ls -la) = %q", w)
	}
}

func TestRuleShell(t *testing.T) {
	shell := func(n int, sig string) []failures.Failure {
		var out []failures.Failure
		for i := 0; i < n; i++ {
			out = append(out, failures.Failure{Session: "s", Class: failures.ClassShellError, Tool: "Bash", ShellSignature: sig})
		}
		return out
	}
	if p := only(t, shell(2, "no_such_file")); p != nil {
		t.Errorf("two fired")
	}
	if p := only(t, shell(3, "")); p != nil {
		t.Errorf("plain shell error fired")
	}
	if p := only(t, shell(3, "new_signature")); p != nil {
		t.Errorf("unknown signature fired")
	}
	for sig, fix := range shellFixes {
		p := only(t, shell(3, sig))
		if p == nil || p.Rule != RuleShell || p.Change != fix.change || p.Evidence.Count != 3 || p.Score != 3 {
			t.Errorf("%s: %+v", sig, p)
		}
	}
}

func TestCommandKey(t *testing.T) {
	for in, want := range map[string]string{
		"git status":            "git status",
		"git --no-pager log -3": "git log",
		"cd /x/git && git diff": "git diff",
		"git":                   "git",
		"git && ls":             "git",
		"git status&& ls":       "git status",
		"FOO=1 go vet ./...":    "go vet",
		"ls -la":                "ls",
	} {
		if got := commandKey(in); got != want {
			t.Errorf("commandKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRender(t *testing.T) {
	in := fixture()
	in.Failures = append(in.Failures, denied(3, "cat <b>")...)
	in.Current.Sessions.Substantive = 12
	r := Propose(in)
	r.Notes = []string{"a <note>"}
	txt := Text(r, "coach")
	for _, want := range []string{"coach  4 proposal(s)", "rests on 2 of 12 sessions", "0.40/session",
		`quote:    "Use tabs"`, "note: a <note>", "change:   - Recurring correction"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text missing %q:\n%s", want, txt)
		}
	}
	h := HTML(r, "Coach <w>")
	if !strings.Contains(h, "Coach &lt;w&gt;") || !strings.Contains(h, "a &lt;note&gt;") || strings.Contains(h, "<b>") {
		t.Errorf("html not escaped:\n%s", h)
	}
	if h := HTML(Result{}, "t"); !strings.Contains(h, "No proposals") {
		t.Errorf("empty page: %s", h)
	}
}
