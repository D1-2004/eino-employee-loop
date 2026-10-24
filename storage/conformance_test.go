package storage_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/D1-2004/eino-employee-loop/contextbuilder"
	"github.com/D1-2004/eino-employee-loop/interaction"
	"github.com/D1-2004/eino-employee-loop/storage"
	"github.com/D1-2004/eino-employee-loop/storage/markdown"
	"github.com/D1-2004/eino-employee-loop/storage/memory"
	"github.com/D1-2004/eino-employee-loop/storage/sqlite"
	"github.com/D1-2004/eino-employee-loop/work"
	"github.com/D1-2004/eino-employee-loop/workflow"
)

func adapters(t *testing.T) map[string]func(*testing.T) (storage.State, storage.Artifacts) {
	return map[string]func(*testing.T) (storage.State, storage.Artifacts){
		"sqlite-markdown": func(t *testing.T) (storage.State, storage.Artifacts) {
			db, e := sqlite.Open(context.Background(), t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			files, e := markdown.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			return db, files
		},
		"memory": func(t *testing.T) (storage.State, storage.Artifacts) { return memory.New(), memory.NewArtifacts() },
	}
}

func TestAdaptersPreserveAtomicCASDetachedReadsAndArtifacts(t *testing.T) {
	for name, open := range adapters(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, files := open(t)
			t.Cleanup(func() { db.Close(); files.Close() })
			d := storage.Document{Namespace: "aggregate", Scope: "scope", ID: "one", Body: []byte(`{"value":1}`)}
			rollback := errors.New("reject aggregate")
			var escaped storage.Tx
			err := db.Transact(ctx, func(tx storage.Tx) error {
				escaped = tx
				if _, e := tx.Put(ctx, d); e != nil {
					return e
				}
				if _, e := tx.Append(ctx, storage.Event{Namespace: "events", Scope: "scope", ID: "op", Kind: "changed", Body: d.Body}); e != nil {
					return e
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if _, e := db.Get(ctx, d.Namespace, d.Scope, d.ID); !errors.Is(e, storage.ErrNotFound) {
				t.Fatal("rolled back aggregate remained", e)
			}
			events, e := db.Events(ctx, "events", "scope", 0, 100)
			if e != nil || len(events) != 0 {
				t.Fatal("rolled back event remained", events, e)
			}
			if _, e = escaped.Put(ctx, d); e == nil {
				t.Fatal("escaped transaction remained usable")
			}
			rev, e := db.Put(ctx, d)
			if e != nil || rev != 1 {
				t.Fatal(rev, e)
			}
			d.Body[2] = 'X'
			read, e := db.Get(ctx, "aggregate", "scope", "one")
			if e != nil || string(read.Body) != `{"value":1}` {
				t.Fatal("adapter retained input slice", read, e)
			}
			read.Body[2] = 'Y'
			read, e = db.Get(ctx, "aggregate", "scope", "one")
			if e != nil || string(read.Body) != `{"value":1}` {
				t.Fatal("adapter leaked stored slice", read, e)
			}
			err = db.Transact(ctx, func(tx storage.Tx) error {
				if _, e := tx.Put(ctx, storage.Document{Namespace: "aggregate", Scope: "scope", ID: "two", Body: []byte(`{}`)}); e != nil {
					return e
				}
				_, e := tx.Put(ctx, storage.Document{Namespace: "aggregate", Scope: "scope", ID: "one", Body: []byte(`{}`)})
				return e
			})
			if !errors.Is(err, storage.ErrConflict) {
				t.Fatal(err)
			}
			if _, e = db.Get(ctx, "aggregate", "scope", "two"); !errors.Is(e, storage.ErrNotFound) {
				t.Fatal("CAS failure left partial write", e)
			}
			event := storage.Event{Namespace: "events", Scope: "scope", ID: "op", Kind: "changed", Body: []byte(`{}`)}
			err = db.Transact(ctx, func(tx storage.Tx) error {
				first, e := tx.Append(ctx, event)
				if e != nil {
					return e
				}
				second, e := tx.Append(ctx, event)
				if first != second {
					t.Fatalf("event replay changed sequence %d %d", first, second)
				}
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			event.Kind = "different"
			if e = db.Transact(ctx, func(tx storage.Tx) error { _, e := tx.Append(ctx, event); return e }); !errors.Is(e, storage.ErrConflict) {
				t.Fatal(e)
			}
			if _, e = files.Read(ctx, "a.md"); !errors.Is(e, fs.ErrNotExist) {
				t.Fatal(e)
			}
			f, e := files.Write(ctx, "a.md", "", "hello")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = files.Write(ctx, "a.md", "", "overwrite"); !errors.Is(e, storage.ErrArtifactConflict) {
				t.Fatal(e)
			}
			if _, e = files.Write(ctx, "a.md", f.SHA256, "updated"); e != nil {
				t.Fatal(e)
			}
		})
	}
}

type acceptedAfterDisconnect struct {
	local interaction.Local
	sends int
}

func (p *acceptedAfterDisconnect) Send(ctx context.Context, in interaction.Intent) (interaction.Receipt, error) {
	p.sends++
	r, e := p.local.Send(ctx, in)
	if e != nil {
		return r, e
	}
	return r, errors.New("connection lost after acceptance")
}
func (p *acceptedAfterDisconnect) Lookup(ctx context.Context, in interaction.Intent) (interaction.Lookup, error) {
	return p.local.Lookup(ctx, in)
}

func TestAdaptersRunWorkAggregateAndReconcileOutboxWithoutResend(t *testing.T) {
	for name, open := range adapters(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, files := open(t)
			t.Cleanup(func() { db.Close(); files.Close() })
			manager, e := work.New(db)
			if e != nil {
				t.Fatal(e)
			}
			scope := work.Scope{Workspace: "w", Tenant: "t", Employee: "e", Scene: "s", Requester: "owner"}
			source := work.Source{Receipt: "input", Actor: "owner", Text: "write a report"}
			def := work.Definition{Goal: "verified report", Instruction: "write and verify", MaxRuns: 2}
			r, e := manager.Dispatch(ctx, scope, source, "call", def)
			if e != nil {
				t.Fatal(e)
			}
			again, e := manager.Dispatch(ctx, scope, source, "call", def)
			if e != nil || again != r {
				t.Fatal("dispatch replay changed receipt", again, e)
			}
			key, _ := scope.Key()
			events, e := db.Events(ctx, "work-events", key, 0, 100)
			if e != nil || len(events) != 1 {
				t.Fatal("aggregate event not atomic/idempotent", events, e)
			}
			def.Goal = "different"
			if _, e = manager.Dispatch(ctx, scope, source, "call", def); !errors.Is(e, storage.ErrConflict) {
				t.Fatal(e)
			}
			task, e := manager.Get(ctx, scope, r.TaskID)
			if e != nil {
				t.Fatal(e)
			}
			run := task.CurrentRun()
			if _, e = manager.BeginSubmission(ctx, scope, task.ID, run.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = manager.Submission(ctx, scope, task.ID, run.ID, "exec", false); e != nil {
				t.Fatal(e)
			}
			if _, e = manager.RecordExecution(ctx, scope, task.ID, "exit", work.ExecutionFact{RunID: run.ID, ExecutionID: "exec", State: work.RunSucceeded, ExitProven: true, Output: "verified output"}); e != nil {
				t.Fatal(e)
			}
			task, e = manager.Get(ctx, scope, task.ID)
			if e != nil {
				t.Fatal(e)
			}
			if task.State == work.Completed || !task.NeedsReview {
				t.Fatal("run exit falsely completed goal", task)
			}
			if _, e = manager.ApplyReview(ctx, scope, task.ID, "review", work.Review{TaskRevision: task.Revision, GoalRevision: task.Goal.Revision, RunID: run.ID, Kind: "complete", Summary: "verified goal"}); e != nil {
				t.Fatal(e)
			}
			port := &acceptedAfterDisconnect{local: interaction.Local{Files: files}}
			outbox := &interaction.Manager{DB: db, Port: port, Owner: "one", Authorize: func(context.Context, work.Scope) error { return nil }}
			intent := interaction.Intent{ID: "reply", SourceReceipt: "input", TaskID: task.ID, Kind: "reply", Body: "report done", Scope: scope}
			if _, e = outbox.Enqueue(ctx, intent); e != nil {
				t.Fatal(e)
			}
			if got, e := outbox.Deliver(ctx, scope, intent.ID); e == nil || got.State != "unknown" {
				t.Fatal(got, e)
			}
			outbox = &interaction.Manager{DB: db, Port: port, Owner: "two", Authorize: outbox.Authorize}
			got, e := outbox.Deliver(ctx, scope, intent.ID)
			if e != nil || got.State != "applied" || port.sends != 1 {
				t.Fatal("recovery resent accepted message", got, e, port.sends)
			}
			if got.Receipt.BodySHA256 != contextbuilder.Digest([]byte(intent.Body)) {
				t.Fatal("receipt hash changed", got)
			}
		})
	}
}

func TestAdaptersCheckpointIsolationAndLeaseFencing(t *testing.T) {
	for name, open := range adapters(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, files := open(t)
			t.Cleanup(func() { db.Close(); files.Close() })
			cp, e := db.CheckpointStore("scope", "v1")
			if e != nil {
				t.Fatal(e)
			}
			input := []byte("checkpoint")
			if e = cp.Set(ctx, "id", input); e != nil {
				t.Fatal(e)
			}
			input[0] = 'X'
			out, exists, e := cp.Get(ctx, "id")
			if e != nil || !exists || string(out) != "checkpoint" {
				t.Fatal(string(out), exists, e)
			}
			out[0] = 'Y'
			out, _, _ = cp.Get(ctx, "id")
			if string(out) != "checkpoint" {
				t.Fatal("checkpoint leaked slice")
			}
			other, _ := db.CheckpointStore("scope", "v2")
			if _, exists, e = other.Get(ctx, "id"); e != nil || exists {
				t.Fatal("checkpoint crossed version", exists, e)
			}
			key := workflow.Key{Workspace: "w", Tenant: "t", Employee: "e", Scene: "s", Lane: "chat", Subject: "owner"}
			lease, e := db.Acquire(ctx, key)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = db.Acquire(ctx, key); !errors.Is(e, workflow.ErrBusy) {
				t.Fatal("concurrent lease acquired", e)
			}
			if e = db.Close(); !errors.Is(e, storage.ErrBusy) {
				t.Fatal("closed active workflow", e)
			}
			if _, e = lease.Load(ctx); e != nil {
				t.Fatal(e)
			}
			snap := workflow.Snapshot{Key: key, Version: "v1"}
			if e = lease.Save(ctx, snap); e != nil {
				t.Fatal(e)
			}
			if e = lease.Save(ctx, snap); !errors.Is(e, workflow.ErrConflict) {
				t.Fatal("stale workflow revision accepted", e)
			}
			if e = lease.Release(); e != nil {
				t.Fatal(e)
			}
			next, e := db.Acquire(ctx, key)
			if e != nil {
				t.Fatal(e)
			}
			defer next.Release()
			snap, e = next.Load(ctx)
			if e != nil || snap.Revision != 1 {
				t.Fatal(snap, e)
			}
			if e = lease.Save(ctx, snap); !errors.Is(e, workflow.ErrBusy) {
				t.Fatal("released owner wrote", e)
			}
		})
	}
}

// The aggregate must not persist its Task or receipt if the final event append
// fails. This exercises the business transaction, not just adapter CRUD.
type appendFailureState struct{ storage.State }
type appendFailureTx struct{ storage.Tx }

func (s appendFailureState) Transact(ctx context.Context, fn func(storage.Tx) error) error {
	return s.State.Transact(ctx, func(tx storage.Tx) error { return fn(appendFailureTx{tx}) })
}

var errEventUnavailable = errors.New("event sink unavailable")

func (appendFailureTx) Append(context.Context, storage.Event) (int64, error) {
	return 0, errEventUnavailable
}

func TestAggregateAppendFailureRollsBackTaskAndReceipt(t *testing.T) {
	for name, open := range adapters(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, files := open(t)
			t.Cleanup(func() { db.Close(); files.Close() })
			manager, e := work.New(appendFailureState{db})
			if e != nil {
				t.Fatal(e)
			}
			scope := work.Scope{Workspace: "w", Tenant: "t", Employee: "e", Scene: "s", Requester: "owner"}
			source := work.Source{Receipt: "input", Actor: "owner", Text: "write report"}
			def := work.Definition{Goal: "verified report", Instruction: "write and verify", MaxRuns: 2}
			if _, e = manager.Dispatch(ctx, scope, source, "call", def); !errors.Is(e, errEventUnavailable) {
				t.Fatal(e)
			}
			key, _ := scope.Key()
			for _, namespace := range []string{"tasks", "work-receipts"} {
				docs, e := db.List(ctx, namespace, key)
				if e != nil || len(docs) != 0 {
					t.Fatal("partial business transaction", namespace, docs, e)
				}
			}
			healthy, _ := work.New(db)
			if _, e = healthy.Dispatch(ctx, scope, source, "call", def); e != nil {
				t.Fatal("failed receipt poisoned retry", e)
			}
		})
	}
}
