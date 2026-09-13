package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCmdPrepareRequiresOut(t *testing.T) {
	err := cmdPrepare([]string{"--days", "7"})
	if err == nil || !strings.Contains(err.Error(), "--out is required") {
		t.Fatalf("cmdPrepare without --out: got %v, want --out is required", err)
	}
}

func TestCmdPrepareHelpShowsZeroDefaults(t *testing.T) {
	if os.Getenv("WEEKLY_INSIGHTS_PREPARE_HELP") == "1" {
		// -h makes the ExitOnError flag set print its defaults and exit 0.
		_ = cmdPrepare([]string{"-h"})
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCmdPrepareHelpShowsZeroDefaults$")
	cmd.Env = append(os.Environ(), "WEEKLY_INSIGHTS_PREPARE_HELP=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare -h: %v\n%s", err, out)
	}
	help := string(out)

	// The flag package appends " (default X)" only for non-zero defaults, so
	// each usage line ending right after its text proves the default is zero.
	for _, want := range []string{
		"do not apply the config's project exclusions\n",
		"cap the number of transcripts (0 means no cap)\n",
		"render every substantive session, not only those needing facets\n",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("prepare -h output missing %q (non-zero default?)\n%s", want, help)
		}
	}
}

func TestCmdReportWithoutSnapshots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	err := cmdReport([]string{"--root", root})
	if err == nil {
		t.Fatal("cmdReport on an empty root: got nil error, want one")
	}
}

func TestCmdValidateEmptyDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	runErr := cmdValidate([]string{"--root", t.TempDir(), "--dir", dir})
	os.Stdout = stdout
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("cmdValidate: %v", runErr)
	}
	if !strings.Contains(string(out), "0 facet files, 0 with problems") {
		t.Fatalf("cmdValidate output = %q, want a zero-file summary", out)
	}
}

func TestCmdProgressRejectsInsufficientInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := cmdProgress([]string{"--weeks", "1"})
	if err == nil || !strings.Contains(err.Error(), "--weeks must be at least 2") {
		t.Fatalf("cmdProgress --weeks 1: got %v, want a --weeks error", err)
	}

	def := home + "/claude-weekly-insights"
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	err = cmdProgress(nil)
	if err == nil || !strings.Contains(err.Error(), "need at least two weekly reports in "+def+" to judge progression; found 0") {
		t.Fatalf("cmdProgress with default reports dir: got %v, want a too-few-reports error naming %s", err, def)
	}

	notDir := home + "/not-a-dir"
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProgress([]string{"--reports", notDir}); err == nil {
		t.Fatal("cmdProgress with a file as --reports: got nil error, want one")
	}

	t.Setenv("HOME", "")
	if err := cmdProgress(nil); err == nil {
		t.Fatal("cmdProgress without HOME: got nil error, want one")
	}
}

