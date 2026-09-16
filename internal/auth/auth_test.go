package auth

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolate points the user config dir at a temp directory and disables the
// keychain, so tests never read or write the developer's real token.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", filepath.Join(home, "AppData"))
	}
	t.Setenv(EnvVar, "")
	old := keychainEnabled
	keychainEnabled = false
	t.Cleanup(func() { keychainEnabled = old })
	return home
}

const sample = "test-token-xxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

func TestResolvePrefersEnvironment(t *testing.T) {
	isolate(t)
	if _, err := Store(sample); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, "from-env-0123456789abcdef")
	tok, src := Resolve()
	if tok != "from-env-0123456789abcdef" || src != SourceEnv {
		t.Errorf("env must win: got %q from %q", tok, src)
	}
}

func TestStoreAndResolveFile(t *testing.T) {
	home := isolate(t)
	src, err := Store("  " + sample + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Errorf("without a keychain the token must go to the file, got %q", src)
	}
	p, _ := Path()
	if !strings.HasPrefix(p, home) {
		t.Errorf("token path %s escaped the isolated home %s", p, home)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %o, want 0600", fi.Mode().Perm())
	}
	tok, from := Resolve()
	if tok != sample || from != SourceFile {
		t.Errorf("Resolve = %q from %q; want stored token from file", tok, from)
	}
}

func TestStoreTightensExistingMode(t *testing.T) {
	isolate(t)
	p, _ := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Store(sample); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("pre-existing world-readable file was left at %o", fi.Mode().Perm())
	}
}

func TestStoreRejectsBadTokens(t *testing.T) {
	isolate(t)
	for _, bad := range []string{"", "short", "has space in it 0123456789"} {
		if _, err := Store(bad); err == nil {
			t.Errorf("Store(%q) should fail", bad)
		}
	}
}

func TestResolveNothingStored(t *testing.T) {
	isolate(t)
	if tok, src := Resolve(); tok != "" || src != SourceNone {
		t.Errorf("expected nothing, got %q from %q", tok, src)
	}
}

func TestClearRemovesFile(t *testing.T) {
	isolate(t)
	if _, err := Store(sample); err != nil {
		t.Fatal(err)
	}
	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if tok, _ := Resolve(); tok != "" {
		t.Error("token still resolvable after Clear")
	}
}

func TestPathIsOutsideAnyRepoCheckout(t *testing.T) {
	// The path is derived from the user config dir, never the working
	// directory, so it cannot land inside a git checkout by accident.
	isolate(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if strings.HasPrefix(p, wd) {
		t.Errorf("token path %s is under the working directory %s", p, wd)
	}
}

// securityCall records one call to the fake `security`, including its stdin,
// so a test can assert where the token travelled.
type securityCall struct {
	stdin string
	args  []string
}

// stubKeychain turns the keychain path on and routes it at a fake `security`,
// so the macOS branches are testable on any platform and never touch a real
// keychain.
func stubKeychain(t *testing.T, fn func(stdin string, args ...string) ([]byte, error)) *[]securityCall {
	t.Helper()
	var calls []securityCall
	oldEnabled, oldRun := keychainEnabled, runSecurity
	keychainEnabled = true
	runSecurity = func(stdin string, args ...string) ([]byte, error) {
		calls = append(calls, securityCall{stdin: stdin, args: args})
		return fn(stdin, args...)
	}
	t.Cleanup(func() { keychainEnabled, runSecurity = oldEnabled, oldRun })
	return &calls
}

func TestResolveUsesKeychain(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		if args[0] != "find-generic-password" {
			t.Errorf("unexpected security call %v", args)
		}
		return []byte(sample + "\n"), nil
	})
	tok, src := Resolve()
	if tok != sample || src != SourceKeychain {
		t.Errorf("Resolve = %q from %q; want the keychain token", tok, src)
	}
}

func TestStoreUsesKeychain(t *testing.T) {
	isolate(t)
	calls := stubKeychain(t, func(stdin string, args ...string) ([]byte, error) {
		// Interactive mode stores, then the read-back verifies; both go
		// through this stub, so the second call has to answer with the token.
		if len(args) > 0 && args[0] == "find-generic-password" {
			return []byte(sample + "\n"), nil
		}
		return nil, nil
	})
	src, err := Store(sample)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceKeychain {
		t.Errorf("Store reported %q, want %q", src, SourceKeychain)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected a store and a read-back, got %d security calls", len(*calls))
	}
	store := (*calls)[0]
	if len(store.args) != 1 || store.args[0] != "-i" {
		t.Errorf("called security %v, want interactive mode", store.args)
	}
	for _, a := range store.args {
		if strings.Contains(a, sample) {
			t.Fatalf("the token appeared in argv %v; it must only travel on stdin", store.args)
		}
	}
	if !strings.Contains(store.stdin, "add-generic-password") {
		t.Errorf("stdin %q does not carry add-generic-password", store.stdin)
	}
	if !strings.Contains(store.stdin, "-s "+KeychainService) {
		t.Errorf("service flag missing from stdin %q", store.stdin)
	}
	if !strings.Contains(store.stdin, "-w "+sample) {
		t.Errorf("token missing from stdin %q", store.stdin)
	}
	if !strings.Contains(store.stdin, "-U") {
		t.Errorf("-U missing from stdin %q; an existing item would not be updated", store.stdin)
	}
	p, _ := Path()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("keychain store should not have written the fallback file %s", p)
	}
}

