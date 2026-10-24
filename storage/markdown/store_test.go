package markdown_test

import (
	"context"
	"errors"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkdownCASRestartAndEscape(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := markdown.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Write(ctx, "owners/one/memory.md", "", "# Memory\n\nExplicit fact.\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Write(ctx, first.Path, "", "overwrite"); !errors.Is(err, markdown.ErrConflict) {
		t.Fatal("create overwrote prior state", err)
	}
	next, err := s.Write(ctx, first.Path, first.SHA256, "# Memory\n\nCorrected explicit fact.\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Write(ctx, first.Path, first.SHA256, "stale"); !errors.Is(err, markdown.ErrConflict) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = markdown.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	read, err := s.Read(ctx, first.Path)
	if err != nil || read != next {
		t.Fatal(read, err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside.md", "/outside.md", "escape/outside.md"} {
		if _, err = s.Write(ctx, name, "", "must not escape"); err == nil {
			t.Fatal("path escaped workspace", name)
		}
	}
	if _, err = os.Stat(filepath.Join(outside, "outside.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("escape wrote an external file", err)
	}
}
