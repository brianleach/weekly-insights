package auth

import (
	"os"
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

const sample = "sk-ant-oat01-test-token-value-0123456789"

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
