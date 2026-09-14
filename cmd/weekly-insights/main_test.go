package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	os.Stdout = orig
	w.Close()
	return <-done, runErr
}

func TestCmdFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	args := []string{"--root", root, "--days", "7", "--end", "2026-01-10"}

	out, err := captureStdout(t, func() error { return cmdFailures(args) })
	if err != nil {
		t.Fatalf("cmdFailures: %v", err)
	}
	if !strings.Contains(out, "2026-01-10") || !strings.Contains(out, "(7d)") {
		t.Errorf("report missing window label: %q", out)
	}

	out, err = captureStdout(t, func() error { return cmdFailures(append(args, "--json")) })
	if err != nil {
		t.Fatalf("cmdFailures --json: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("--json output is not JSON: %q", out)
	}
}

func TestCmdPrepare(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	err := cmdPrepare([]string{"--root", root})
	if err == nil || !strings.Contains(err.Error(), "--out is required") {
		t.Fatalf("want --out required error, got %v", err)
	}

	dir := filepath.Join(t.TempDir(), "rendered")
	if err := cmdPrepare([]string{"--root", root, "--end", "2026-01-10", "--out", dir, "--all", "--limit", "1"}); err != nil {
		t.Fatalf("cmdPrepare: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("out dir not created: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("out dir perms = %v, want owner-only", fi.Mode().Perm())
	}
}

func TestCmdAggregateAndReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	err := cmdReport([]string{"--root", root})
	if err == nil || !strings.Contains(err.Error(), "no snapshots") {
		t.Fatalf("want no snapshots error, got %v", err)
	}

	out, err := captureStdout(t, func() error {
		return cmdAggregate([]string{"--root", root, "--days", "7", "--end", "2026-01-10", "--explain"})
	})
	if err != nil {
		t.Fatalf("cmdAggregate: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("aggregate output is not JSON: %q", out)
	}

	if err := cmdReport([]string{"--root", root, "--out", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--out requires --html") {
		t.Errorf("want --out requires --html error, got %v", err)
	}

	htmlDir := filepath.Join(t.TempDir(), "pages")
	out, err = captureStdout(t, func() error {
		return cmdReport([]string{"--root", root, "--html", "--out", htmlDir})
	})
	if err != nil {
		t.Fatalf("cmdReport --html --out: %v", err)
	}
	weeks, _ := filepath.Glob(filepath.Join(htmlDir, "week-*.html"))
	if len(weeks) != 1 {
		t.Errorf("want one week page, got %v", weeks)
	}
	trendPath := filepath.Join(htmlDir, "trend.html")
	if _, err := os.Stat(trendPath); err != nil {
		t.Errorf("trend.html not written: %v", err)
	}
	if !strings.Contains(out, trendPath) {
		t.Errorf("paths not printed: %q", out)
	}

	for _, args := range [][]string{{"--trend"}, {"--trend", "--html"}, {"--html"}, {}} {
		out, err := captureStdout(t, func() error { return cmdReport(append([]string{"--root", root}, args...)) })
		if err != nil {
			t.Errorf("cmdReport %v: %v", args, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("cmdReport %v printed nothing", args)
		}
	}

	if err := cmdReport([]string{"--root", root, "--current", "nope"}); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("want unknown current error, got %v", err)
	}
	if err := cmdReport([]string{"--root", root, "--previous", "gone"}); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Errorf("want unknown previous error, got %v", err)
	}
}

func TestCmdValidateEmptyDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := captureStdout(t, func() error {
		return cmdValidate([]string{"--root", t.TempDir(), "--dir", t.TempDir()})
	})
	if err != nil {
		t.Fatalf("cmdValidate: %v", err)
	}
	if !strings.Contains(out, "0 facet files, 0 with problems") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestCmdSelect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	args := []string{"--root", root, "--days", "7", "--end", "2026-01-10"}

	out, err := captureStdout(t, func() error { return cmdSelect(args) })
	if err != nil {
		t.Fatalf("cmdSelect: %v", err)
	}
	for _, want := range []string{"2026-01-10", "(7d)", "0 in window", "0 substantive", "to extract   : 0", "top projects :"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q: %q", want, out)
		}
	}

	out, err = captureStdout(t, func() error { return cmdSelect(append(args, "--worklist", "--limit", "1")) })
	if err != nil {
		t.Fatalf("cmdSelect --worklist: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("worklist for an empty window should be empty, got %q", out)
	}

	if err := cmdSelect([]string{"--root", root, "--end", "01/10/2026"}); err == nil || !strings.Contains(err.Error(), "--end") {
		t.Errorf("want --end parse error, got %v", err)
	}
}

func TestCmdInsightsNoSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	err := cmdInsights([]string{"--root", root, "--end", "not-a-date"})
	if err == nil || !strings.Contains(err.Error(), "parsing --end") {
		t.Fatalf("want --end parse error, got %v", err)
	}

	err = cmdInsights([]string{"--root", root, "--days", "7", "--end", "2026-01-10", "--keep-stage", "--claude", filepath.Join(root, "missing-claude")})
	if err == nil || !strings.Contains(err.Error(), "no substantive sessions in the last 7 days") {
		t.Fatalf("want no substantive sessions error, got %v", err)
	}
}

func TestCmdProgress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()

	if err := cmdProgress([]string{"--weeks", "1"}); err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("want --weeks error, got %v", err)
	}

	empty := t.TempDir()
	if err := cmdProgress([]string{"--reports", empty, "--root", root}); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("want too few reports error, got %v", err)
	}

	page := "<html><head><title>Claude Code Insights</title></head><body><h1>Claude Code Insights</h1><p>What you worked on</p></body></html>"
	dir := t.TempDir()
	for _, d := range []string{"2025-12-13", "2025-12-20", "2025-12-27", "2026-01-03", "2026-01-10"} {
		if err := os.WriteFile(filepath.Join(dir, "insights-7d-"+d+".html"), []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, err := captureStdout(t, func() error {
		return cmdProgress([]string{"--reports", dir, "--root", root, "--dry-run", "--claude", filepath.Join(root, "no-claude")})
	})
	if err != nil {
		t.Fatalf("cmdProgress --dry-run: %v", err)
	}
	if !strings.Contains(out, "2025-12-20") || !strings.Contains(out, "2026-01-10") {
		t.Errorf("default dry run missing the last four weeks: %q", out)
	}
	if strings.Contains(out, "2025-12-13") {
		t.Errorf("default dry run kept more than four weeks: %q", out)
	}

	out, err = captureStdout(t, func() error {
		return cmdProgress([]string{"--reports", dir, "--root", root, "--weeks", "2", "--dry-run", "--claude", filepath.Join(root, "no-claude")})
	})
	if err != nil {
		t.Fatalf("cmdProgress --weeks 2 --dry-run: %v", err)
	}
	if !strings.Contains(out, "2026-01-03") || !strings.Contains(out, "2026-01-10") {
		t.Errorf("dry run missing the last two weeks: %q", out)
	}
	if strings.Contains(out, "2025-12-27") {
		t.Errorf("dry run kept a week beyond --weeks: %q", out)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >/dev/null\necho '{}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	countChildren := func() int {
		self, err := os.Readlink("/proc/self")
		if err != nil {
			t.Fatal(err)
		}
		stats, _ := filepath.Glob("/proc/[0-9]*/stat")
		n := 0
		for _, s := range stats {
			b, err := os.ReadFile(s)
			if err != nil {
				continue
			}
			f := strings.Fields(string(b)[strings.LastIndex(string(b), ")")+1:])
			if len(f) > 1 && f[1] == self {
				n++
			}
		}
		return n
	}
	before := countChildren()
	out, err = captureStdout(t, func() error {
		return cmdProgress([]string{"--reports", dir, "--root", root, "--claude", filepath.Join(bin, "claude")})
	})
	if err != nil {
		t.Fatalf("cmdProgress: %v", err)
	}
	memos, _ := filepath.Glob(filepath.Join(dir, "progress-*.html"))
	if len(memos) != 1 {
		t.Fatalf("want one memo written without --dry-run, got %v", memos)
	}
	if !strings.Contains(out, memos[0]) {
		t.Errorf("memo path not printed: %q", out)
	}
	if after := countChildren(); after != before {
		t.Errorf("memo was opened without --open: %d child processes, want %d", after, before)
	}

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "insights-7d-2026-01-03.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(bad, "insights-7d-2026-01-10.html"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = captureStdout(t, func() error {
		return cmdProgress([]string{"--reports", bad, "--root", root, "--dry-run", "--claude", filepath.Join(root, "no-claude")})
	})
	if err == nil {
		t.Errorf("want an extract error for an unreadable report")
	}
}