func TestCmdProgressDryRunKeepsDefaultFourWeeks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	page := "<html><head><title>Claude Code Insights</title></head><body><h1>Claude Code Insights</h1><p>You worked on tests.</p></body></html>"
	for _, date := range []string{"2026-01-01", "2026-01-08", "2026-01-15", "2026-01-22", "2026-01-29"} {
		if err := os.WriteFile(dir+"/insights-7d-"+date+".html", []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	stdout := os.Stdout
	os.Stdout = w
	runErr := cmdProgress([]string{"--reports", dir, "--root", t.TempDir(), "--dry-run"})
	os.Stdout = stdout
	w.Close()
	out := string(<-done)

	if runErr != nil {
		t.Fatalf("cmdProgress --dry-run: %v", runErr)
	}
	for _, want := range []string{"2026-01-08", "2026-01-15", "2026-01-22", "2026-01-29"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing week %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "2026-01-01") {
		t.Fatalf("dry-run output includes a week beyond the default of 4:\n%s", out)
	}
}

func TestCmdProgressWritesMemoWithoutOpening(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	page := "<html><head><title>Claude Code Insights</title></head><body><h1>Claude Code Insights</h1><p>You worked on tests.</p></body></html>"
	for _, date := range []string{"2026-01-08", "2026-01-15"} {
		if err := os.WriteFile(dir+"/insights-7d-"+date+".html", []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	claude := bin + "/fake-claude"
	if err := os.WriteFile(claude, []byte("#!/bin/sh\ncat >/dev/null\necho 'not a memo'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+"/xdg-open", []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	if err := cmdProgress([]string{"--reports", dir, "--root", t.TempDir(), "--claude", claude}); err != nil {
		t.Fatalf("cmdProgress: %v", err)
	}
	memo, err := os.ReadFile(dir + "/progress-7d-2026-01-15.html")
	if err != nil {
		t.Fatalf("memo was not written (did the default run as --dry-run?): %v", err)
	}
	if len(memo) == 0 {
		t.Fatal("memo is empty")
	}

	// Without --open no opener process may be started; a started one stays
	// listed as a child until reaped.
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		b, err := os.ReadFile("/proc/self/task/" + task.Name() + "/children")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(b)) != "" {
			t.Fatalf("cmdProgress without --open started a child process: %s", b)
		}
	}
}

func TestCmdProgress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/.claude", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home+"/.claude/CLAUDE.md", []byte("ADOPTED-RULE-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	reports := t.TempDir()
	for _, d := range []string{"2026-08-30", "2026-09-06"} {
		page := "<html><head><title>Claude Code Insights</title></head><body><h1>Insights</h1><p>Worked on the " + d + " week.</p></body></html>"
		if err := os.WriteFile(reports+"/insights-7d-"+d+".html", []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for _, end := range []string{"2026-08-30", "2026-09-06"} {
		if err := cmdAggregate([]string{"--root", root, "--end", end, "--quiet"}); err != nil {
			t.Fatalf("cmdAggregate --end %s: %v", end, err)
		}
	}

	// Both snapshots must land under --root and be labeled by their --end, so
	// the trend over that root lists each window separately.
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = f
	reportErr := cmdReport([]string{"--root", root, "--trend"})
	os.Stdout = stdout
	f.Close()
	trend, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if reportErr != nil {
		t.Fatalf("cmdReport --trend over the aggregated root: %v", reportErr)
	}
	for _, want := range []string{"2026-08-30", "2026-09-06"} {
		if !strings.Contains(string(trend), want) {
			t.Fatalf("trend output missing window %q:\n%s", want, trend)
		}
	}

	memoPath := reports + "/progress-7d-2026-09-06.html"

	t.Run("dry run", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "stdout")
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = f
		runErr := cmdProgress([]string{"--reports", reports, "--root", root, "--dry-run"})
		os.Stdout = stdout
		f.Close()
		out, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if runErr != nil {
			t.Fatalf("cmdProgress --dry-run: %v", runErr)
		}
		if !strings.Contains(string(out), "ADOPTED-RULE-MARKER") {
			t.Fatalf("dry-run output missing CLAUDE.md contents:\n%s", out)
		}
	})

	t.Run("child failure", func(t *testing.T) {
		bin := t.TempDir() + "/claude"
		if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := cmdProgress([]string{"--reports", reports, "--root", root, "--claude", bin})
		if err == nil {
			t.Fatal("cmdProgress with a failing child: got nil error, want one")
		}
		if _, statErr := os.Stat(memoPath); statErr == nil {
			t.Fatal("memo written despite a failed child")
		}
	})

	bin := t.TempDir() + "/claude"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\necho RAW-UNPARSEABLE-REPLY\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("memo write failure", func(t *testing.T) {
		if err := os.Mkdir(memoPath, 0o700); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(memoPath)
		err := cmdProgress([]string{"--reports", reports, "--root", root, "--claude", bin})
		if err == nil || !strings.Contains(err.Error(), "writing memo") {
			t.Fatalf("cmdProgress with an unwritable memo path: got %v, want writing memo error", err)
		}
	})

	t.Run("unparseable reply is written raw", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := cmdProgress([]string{"--reports", reports, "--root", root, "--claude", bin, "--open"}); err != nil {
			t.Fatalf("cmdProgress: %v", err)
		}
		b, err := os.ReadFile(memoPath)
		if err != nil {
			t.Fatalf("reading memo: %v", err)
		}
		if !strings.Contains(string(b), "RAW-UNPARSEABLE-REPLY") {
			t.Fatalf("memo does not contain the raw reply:\n%s", b)
		}
	})
}

func TestCmdAuthStoresPipedTokenInFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString("sk-ant-oat01-" + strings.Repeat("a", 90) + "\n"); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stderr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = inR, errW
	runErr := cmdAuth(nil)
	os.Stdin, os.Stderr = stdin, stderr
	errW.Close()
	inR.Close()
	out, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("cmdAuth: %v\n%s", runErr, out)
	}
	if !strings.Contains(string(out), "token stored ("+home) {
		t.Fatalf("cmdAuth stderr = %q, want the stored file path under %s", out, home)
	}
}

func TestCmdSelectRejectsBadEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := cmdSelect([]string{"--root", t.TempDir(), "--days", "7", "--end", "15/01/2026"})
	if err == nil || !strings.Contains(err.Error(), `parsing --end "15/01/2026"`) {
		t.Fatalf("cmdSelect with a bad --end: got %v, want a parse error", err)
	}
}

func TestCommonFlagsResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	c := commonFlags{days: 7, end: "2026-01-15", root: root, includeScratch: true}
	p, o, err := c.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Root != root {
		t.Errorf("Root = %q, want %q", p.Root, root)
	}
	if o.Days != 7 || !o.IncludeScratch {
		t.Errorf("Options = %+v, want Days 7 and IncludeScratch", o)
	}
	if got := o.End.Format("2006-01-02 15:04:05 MST"); got != "2026-01-15 23:59:59 UTC" {
		t.Errorf("End = %q, want the last second of the end day", got)
	}

	cfg := t.TempDir() + "/config.json"
	if err := os.WriteFile(cfg, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := commonFlags{days: 7, config: cfg}
	if _, _, err := bad.resolve(); err == nil {
		t.Error("resolve with a malformed config: got nil error, want one")
	}

	t.Setenv("HOME", "")
	if _, _, err := (&commonFlags{days: 7}).resolve(); err == nil {
		t.Error("resolve without a home directory: got nil error, want one")
	}
}

func TestMainDispatch(t *testing.T) {
	if os.Getenv("WEEKLY_INSIGHTS_MAIN") == "1" {
		os.Args = []string{"weekly-insights"}
		if a := os.Getenv("WEEKLY_INSIGHTS_MAIN_ARGS"); a != "" {
			os.Args = append(os.Args, strings.Split(a, "\x1f")...)
		}
		main()
		// Exit here so the test runner's PASS line does not pollute the output.
		os.Exit(0)
	}
	home := t.TempDir()
	root := t.TempDir()
	dir := t.TempDir()

	cases := []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "Usage:"},
		{[]string{"--end", "bad"}, 1, "parsing --end"},
		{[]string{"run", "--end", "bad"}, 1, "parsing --end"},
		{[]string{"insights", "--end", "bad"}, 1, "parsing --end"},
		{[]string{"auth", "--check"}, -1, "token"},
		{[]string{"progress", "--weeks", "1"}, 1, "--weeks must be at least 2"},
		{[]string{"select", "--end", "bad"}, 1, "parsing --end"},
		{[]string{"failures", "--end", "bad"}, 1, "parsing --end"},
		{[]string{"prepare"}, 1, "--out is required"},
		{[]string{"aggregate", "--end", "bad"}, 1, "parsing --end"},
		{[]string{"report", "--root", root}, 1, "no snapshots"},
		{[]string{"validate", "--root", root, "--dir", dir}, 0, "0 facet files, 0 with problems"},
		{[]string{"prompt"}, 0, ""},
		{[]string{"version"}, 0, "dev\n"},
		{[]string{"-v"}, 0, "dev\n"},
		{[]string{"help"}, 0, "Usage:"},
		{[]string{"--help"}, 0, "Usage:"},
		{[]string{"bogus"}, 2, `unknown command "bogus"`},
	}
	for _, c := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainDispatch$")
		cmd.Env = append(os.Environ(),
			"WEEKLY_INSIGHTS_MAIN=1",
			"WEEKLY_INSIGHTS_MAIN_ARGS="+strings.Join(c.args, "\x1f"),
			"HOME="+home,
		)
		out, _ := cmd.CombinedOutput()
		got := string(out)
		code := cmd.ProcessState.ExitCode()
		if c.code >= 0 && code != c.code {
			t.Errorf("args %q: exit code %d, want %d\n%s", c.args, code, c.code, got)
		}
		if c.code < 0 && code != 0 && code != 1 {
			t.Errorf("args %q: exit code %d, want 0 or 1\n%s", c.args, code, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("args %q: output %q, want it to contain %q", c.args, got, c.want)
		}
		if c.want == "" && (strings.TrimSpace(got) == "" || strings.Contains(got, "Usage:")) {
			t.Errorf("args %q: output %q, want non-usage output", c.args, got)
		}
	}
}

func TestResolvedVersion(t *testing.T) {
	saved := version
	defer func() { version = saved }()

	// A test binary carries no module version, so an unstamped build must
	// report the placeholder rather than an empty or "(devel)" string.
	version = "dev"
	if got := resolvedVersion(); got != "dev" {
		t.Errorf("resolvedVersion() with no stamp = %q, want %q", got, "dev")
	}

	version = "v9.9.9"
	if got := resolvedVersion(); got != "v9.9.9" {
		t.Errorf("resolvedVersion() with ldflags stamp = %q, want %q", got, "v9.9.9")
	}
}
