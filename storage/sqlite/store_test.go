package sqlite_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/workflow"
)

func key() workflow.Key {
	return workflow.Key{Workspace: "w", Tenant: "t", Employee: "e", Scene: "s", Lane: "foreground", Subject: "interactive"}
}
func open(t *testing.T, dir string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestTransactionalStateJournalAndScopedCheckpointSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	err := s.Transaction(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.Put(ctx, sqlite.Document{Namespace: "tasks", Scope: "owner-one", ID: "task-one", Body: json.RawMessage(`{"state":"running"}`)}); err != nil {
			return err
		}
		_, err := tx.Append(ctx, sqlite.Event{Namespace: "effects", Scope: "owner-one", ID: "source/call", Kind: "dispatch", Body: json.RawMessage(`{"task_id":"task-one"}`)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	err = s.Transaction(ctx, func(tx *sqlite.Tx) error {
		if _, err := tx.Put(ctx, sqlite.Document{Namespace: "tasks", Scope: "owner-one", ID: "task-two", Body: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, "tasks", "owner-one", "task-two"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("partial transaction committed", err)
	}
	cp, err := s.Checkpoints("owner-one", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err = cp.Set(ctx, "run-one", []byte("opaque-native-checkpoint")); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer s.Close()
	doc, err := s.Get(ctx, "tasks", "owner-one", "task-one")
	if err != nil || doc.Revision != 1 {
		t.Fatal(doc, err)
	}
	events, err := s.Events(ctx, "effects", "owner-one", 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	cp, _ = s.Checkpoints("owner-one", "v1")
	data, exists, err := cp.Get(ctx, "run-one")
	if err != nil || !exists || string(data) != "opaque-native-checkpoint" {
		t.Fatal(string(data), exists, err)
	}
	for _, pair := range [][2]string{{"owner-two", "v1"}, {"owner-one", "v2"}} {
		other, _ := s.Checkpoints(pair[0], pair[1])
		if _, exists, err = other.Get(ctx, "run-one"); err != nil || exists {
			t.Fatal("checkpoint crossed scope/version", pair, exists, err)
		}
	}
	if _, err = s.Get(ctx, "tasks", "owner-two", "task-one"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("document crossed scope", err)
	}
}
func TestConcurrentCASAndEventIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, b := open(t, dir), open(t, dir)
	defer a.Close()
	defer b.Close()
	d := sqlite.Document{Namespace: "tasks", Scope: "owner", ID: "one", Body: json.RawMessage(`{}`)}
	if _, err := a.Put(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Revision = 1
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, s := range []*sqlite.Store{a, b} {
		wg.Go(func() { _, err := s.Put(ctx, d); out <- err })
	}
	wg.Wait()
	close(out)
	success, conflicts := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, sqlite.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	appendEvent := func(body string) error {
		return a.Transaction(ctx, func(tx *sqlite.Tx) error {
			_, err := tx.Append(ctx, sqlite.Event{Namespace: "effects", Scope: "owner", ID: "one", Kind: "dispatch", Body: json.RawMessage(body)})
			return err
		})
	}
	if err := appendEvent(`{"task":"one"}`); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(`{"task":"one"}`); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(`{"task":"other"}`); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("reused receipt changed effect", err)
	}
}
func TestSQLiteWorkflowLeaseRevisionAndBusy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, b := open(t, dir), open(t, dir)
	defer a.Close()
	defer b.Close()
	l, err := a.Acquire(ctx, key())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(ctx, key()); !errors.Is(err, workflow.ErrBusy) {
		t.Fatal("parallel session admitted", err)
	}
	if err = a.Close(); !errors.Is(err, sqlite.ErrBusy) {
		t.Fatal("closed store with active lease", err)
	}
	snapshot, err := l.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Key = key()
	snapshot.Kind = "employee"
	snapshot.Version = "v1"
	if err = l.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err = l.Save(ctx, snapshot); !errors.Is(err, workflow.ErrConflict) {
		t.Fatal("stale snapshot accepted", err)
	}
	if err = l.Release(); err != nil {
		t.Fatal(err)
	}
	l, err = b.Acquire(ctx, key())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	snapshot, err = l.Load(ctx)
	if err != nil || snapshot.Revision != 1 {
		t.Fatal(snapshot, err)
	}
}
func TestSQLiteLeaseReleasedByProcessDeath(t *testing.T) {
	if dir := os.Getenv("EEL_SQLITE_LOCK_CHILD"); dir != "" {
		s := open(t, dir)
		defer s.Close()
		l, err := s.Acquire(context.Background(), key())
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		fmt.Println("locked")
		<-time.After(30 * time.Second)
		t.Fatal("parent did not terminate child")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteLeaseReleasedByProcessDeath$")
	cmd.Env = append(os.Environ(), "EEL_SQLITE_LOCK_CHILD="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if line, e := bufio.NewReader(stdout).ReadString('\n'); e != nil || line != "locked\n" {
		t.Fatal(line, e)
	}
	s := open(t, dir)
	defer s.Close()
	if _, err = s.Acquire(context.Background(), key()); !errors.Is(err, workflow.ErrBusy) {
		t.Fatal("child lock not held", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	l, err := s.Acquire(context.Background(), key())
	if err != nil {
		t.Fatal("dead process left a permanent lock", err)
	}
	if err = l.Release(); err != nil {
		t.Fatal(err)
	}
}
