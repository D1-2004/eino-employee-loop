package checkpoint

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteCheckPointStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cp.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, ok, err := s.Get(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}

	payload := []byte("hello-checkpoint")
	if err := s.Set(ctx, "sess-1", payload); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "sess-1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q", got)
	}

	if err := s.Set(ctx, "sess-1", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "sess-1")
	if string(got) != "v2" {
		t.Fatalf("overwrite: %q", got)
	}

	if err := s.Delete(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = s.Get(ctx, "sess-1")
	if err != nil || ok {
		t.Fatalf("after delete: ok=%v err=%v", ok, err)
	}
}

func TestAttachSameDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := Attach(s1.DB())
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Set(ctx, "a", []byte("x"))
	got, ok, err := s1.Get(ctx, "a")
	if err != nil || !ok || string(got) != "x" {
		t.Fatalf("shared: ok=%v got=%q err=%v", ok, got, err)
	}
}