func TestCmdProgressDryRunAndMemo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("adopted-rule-xyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, end := range []string{"2026-01-03", "2026-01-10"} {
		if _, err := captureStdout(t, func() error {
			return cmdAggregate([]string{"--root", root, "--days", "7", "--end", end, "--quiet"})
		}); err != nil {
			t.Fatalf("cmdAggregate %s: %v", end, err)
		}
	}
	reports := t.TempDir()
	for _, d := range []string{"2026-01-03", "2026-01-10"} {
		page := "<html><body><h1>Claude Code Insights</h1><p>week " + d + "</p></body></html>"
		if err := os.WriteFile(filepath.Join(reports, "insights-7d-"+d+".html"), []byte(page), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	okBin := filepath.Join(bin, "claude-ok")
	if err := os.WriteFile(okBin, []byte("#!/bin/sh\ncat >/dev/null\necho not json\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	failBin := filepath.Join(bin, "claude-fail")
	if err := os.WriteFile(failBin, []byte("#!/bin/sh\ncat >/dev/null\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep the opener from launching anything real.
	t.Setenv("PATH", bin)
	base := []string{"--reports", reports, "--root", root}

	out, err := captureStdout(t, func() error { return cmdProgress(append(base, "--dry-run")) })
	if err != nil {
		t.Fatalf("cmdProgress --dry-run: %v", err)
	}
	if !strings.Contains(out, "adopted-rule-xyz") {
		t.Errorf("dry run missing CLAUDE.md: %q", out)
	}

	if _, err := captureStdout(t, func() error { return cmdProgress(append(base, "--claude", failBin)) }); err == nil {
		t.Errorf("want error from failing claude")
	}

	out, err = captureStdout(t, func() error { return cmdProgress(append(base, "--claude", okBin, "--open")) })
	if err != nil {
		t.Fatalf("cmdProgress: %v", err)
	}
	memo := strings.TrimSpace(out)
	if filepath.Dir(memo) != reports || !strings.HasPrefix(filepath.Base(memo), "progress-7d-") {
		t.Fatalf("unexpected memo path %q", memo)
	}
	fi, err := os.Stat(memo)
	if err != nil {
		t.Fatalf("memo not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("memo perms = %v, want 0600", fi.Mode().Perm())
	}

	if err := os.Remove(memo); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(memo, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = captureStdout(t, func() error { return cmdProgress(append(base, "--claude", okBin)) })
	if err == nil || !strings.Contains(err.Error(), "writing memo") {
		t.Errorf("want writing memo error, got %v", err)
	}
}

func TestCmdAuthStoresPipedToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString("sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ\n"); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origIn, origErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = in, errW
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(errR)
		done <- string(b)
	}()
	runErr := cmdAuth(nil)
	os.Stdin, os.Stderr = origIn, origErr
	errW.Close()
	in.Close()
	stderr := <-done

	if runErr != nil {
		t.Fatalf("cmdAuth: %v", runErr)
	}
	if !strings.Contains(stderr, "token stored (") || !strings.Contains(stderr, home) {
		t.Errorf("stored location not reported under HOME: %q", stderr)
	}

	out, err := captureStdout(t, func() error { return cmdAuth([]string{"--check"}) })
	if err != nil {
		t.Fatalf("cmdAuth --check after store: %v", err)
	}
	if !strings.Contains(out, "token available") {
		t.Errorf("check output = %q", out)
	}
}

func TestCommonFlagsResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	out, err := captureStdout(t, func() error {
		return cmdFailures([]string{"--root", root, "--days", "14", "--end", "2026-01-10", "--include-scratch"})
	})
	if err != nil {
		t.Fatalf("cmdFailures: %v", err)
	}
	if !strings.Contains(out, "2026-01-10") || !strings.Contains(out, "(14d)") {
		t.Errorf("flags not applied to window label: %q", out)
	}

	cf := commonFlags{days: 14, end: "2026-01-10", root: root, includeScratch: true}
	p, o, err := cf.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Root != root {
		t.Errorf("Root = %q, want %q", p.Root, root)
	}
	if o.Days != 14 || !o.IncludeScratch {
		t.Errorf("options = %+v, want days 14 and include-scratch", o)
	}
	if got := o.End.Format("2006-01-02T15:04:05Z07:00"); got != "2026-01-10T23:59:59Z" {
		t.Errorf("End = %s, want 2026-01-10T23:59:59Z", got)
	}

	bad := commonFlags{root: root, end: "01/10/2026"}
	if _, _, err := bad.resolve(); err == nil || !strings.Contains(err.Error(), "parsing --end") {
		t.Errorf("want --end parse error, got %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	badCfg := commonFlags{root: root, config: cfgPath}
	if _, _, err := badCfg.resolve(); err == nil {
		t.Error("want config load error, got nil")
	}

	t.Setenv("HOME", "")
	noHome := commonFlags{root: root}
	if _, _, err := noHome.resolve(); err == nil {
		t.Error("want default paths error without HOME, got nil")
	}
}

func TestMainDispatch(t *testing.T) {
	if cmd, ok := os.LookupEnv("WEEKLY_INSIGHTS_MAIN_ARGS"); ok {
		os.Args = append([]string{"weekly-insights"}, strings.Fields(cmd)...)
		main()
		os.Exit(0)
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	exits := []struct {
		args string
		code int
		want string
	}{
		{"", 2, "Usage:"},
		{"bogus", 2, `unknown command "bogus"`},
		{"--root " + root + " --days 7", 1, "no substantive sessions"},
		{"run --root " + root, 1, "no substantive sessions"},
		{"progress --weeks 1", 1, "--weeks must be at least 2"},
	}
	for _, tc := range exits {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		proc, err := os.StartProcess(exe, []string{exe, "-test.run=^TestMainDispatch$"}, &os.ProcAttr{
			Env:   append([]string{"WEEKLY_INSIGHTS_MAIN_ARGS=" + tc.args}, os.Environ()...),
			Files: []*os.File{nil, w, w},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.Close()
		out, _ := io.ReadAll(r)
		r.Close()
		st, err := proc.Wait()
		if err != nil {
			t.Fatal(err)
		}
		if st.ExitCode() != tc.code || !strings.Contains(string(out), tc.want) {
			t.Errorf("main %q: exit %d, output %q; want exit %d containing %q", tc.args, st.ExitCode(), out, tc.code, tc.want)
		}
	}

	origArgs := os.Args
	origVersion := version
	t.Cleanup(func() {
		os.Args = origArgs
		version = origVersion
	})
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"prompt"}, ""},
		{[]string{"help"}, "Usage:"},
		{[]string{"failures", "--root", root, "--days", "7", "--end", "2026-01-10"}, "(7d)"},
		{[]string{"select", "--root", root, "--end", "2026-01-10"}, "top projects"},
		{[]string{"aggregate", "--root", root, "--days", "7", "--end", "2026-01-10"}, "{"},
		{[]string{"report", "--root", root}, ""},
		{[]string{"validate", "--root", root, "--dir", t.TempDir()}, "0 facet files, 0 with problems"},
	}
	for _, tc := range cases {
		os.Args = append([]string{"weekly-insights"}, tc.args...)
		out, _ := captureStdout(t, func() error {
			main()
			return nil
		})
		if tc.want == "" && strings.TrimSpace(out) == "" {
			t.Errorf("main %v printed nothing", tc.args)
		}
		if tc.want != "" && !strings.Contains(out, tc.want) {
			t.Errorf("main %v output %q, want %q", tc.args, out, tc.want)
		}
	}

	// A test binary carries no release module version, so an unstamped build
	// must report "dev" rather than an empty or "(devel)" build-info version.
	version = "dev"
	os.Args = []string{"weekly-insights", "version"}
	out, _ := captureStdout(t, func() error {
		main()
		return nil
	})
	if out != "dev\n" {
		t.Errorf("unstamped version output = %q, want %q", out, "dev\n")
	}
	if got := resolvedVersion(); got != "dev" {
		t.Errorf("resolvedVersion() unstamped = %q, want %q", got, "dev")
	}

	version = "v9.9.9"
	out, _ = captureStdout(t, func() error {
		main()
		return nil
	})
	if out != "v9.9.9\n" {
		t.Errorf("stamped version output = %q, want %q", out, "v9.9.9\n")
	}
	if got := resolvedVersion(); got != "v9.9.9" {
		t.Errorf("resolvedVersion() stamped = %q, want %q", got, "v9.9.9")
	}

	dir := filepath.Join(t.TempDir(), "rendered")
	os.Args = []string{"weekly-insights", "prepare", "--root", root, "--end", "2026-01-10", "--out", dir}
	main()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("prepare via main did not create out dir: %v", err)
	}
}
