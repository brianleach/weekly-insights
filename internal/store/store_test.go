package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty path returns defaults", func(t *testing.T) {
		c, err := LoadConfig("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(c, DefaultConfig()) {
			t.Fatalf("got %+v, want defaults", c)
		}
	})

	t.Run("missing file returns defaults", func(t *testing.T) {
		c, err := LoadConfig(filepath.Join(dir, "absent.json"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(c, DefaultConfig()) {
			t.Fatalf("got %+v, want defaults", c)
		}
	})

	t.Run("unreadable path is an error", func(t *testing.T) {
		c, err := LoadConfig(dir)
		if err == nil {
			t.Fatal("expected error reading a directory")
		}
		if c.ExcludeProjectGlobs != nil {
			t.Fatalf("expected empty config, got %+v", c)
		}
	})

	t.Run("invalid json is an error", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected parse error")
		}
		if c.ExcludeProjectGlobs != nil {
			t.Fatalf("expected empty config, got %+v", c)
		}
	})

	t.Run("valid file is parsed", func(t *testing.T) {
		path := filepath.Join(dir, "good.json")
		if err := os.WriteFile(path, []byte(`{"exclude_project_globs":["/work/*"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"/work/*"}
		if !reflect.DeepEqual(c.ExcludeProjectGlobs, want) {
			t.Fatalf("got %v, want %v", c.ExcludeProjectGlobs, want)
		}
	})
}

func TestDefaultPaths(t *testing.T) {
	t.Run("derives paths from home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		p, err := DefaultPaths()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := Paths{
			Root:       filepath.Join(home, ".claude", "usage-data"),
			ClaudeHome: filepath.Join(home, ".claude"),
		}
		if !reflect.DeepEqual(p, want) {
			t.Fatalf("got %+v, want %+v", p, want)
		}
	})

	t.Run("missing home is an error", func(t *testing.T) {
		t.Setenv("HOME", "")
		p, err := DefaultPaths()
		if err == nil {
			t.Fatal("expected error when HOME is unset")
		}
		if !reflect.DeepEqual(p, Paths{}) {
			t.Fatalf("expected empty paths, got %+v", p)
		}
	})
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*/.git", "/home/u/repo/.git", true},
		{"*/.git", "/home/u/repo", false},
		{"/home/*/repo", "/home/u/repo", true},
		{"/home/*/repo", "/home/u/other", false},
		{"/home/*/repo", "/home/a/b/repo", false},
		{"/home/*/[", "/home/u/[", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.s); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestLoadAllMetaSkipsUnusableFiles(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	dir := p.SessionMeta()
	if err := os.MkdirAll(filepath.Join(dir, "unreadable.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"bad.json":   "{not json",
		"noid.json":  "{}",
		"valid.json": `{"session_id":"abc"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, err := p.LoadAllMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].SessionID != "abc" {
		t.Fatalf("got %+v, want only session abc", out)
	}
}

func TestWriteJSONErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("parent that is a file is an error", func(t *testing.T) {
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := WriteJSON(filepath.Join(blocker, "sub", "out.json"), map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error creating directory under a file")
		}
		prefix := "creating directory for "
		if msg := err.Error(); len(msg) < len(prefix) || msg[:len(prefix)] != prefix {
			t.Fatalf("got %q, want prefix %q", msg, prefix)
		}
	})

	t.Run("unencodable value is an error", func(t *testing.T) {
		path := filepath.Join(dir, "chan.json")
		err := WriteJSON(path, make(chan int))
		if err == nil {
			t.Fatal("expected encoding error")
		}
		prefix := "encoding "
		if msg := err.Error(); len(msg) < len(prefix) || msg[:len(prefix)] != prefix {
			t.Fatalf("got %q, want prefix %q", msg, prefix)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("expected no file written, stat err: %v", statErr)
		}
	})
}

func TestWriteJSONFailuresLeaveNothingBehind(t *testing.T) {
	t.Run("a destination that cannot be replaced is an error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sub")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := WriteJSON(path, map[string]int{"a": 1}); err == nil {
			t.Fatal("expected an error writing over a directory")
		}
		// The temporary file the write went through must not survive the
		// failure: it holds the same session data the destination would.
		leftovers, _ := filepath.Glob(filepath.Join(dir, ".sub.tmp*"))
		if len(leftovers) != 0 {
			t.Errorf("failed write left %v behind", leftovers)
		}
	})

	t.Run("a pre-existing loose mode is replaced, not written through", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "snapshot.json")
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("snapshot mode = %04o, want 0600", got)
		}
	})

	t.Run("a symlink at the destination is replaced, not followed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "snapshot.json")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(target); string(b) != "do not touch" {
			t.Errorf("the symlink target became %q; it must be left alone", b)
		}
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			t.Error("the destination is still a symlink; the write followed it")
		}
	})
}

