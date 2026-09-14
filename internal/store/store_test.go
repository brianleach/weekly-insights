package store

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestWriteJSONChmodAndWriteFailures(t *testing.T) {
	t.Run("chmod failure on existing path is an error", func(t *testing.T) {
		// procfs refuses mode changes for every caller, including root.
		err := WriteJSON("/proc/self/status", map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error tightening permissions")
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			t.Fatalf("expected wrapped error, got %v", err)
		}
		pe, ok := u.Unwrap().(*os.PathError)
		if !ok || pe.Op != "chmod" {
			t.Fatalf("expected chmod path error, got %v", err)
		}
	})

	t.Run("write failure is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sub")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		err := WriteJSON(path, map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error writing to a directory")
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			t.Fatalf("expected wrapped error, got %v", err)
		}
		pe, ok := u.Unwrap().(*os.PathError)
		if !ok || pe.Op != "open" {
			t.Fatalf("expected open path error, got %v", err)
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
