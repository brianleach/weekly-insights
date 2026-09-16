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
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brianleach/weekly-insights/internal/safeio"
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

// runSecurity runs the macOS `security` CLI with stdin attached. It is a
// variable so tests can exercise the keychain paths without touching the
// developer's real keychain.
var runSecurity = func(stdin string, args ...string) ([]byte, error) {
	cmd := exec.Command("security", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

// keychainNotFoundExit is the exit status `security` uses for a missing item.
const keychainNotFoundExit = 44

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
	// safeio, so a token file left behind at a looser mode is replaced rather
	// than written through: the token is never on disk readable by anyone else,
	// not even for the moment between a write and a chmod.
	if err := safeio.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		return SourceNone, fmt.Errorf("writing token file: %w", err)
	}
	return SourceFile, nil
}

// Clear removes any stored token from keychain and file. The environment is
// the caller's to manage. A missing item is not a failure, but any other
// keychain or filesystem error is reported rather than swallowed, so that
// `auth --clear` cannot claim success while a token is still stored.
func Clear() error {
	var errs []error
	if keychainEnabled {
		if out, err := runSecurity("", "delete-generic-password", "-s", KeychainService); err != nil && !isKeychainNotFound(err, out) {
			errs = append(errs, fmt.Errorf("security delete-generic-password: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	p, err := Path()
	if err != nil {
		errs = append(errs, err)
	} else if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("removing %s: %w", p, err))
	}
	return errors.Join(errs...)
}

// isKeychainNotFound reports whether a `security` failure just means the item
// was already absent. macOS exits 44 and prints "The specified item could not
// be found in the keychain."; both are checked because the exit status is not
// documented as stable.
func isKeychainNotFound(err error, out []byte) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == keychainNotFoundExit {
		return true
	}
	if strings.Contains(string(out), "could not be found") {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "could not be found")
}

// PromptToken asks the operator for a token on out and reads one line from in.
// When isTerminal is true on a non-Windows platform the terminal echo is
// disabled for the duration of the read, so the token never appears on screen.
// The token is never included in a returned error.
func PromptToken(in io.Reader, out io.Writer, isTerminal bool) (string, error) {
	fmt.Fprintln(out, `Run "claude setup-token" in another terminal, then paste the token it prints.`)

	echoOff := false
	if isTerminal && runtime.GOOS != "windows" {
		if err := setEcho(false); err != nil {
			fmt.Fprintln(out, "Could not disable terminal echo; the token will be visible as you type.")
		} else {
			echoOff = true
			defer func() {
				_ = setEcho(true)
			}()
		}
	}

	fmt.Fprint(out, "Token: ")
	line, err := readLine(in)
	if echoOff {
		// The Return keypress was not echoed, so move the cursor down.
		fmt.Fprintln(out)
	}
	if err != nil {
		return "", fmt.Errorf("reading token: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// setEcho toggles terminal echo with stty, which needs the real terminal on
// its stdin rather than whatever reader the caller passed.
func setEcho(on bool) error {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// readLine reads a single line one byte at a time, so a caller sharing the
// reader (a piped stdin, say) keeps whatever follows the token.
func readLine(in io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
	}
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
	out, err := runSecurity("", "find-generic-password", "-s", KeychainService, "-w")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// toKeychain writes the token to the login keychain.
//
// The token is fed to `security` on stdin, using its interactive mode, rather
// than passed as an argument. A long-lived credential in argv is readable by
// anything that can list processes for the duration of the call and is picked
// up by process accounting, and neither is worth doing to a token that unlocks
// the user's subscription.
//
// Interactive mode splits the command line it reads, so the account name and
// the token are checked against a conservative character set first and the
// keychain is skipped, in favour of the fallback file, for anything outside it.
// It also does not report the inner command's failure in its exit status, so
// the stored item is read back and compared before this reports success.
func toKeychain(token string) error {
	if !keychainSafe(token) {
		return errors.New("token contains characters that cannot be passed to the keychain safely")
	}
	acct := "weekly-insights"
	if u, err := user.Current(); err == nil && keychainSafe(u.Username) {
		acct = u.Username
	}
	// -U updates an existing item instead of failing on it.
	cmd := fmt.Sprintf("add-generic-password -a %s -s %s -w %s -U\n", acct, KeychainService, token)
	out, err := runSecurity(cmd, "-i")
	if err != nil {
		return fmt.Errorf("security add-generic-password: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if fromKeychain() != token {
		return fmt.Errorf("the keychain did not store the token: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// keychainSafe reports whether a value can cross `security -i` unchanged. The
// set covers what a Claude Code token and an ordinary account name are made of;
// quoting, escaping and whitespace are all excluded rather than handled.
func keychainSafe(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '+', r == '=', r == '/', r == '~':
		default:
			return false
		}
	}
	return true
}
