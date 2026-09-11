// Package auth finds and stores the long-lived token the staged child needs.
//
// A staged config directory cannot see the login stored for the real one, so
// the child authenticates with a token from `claude setup-token`. Where that
// token lives is a per-machine concern, never a per-checkout one: a file inside
// a git working tree is one careless add away from being committed, and the
// binary is run from arbitrary directories anyway. So the fallback file sits in
// the platform's user config directory, and the repository's .gitignore only
// exists as a backstop.
package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvVar is the variable Claude Code itself reads for headless auth.
const EnvVar = "CLAUDE_CODE_OAUTH_TOKEN"

// KeychainService names the macOS keychain item.
const KeychainService = "weekly-insights"

// Source says where a token came from, so status output can be useful without
// ever printing the token.
type Source string

const (
	SourceNone     Source = ""
	SourceEnv      Source = "environment"
	SourceKeychain Source = "keychain"
	SourceFile     Source = "file"
)

// keychainEnabled is a variable so tests can force the file path on macOS.
var keychainEnabled = runtime.GOOS == "darwin"

// Path is the fallback token file: ~/Library/Application Support on macOS,
// $XDG_CONFIG_HOME or ~/.config on Linux, %AppData% on Windows.
func Path() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	return filepath.Join(d, "weekly-insights", "token"), nil
}

// Resolve returns the token and where it was found. Precedence is environment,
// then keychain, then file, so an operator can always override a stored value
// for one run without touching storage.
func Resolve() (string, Source) {
	if t := strings.TrimSpace(os.Getenv(EnvVar)); t != "" {
		return t, SourceEnv
	}
	if keychainEnabled {
		if t := fromKeychain(); t != "" {
			return t, SourceKeychain
		}
	}
	p, err := Path()
	if err != nil {
		return "", SourceNone
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", SourceNone
	}
	if t := strings.TrimSpace(string(b)); t != "" {
		return t, SourceFile
	}
	return "", SourceNone
}

// Store saves the token in the platform's preferred place and reports which.
func Store(token string) (Source, error) {
	token = strings.TrimSpace(token)
	if err := validate(token); err != nil {
		return SourceNone, err
	}
	if keychainEnabled {
		if err := toKeychain(token); err == nil {
			return SourceKeychain, nil
		}
		// Fall through to the file when the keychain is unavailable (for
		// example a headless session with no login keychain).
	}
	p, err := Path()
	if err != nil {
		return SourceNone, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return SourceNone, fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		return SourceNone, fmt.Errorf("writing token file: %w", err)
	}
	// WriteFile does not tighten the mode of a pre-existing file.
	if err := os.Chmod(p, 0o600); err != nil {
		return SourceNone, fmt.Errorf("securing token file: %w", err)
	}
	return SourceFile, nil
}

// Clear removes any stored token from keychain and file. The environment is
// the caller's to manage.
func Clear() error {
	var errs []error
	if keychainEnabled {
		// A missing item is not an error for a clear.
		_ = exec.Command("security", "delete-generic-password", "-s", KeychainService).Run()
	}
	if p, err := Path(); err == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validate(token string) error {
	switch {
	case token == "":
		return errors.New("token is empty")
	case strings.ContainsAny(token, " \t\r\n"):
		return errors.New("token contains whitespace; paste only the token itself")
	case len(token) < 20:
		return errors.New("token is too short to be a Claude Code token")
	}
	return nil
}

func fromKeychain() string {
	out, err := exec.Command("security", "find-generic-password", "-s", KeychainService, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func toKeychain(token string) error {
	acct := "weekly-insights"
	if u, err := user.Current(); err == nil && u.Username != "" {
		acct = u.Username
	}
	// -U updates an existing item instead of failing on it. The token travels
	// in argv, the same way the `security` manual page has users enter it.
	cmd := exec.Command("security", "add-generic-password", "-a", acct, "-s", KeychainService, "-w", token, "-U")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security add-generic-password: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
