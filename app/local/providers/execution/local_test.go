package execution_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	localexec "github.com/D1-2004/eino-employee-loop/app/local/providers/execution"
	"github.com/D1-2004/eino-employee-loop/app/local/providers/storage/sqlite"
	contextbuilder "github.com/D1-2004/eino-employee-loop/employee/context"
	"github.com/D1-2004/eino-employee-loop/employee/execution"
	work "github.com/D1-2004/eino-employee-loop/employee/task/state"
)

func fixture(t *testing.T) (*localexec.Local, execution.Request, string) {
	t.Helper()
	dir := t.TempDir()
	db, e := sqlite.Open(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	s := work.Scope{Workspace: "local", Tenant: "tenant", Employee: "employee", Scene: "scene", Requester: "owner"}
	l := &localexec.Local{DB: db, Owner: "process-one", Authorize: func(_ context.Context, scope work.Scope) error {
		if scope != s {
			return work.ErrScope
		}
		return nil
	}}
	request := execution.Request{Task: work.Task{ID: "task", Scope: s, Goal: work.Goal{Revision: 1, State: work.Running}, State: work.Running, Runs: []work.Run{{ID: "run", State: work.RunPreparing, SubmitKey: "stable-submit", GoalRevision: 1}}}, Frame: contextbuilder.Frame{Scope: s, View: contextbuilder.Execution}, Configuration: []byte(`{"version":1}`)}
	return l, request, dir
}
func TestFrozenSubmissionAndReconstructionPreserveSingleExit(t *testing.T) {
	l, in, dir := fixture(t)
	ctx := context.Background()
	prepared, e := l.Submit(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	same, e := l.Submit(ctx, in)
	if e != nil || same.ID != prepared.ID {
		t.Fatal(same, e)
	}
	changed := in
	changed.Configuration = []byte(`{"version":2}`)
	if _, e = l.Submit(ctx, changed); !errors.Is(e, sqlite.ErrConflict) {
		t.Fatal(e)
	}
	if e = l.DB.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := sqlite.Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	l = &localexec.Local{DB: db, Owner: "process-two", Authorize: l.Authorize}
	found, e := l.Lookup(ctx, in.Task.Scope, in.Task.CurrentRun().SubmitKey)
	if e != nil || found.ID != prepared.ID {
		t.Fatal(found, e)
	}
	var calls atomic.Int32
	kernel := func(context.Context, execution.Record) (execution.Result, error) {
		calls.Add(1)
		return execution.Result{Exited: true, Output: "verified output"}, nil
	}
	exited, e := l.Advance(ctx, in.Task.Scope, found.SubmitKey, kernel)
	if e != nil || exited.State != "exited" || !exited.Result.Exited {
		t.Fatal(exited, e)
	}
	replay, e := l.Advance(ctx, in.Task.Scope, found.SubmitKey, kernel)
	if e != nil || replay.Result.Output != "verified output" || calls.Load() != 1 {
		t.Fatal(replay, e, calls.Load())
	}
	other := in.Task.Scope
	other.Requester = "intruder"
	if _, e = l.Lookup(ctx, other, found.SubmitKey); !errors.Is(e, work.ErrScope) {
		t.Fatal(e)
	}
}
func TestActiveInvocationCannotDuplicateAndCancelledExitRemainsRecoverable(t *testing.T) {
	l, in, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepared, e := l.Submit(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	kernel := func(context.Context, execution.Record) (execution.Result, error) {
		close(started)
		<-release
		return execution.Result{Output: "unsettled"}, context.Canceled
	}
	go func() { _, e := l.Advance(ctx, in.Task.Scope, prepared.SubmitKey, kernel); done <- e }()
	<-started
	if _, e = l.Advance(context.Background(), in.Task.Scope, prepared.SubmitKey, kernel); !errors.Is(e, execution.ErrBusy) {
		t.Fatal(e)
	}
	cancel()
	close(release)
	if e = <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	saved, e := l.Lookup(context.Background(), in.Task.Scope, prepared.SubmitKey)
	if e != nil || saved.State != "recovery" || saved.Result.Exited {
		t.Fatal(saved, e)
	}
	recovered, e := l.Advance(context.Background(), in.Task.Scope, prepared.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
		return execution.Result{Exited: true, Output: "recovered"}, nil
	})
	if e != nil || recovered.State != "exited" || recovered.Result.Output != "recovered" {
		t.Fatal(recovered, e)
	}
}
func TestKernelFailureIsDurableExitAndMissingExitIsNotInvented(t *testing.T) {
	l, in, _ := fixture(t)
	ctx := context.Background()
	r, e := l.Submit(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	got, e := l.Advance(ctx, in.Task.Scope, r.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
		return execution.Result{}, errors.New("tool failed")
	})
	if e != nil || !got.Result.Exited || got.Result.Failure != "tool failed" {
		t.Fatal(got, e)
	}
	in.Task.Runs[0].SubmitKey = "second-submit"
	in.Task.Runs[0].ID = "second-run"
	r, e = l.Submit(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	got, e = l.Advance(ctx, in.Task.Scope, r.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
		return execution.Result{Output: "no exit proof"}, nil
	})
	if e == nil || got.State != "recovery" || got.Result.Exited {
		t.Fatal("kernel without exit proof frozen as exited", got, e)
	}
}

func TestStopRequiresInactiveLocalInvocationAndPersistsExactExit(t *testing.T) {
	l, in, dir := fixture(t)
	ctx := context.Background()
	r, e := l.Submit(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := l.Advance(ctx, in.Task.Scope, r.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
			close(started)
			<-release
			return execution.Result{}, nil
		})
		done <- e
	}()
	<-started
	if _, e = l.Stop(ctx, in.Task.Scope, r.SubmitKey); !errors.Is(e, execution.ErrBusy) {
		t.Fatal("stopped an active invocation", e)
	}
	close(release)
	if e = <-done; !errors.Is(e, execution.ErrIncomplete) {
		t.Fatal(e)
	}
	if e = l.DB.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := sqlite.Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	l = &localexec.Local{DB: db, Owner: "new-leader", Authorize: l.Authorize}
	stopped, e := l.Stop(ctx, in.Task.Scope, r.SubmitKey)
	if e != nil || stopped.State != "exited" || !stopped.Result.Exited || stopped.Result.Failure != "local invocation stopped" {
		t.Fatal(stopped, e)
	}
	replay, e := l.Advance(ctx, in.Task.Scope, r.SubmitKey, func(context.Context, execution.Record) (execution.Result, error) {
		t.Fatal("stopped invocation restarted")
		return execution.Result{}, nil
	})
	if e != nil || replay.Result.Failure != stopped.Result.Failure {
		t.Fatal(replay, e)
	}
	if _, e = l.Stop(ctx, in.Task.Scope, "never-submitted"); !errors.Is(e, sqlite.ErrNotFound) {
		t.Fatal("invented execution record for absent submit", e)
	}
}