// `security -i` exits 0 even when the command it read failed, so a store that
// did not land has to be caught by reading the item back.
func TestStoreFallsBackWhenKeychainSilentlyDropsTheToken(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(stdin string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "find-generic-password" {
			return []byte("something-else\n"), nil
		}
		return []byte("add-generic-password: error"), nil
	})
	src, err := Store(sample)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Errorf("Store reported %q; a keychain that did not keep the token must fall back to the file", src)
	}
}

// A token outside the character set interactive mode can carry unchanged is
// stored in the file rather than risking a mangled keychain item.
func TestStoreFallsBackForATokenTheKeychainCannotCarry(t *testing.T) {
	isolate(t)
	calls := stubKeychain(t, func(stdin string, args ...string) ([]byte, error) {
		return nil, nil
	})
	src, err := Store(`sk-ant-oat01-"quoted-token-value`)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Errorf("Store reported %q, want the file fallback", src)
	}
	if len(*calls) != 0 {
		t.Errorf("security was called %d times; a token it cannot carry must not reach it", len(*calls))
	}
}

func TestStoreFallsBackToFileWhenKeychainFails(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return []byte("no login keychain"), errors.New("exit status 1")
	})
	src, err := Store(sample)
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceFile {
		t.Errorf("Store reported %q, want %q when the keychain fails", src, SourceFile)
	}
	p, _ := Path()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != sample {
		t.Errorf("fallback file holds %q", strings.TrimSpace(string(b)))
	}
}

func TestClearIgnoresMissingKeychainItem(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return []byte("security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."),
			errors.New("exit status 44")
	})
	if err := Clear(); err != nil {
		t.Errorf("Clear should ignore a missing keychain item, got %v", err)
	}
}

func TestClearIgnoresNotFoundInErrorText(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return nil, errors.New("The specified item could not be found in the keychain.")
	})
	if err := Clear(); err != nil {
		t.Errorf("Clear should ignore a not-found error, got %v", err)
	}
}

func TestClearPropagatesKeychainError(t *testing.T) {
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return []byte("User interaction is not allowed."), errors.New("exit status 36")
	})
	err := Clear()
	if err == nil {
		t.Fatal("Clear must not report success when the keychain delete fails")
	}
	if !strings.Contains(err.Error(), "delete-generic-password") {
		t.Errorf("error lacks context: %v", err)
	}
}

func TestPromptTokenReadsPipedInput(t *testing.T) {
	var out strings.Builder
	tok, err := PromptToken(strings.NewReader("  "+sample+"  \nleftover\n"), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if tok != sample {
		t.Errorf("PromptToken = %q, want %q", tok, sample)
	}
	if !strings.Contains(out.String(), "claude setup-token") {
		t.Errorf("instructions missing from prompt output %q", out.String())
	}
	if !strings.Contains(out.String(), "Token: ") {
		t.Errorf("prompt missing from output %q", out.String())
	}
}

func TestPromptTokenAcceptsUnterminatedLine(t *testing.T) {
	var out strings.Builder
	tok, err := PromptToken(strings.NewReader(sample), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	if tok != sample {
		t.Errorf("PromptToken = %q, want %q", tok, sample)
	}
}

func TestPromptTokenEmptyInputErrors(t *testing.T) {
	var out strings.Builder
	tok, err := PromptToken(strings.NewReader(""), &out, false)
	if err == nil {
		t.Fatal("expected an error on empty input")
	}
	if tok != "" {
		t.Errorf("PromptToken returned %q on error", tok)
	}
	if strings.Contains(err.Error(), sample) {
		t.Errorf("error text leaked the token: %v", err)
	}
}

func TestClearIgnoresExitCode44(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to produce a real exit status")
	}
	isolate(t)
	// A real *exec.ExitError, so the exit-code branch is covered rather than
	// only the message substring.
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return exec.Command("sh", "-c", "exit 44").CombinedOutput()
	})
	if err := Clear(); err != nil {
		t.Errorf("Clear should ignore exit status 44, got %v", err)
	}
}

func TestClearPropagatesOtherExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh to produce a real exit status")
	}
	isolate(t)
	stubKeychain(t, func(_ string, args ...string) ([]byte, error) {
		return exec.Command("sh", "-c", "exit 36").CombinedOutput()
	})
	if err := Clear(); err == nil {
		t.Error("Clear must propagate a non-not-found exit status")
	}
}

func TestStoreReportsFileErrors(t *testing.T) {
	home := isolate(t)
	p, _ := Path()

	// A directory where the token file should be.
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := Store(sample)
	if err == nil || src != SourceNone || !strings.Contains(err.Error(), "writing token file") {
		t.Errorf("Store with a directory at the token path = %q, %v; want a write error", src, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "linux" {
		// procfs accepts the write but refuses a mode change, even for root.
		old, err := os.ReadFile("/proc/self/comm")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile("/proc/self/comm", old, 0o600) })
		if err := os.Symlink("/proc/self/comm", p); err != nil {
			t.Fatal(err)
		}
		src, err = Store(sample)
		if err == nil || src != SourceNone || !strings.Contains(err.Error(), "securing token file") {
			t.Errorf("Store with an unchmoddable token file = %q, %v; want a securing error", src, err)
		}
	}

	// No way to locate the user config dir at all.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	src, err = Store(sample)
	if err == nil || src != SourceNone {
		t.Errorf("Store without a config dir = %q, %v; want an error", src, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "weekly-insights")); !os.IsNotExist(statErr) {
		t.Errorf("Store wrote a token despite failing to locate the config dir")
	}
}

func TestPromptTokenDisablesAndRestoresEcho(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "stty.log")
	script := "#!/bin/sh\necho \"$1\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out strings.Builder
	tok, err := PromptToken(strings.NewReader(sample+"\n"), &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if tok != sample {
		t.Errorf("PromptToken = %q, want %q", tok, sample)
	}
	if strings.Contains(out.String(), "Could not disable terminal echo") {
		t.Errorf("unexpected echo warning in %q", out.String())
	}
	if !strings.HasSuffix(out.String(), "Token: \n") {
		t.Errorf("expected a newline after the silent read, got %q", out.String())
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "-echo\necho\n" {
		t.Errorf("stty calls = %q, want echo disabled then restored", string(b))
	}
}

func TestPromptTokenWarnsWhenEchoCannotBeDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stty"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out strings.Builder
	tok, err := PromptToken(strings.NewReader(sample+"\n"), &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if tok != sample {
		t.Errorf("PromptToken = %q, want %q", tok, sample)
	}
	if !strings.Contains(out.String(), "Could not disable terminal echo") {
		t.Errorf("echo warning missing from %q", out.String())
	}
	if strings.HasSuffix(out.String(), "Token: \n") {
		t.Errorf("no extra newline expected when echo stayed on, got %q", out.String())
	}
}

func TestRunSecurityInvokesSecurityOnPath(t *testing.T) {
	dir := t.TempDir()
	// The stand-in echoes its arguments and then whatever it was handed on
	// stdin, so the test can tell the two apart.
	script := "#!/bin/sh\necho \"args: $@\"\nwhile IFS= read -r line; do echo \"stdin: $line\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	out, err := runSecurity("", "find-generic-password", "-s", KeychainService, "-w")
	if err != nil {
		t.Fatalf("runSecurity: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "args: find-generic-password -s "+KeychainService+" -w"; got != want {
		t.Errorf("security received %q, want %q", got, want)
	}

	out, err = runSecurity("add-generic-password -w "+sample+"\n", "-i")
	if err != nil {
		t.Fatalf("runSecurity: %v: %s", err, out)
	}
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if lines[0] != "args: -i" {
		t.Errorf("security argv was %q; the token must not be in it", lines[0])
	}
	if len(lines) < 2 || !strings.Contains(lines[1], "stdin: ") || !strings.Contains(lines[1], sample) {
		t.Errorf("security stdin was %q, want the token", out)
	}
}

func TestResolveWithoutConfigDir(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if _, err := Path(); err == nil {
		t.Fatal("expected Path to fail with no config dir")
	}
	if tok, src := Resolve(); tok != "" || src != SourceNone {
		t.Errorf("Resolve = %q from %q; want nothing when the config dir is unknown", tok, src)
	}
}

func TestResolveIgnoresBlankFile(t *testing.T) {
	isolate(t)
	p, _ := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, src := Resolve(); tok != "" || src != SourceNone {
		t.Errorf("blank token file resolved to %q from %q", tok, src)
	}
}