func TestExcludedIgnoresEmptyGlob(t *testing.T) {
	c := Config{ExcludeProjectGlobs: []string{""}}
	if c.Excluded("") {
		t.Fatal("empty glob should never match, even an empty project path")
	}
}

func TestMatchGlobExactPattern(t *testing.T) {
	c := Config{ExcludeProjectGlobs: []string{"/work/app"}}
	if !c.Excluded("/work/app") {
		t.Fatal("expected exact path to be excluded")
	}
	if c.Excluded("/work/app2") {
		t.Fatal("expected different path not to be excluded")
	}
}

func TestValidSessionID(t *testing.T) {
	for _, id := range []string{"0047a6a4-d1fc-434d-947a-7880d24d3755", "abc123", "a_b.c-d"} {
		if !ValidSessionID(id) {
			t.Errorf("ValidSessionID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"", ".", "..", "../../../.claude", "a/b", `a\b`, "*", "sess*", "sess[0-9]",
		".hidden", "with space", "with\nnewline", strings.Repeat("a", 129),
	} {
		if ValidSessionID(id) {
			t.Errorf("ValidSessionID(%q) = true, want false", id)
		}
	}
}

// A tampered or corrupt cache record must not be able to steer a later path
// join out of the directory the caller named.
func TestLoadAllMetaDropsUnusableSessionIDs(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Root: dir, ClaudeHome: dir}
	if err := os.MkdirAll(p.SessionMeta(), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, id string) {
		body := `{"session_id":"` + id + `","user_message_count":5,"duration_minutes":10}`
		if err := os.WriteFile(filepath.Join(p.SessionMeta(), name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("good.json", "good-session-id")
	write("bad.json", "../../../.claude")

	metas, err := p.LoadAllMeta()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].SessionID != "good-session-id" {
		t.Fatalf("LoadAllMeta = %+v, want only the usable record", metas)
	}
}

func TestLookupsRefuseUnusableSessionIDs(t *testing.T) {
	p := Paths{Root: t.TempDir(), ClaudeHome: t.TempDir()}
	if f, src := p.LoadFacets("../../../.claude"); f != nil || src != "" {
		t.Errorf("LoadFacets returned %v/%q for a traversal id", f, src)
	}
	if got := p.TranscriptPath("*"); got != "" {
		t.Errorf("TranscriptPath(%q) = %q; a glob must not match anything", "*", got)
	}
}

func TestPathsLayout(t *testing.T) {
	p := Paths{Root: "/synthetic/usage", ClaudeHome: "/synthetic/home"}
	cases := []struct {
		name, got, want string
	}{
		{"SessionMeta", p.SessionMeta(), "/synthetic/usage/session-meta"},
		{"BuiltinFacets", p.BuiltinFacets(), "/synthetic/usage/facets"},
		{"WeeklyFacets", p.WeeklyFacets(), "/synthetic/usage/weekly-facets"},
		{"Snapshots", p.Snapshots(), "/synthetic/usage/weekly"},
		{"Transcripts", p.Transcripts(), "/synthetic/home/projects"},
	}
	for _, tc := range cases {
		if tc.got != filepath.FromSlash(tc.want) {
			t.Errorf("%s() = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	// The builtin's and our own facets must never share a directory, or a
	// write of ours would land in the builtin's cache.
	if p.BuiltinFacets() == p.WeeklyFacets() {
		t.Error("builtin and weekly facets resolve to the same directory")
	}
}

func TestMatchGlobSurroundingStars(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*/scratchpad/*", "/home/u/scratchpad/run1", true},
		{"*/scratchpad/*", "/home/u/notes", false},
		{"/tmp/*", "/tmp/x", true},
		{"/tmp/*", "/var/tmp/x", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.s); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestLoadAllMetaBadRootPatternIsAnError(t *testing.T) {
	// An unclosed "[" in the root makes the glob pattern malformed, which
	// filepath.Glob reports before touching the disk.
	p := Paths{Root: filepath.Join(t.TempDir(), "[")}
	if _, err := p.LoadAllMeta(); err == nil {
		t.Fatal("expected an error for a malformed glob pattern")
	}
}

func TestLoadFacets(t *testing.T) {
	const id = "sess-1"
	type file struct{ dir, body string }
	cases := []struct {
		name     string
		files    []file
		dirs     []string // paths under Root created as directories
		wantGoal string
		wantSrc  string
	}{
		{
			name:     "canonical is preferred over builtin",
			files:    []file{{"weekly-facets", `{"underlying_goal":"ours"}`}, {"facets", `{"underlying_goal":"theirs"}`}},
			wantGoal: "ours",
			wantSrc:  SourceCanonical,
		},
		{
			name:     "builtin is used when canonical is absent",
			files:    []file{{"facets", `{"underlying_goal":"theirs"}`}},
			wantGoal: "theirs",
			wantSrc:  SourceBuiltin,
		},
		{
			name:     "corrupt canonical falls back to builtin",
			files:    []file{{"weekly-facets", "{not json"}, {"facets", `{"underlying_goal":"theirs"}`}},
			wantGoal: "theirs",
			wantSrc:  SourceBuiltin,
		},
		{
			name:     "unreadable canonical falls back to builtin",
			files:    []file{{"facets", `{"underlying_goal":"theirs"}`}},
			dirs:     []string{filepath.Join("weekly-facets", id+".json")},
			wantGoal: "theirs",
			wantSrc:  SourceBuiltin,
		},
		{
			name:  "corrupt everywhere is nothing",
			files: []file{{"weekly-facets", "{"}, {"facets", "["}},
		},
		{
			name: "neither exists",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Paths{Root: t.TempDir()}
			for _, d := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(p.Root, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.files {
				dir := filepath.Join(p.Root, f.dir)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(f.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f, src := p.LoadFacets(id)
			if src != tc.wantSrc {
				t.Errorf("source = %q, want %q", src, tc.wantSrc)
			}
			if tc.wantSrc == "" {
				if f != nil {
					t.Errorf("facets = %+v, want nil", f)
				}
				return
			}
			if f == nil || f.UnderlyingGoal != tc.wantGoal {
				t.Errorf("facets = %+v, want goal %q", f, tc.wantGoal)
			}
		})
	}
}

func TestTranscriptPath(t *testing.T) {
	const id = "sess-2"
	cases := []struct {
		name  string
		home  func(t *testing.T) string
		files []string // relative to the projects directory
		want  string   // relative to the projects directory, "" for none
	}{
		{
			name:  "found under its project directory",
			home:  func(t *testing.T) string { return t.TempDir() },
			files: []string{"-work-app/" + id + ".jsonl", "-work-app/other.jsonl"},
			want:  "-work-app/" + id + ".jsonl",
		},
		{
			name:  "not found",
			home:  func(t *testing.T) string { return t.TempDir() },
			files: []string{"-work-app/other.jsonl"},
		},
		{
			name:  "a transcript at the top level is not a match",
			home:  func(t *testing.T) string { return t.TempDir() },
			files: []string{id + ".jsonl"},
		},
		{
			name: "malformed home pattern finds nothing",
			home: func(t *testing.T) string { return filepath.Join(t.TempDir(), "[") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Paths{ClaudeHome: tc.home(t)}
			for _, f := range tc.files {
				path := filepath.Join(p.Transcripts(), filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := ""
			if tc.want != "" {
				want = filepath.Join(p.Transcripts(), filepath.FromSlash(tc.want))
			}
			if got := p.TranscriptPath(id); got != want {
				t.Errorf("TranscriptPath = %q, want %q", got, want)
			}
		})
	}
}
