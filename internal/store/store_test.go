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

	t.Run("unreadable path returns error", func(t *testing.T) {
		c, err := LoadConfig(dir)
		if err == nil {
			t.Fatal("expected error reading a directory")
		}
		if c.ExcludeProjectGlobs != nil {
			t.Fatalf("got %+v, want empty config", c)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected parse error")
		}
		if c.ExcludeProjectGlobs != nil {
			t.Fatalf("got %+v, want empty config", c)
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
		want := Config{ExcludeProjectGlobs: []string{"/work/*"}}
		if !reflect.DeepEqual(c, want) {
			t.Fatalf("got %+v, want %+v", c, want)
		}
	})
}

func TestDefaultPaths(t *testing.T) {
	t.Run("derives paths from home directory", func(t *testing.T) {
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

	t.Run("unset home returns error", func(t *testing.T) {
		t.Setenv("HOME", "")
		p, err := DefaultPaths()
		if err == nil {
			t.Fatal("expected error when home is unset")
		}
		if !reflect.DeepEqual(p, Paths{}) {
			t.Fatalf("got %+v, want empty paths", p)
		}
	})
}

func TestMatchGlobSuffixAndMiddleStar(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*/scratch", "/home/me/scratch", true},
		{"*/scratch", "/home/me/scratch/x", false},
		{"/work/*/src", "/work/app/src", true},
		{"/work/*/src", "/work/app/lib", false},
		{"/work/*/src", "/work/a/b/src", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.s); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

func TestLoadAllMetaSkipsUnreadableAndInvalidFiles(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	metaDir := p.SessionMeta()
	if err := os.MkdirAll(filepath.Join(metaDir, "unreadable.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "noid.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := p.LoadAllMeta()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d records, want 0: %+v", len(out), out)
	}
}

func TestWriteJSONErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("parent is a file returns directory error", func(t *testing.T) {
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := WriteJSON(filepath.Join(blocker, "out.json"), map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error creating directory under a file")
		}
		w, ok := err.(interface{ Unwrap() error })
		if !ok {
			t.Fatalf("error %v does not wrap a cause", err)
		}
		pe, ok := w.Unwrap().(*os.PathError)
		if !ok || pe.Op != "mkdir" {
			t.Fatalf("got cause %v, want mkdir path error", w.Unwrap())
		}
	})

	t.Run("unencodable value returns error and writes nothing", func(t *testing.T) {
		path := filepath.Join(dir, "chan.json")
		if err := WriteJSON(path, make(chan int)); err == nil {
			t.Fatal("expected encoding error")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected no file written, stat err: %v", err)
		}
	})
}

func TestWriteJSONChmodAndWriteFailures(t *testing.T) {
	t.Run("chmod failure on existing file returns error", func(t *testing.T) {
		path := "/proc/self/status"
		err := WriteJSON(path, map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error")
		}
		w, ok := err.(interface{ Unwrap() error })
		if !ok {
			t.Fatalf("error %v does not wrap a cause", err)
		}
		pe, ok := w.Unwrap().(*os.PathError)
		if !ok {
			t.Fatalf("got cause %T, want *os.PathError", w.Unwrap())
		}
		if pe.Op != "chmod" || pe.Path != path {
			t.Fatalf("got %s on %s, want chmod on %s", pe.Op, pe.Path, path)
		}
	})

	t.Run("write failure returns error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "snapshot.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		err := WriteJSON(path, map[string]int{"a": 1})
		if err == nil {
			t.Fatal("expected error")
		}
		w, ok := err.(interface{ Unwrap() error })
		if !ok {
			t.Fatalf("error %v does not wrap a cause", err)
		}
		pe, ok := w.Unwrap().(*os.PathError)
		if !ok {
			t.Fatalf("got cause %T, want *os.PathError", w.Unwrap())
		}
		if pe.Op != "open" || pe.Path != path {
			t.Fatalf("got %s on %s, want open on %s", pe.Op, pe.Path, path)
		}
	})
}

func TestExcludedIgnoresEmptyGlob(t *testing.T) {
	c := Config{ExcludeProjectGlobs: []string{""}}
	if c.Excluded("") {
		t.Fatal("empty glob must not match an empty project path")
	}
}

func TestExcludedExactPattern(t *testing.T) {
	c := Config{ExcludeProjectGlobs: []string{"/work/app"}}
	if !c.Excluded("/work/app") {
		t.Fatal("expected exact path to be excluded")
	}
	if c.Excluded("/work/app/sub") {
		t.Fatal("expected non-identical path not to be excluded")
	}
}
